import { afterEach, describe, expect, it, vi } from 'vitest';
import { captureFilePreviewDOM, filePreviewSelectionBox, visiblePreviewBox } from './file-preview-dom-screenshots';
import { captureFilePreviewScreenshots } from './file-annotations';

function bounds(left, top, width, height) {
  return { left, top, right: left + width, bottom: top + height, width, height };
}
function box(node, value) { node.getBoundingClientRect = () => value; return node; }
let root;
afterEach(() => { root?.remove(); root = null; vi.restoreAllMocks(); vi.unstubAllGlobals(); });

function textPreview() {
  root = box(document.createElement('pre'), bounds(100, 100, 800, 600));
  root.innerHTML = '<span>你好😀</span><span> second line</span>';
  document.body.append(root);
  return root;
}

describe('file preview DOM screenshot geometry', () => {
  it('clips to the scrolled preview and viewport rather than allocating the whole file', () => {
    root = box(document.createElement('div'), bounds(80, 90, 300, 200));
    root.style.overflow = 'auto';
    Object.defineProperties(root, { clientWidth: { value: 300 }, clientHeight: { value: 200 } });
    const pre = box(document.createElement('pre'), bounds(50, 40, 12000, 30000));
    root.append(pre); document.body.append(root);
    expect(visiblePreviewBox(pre)).toEqual({ x: 80, y: 90, width: 300, height: 200 });
  });

  it('reconstructs UTF-16 offsets across text nodes after native selection has gone', () => {
    const pre = textPreview();
    const measure = vi.fn(function () {
      expect(this.startContainer).toBe(pre.firstChild.firstChild);
      expect(this.startOffset).toBe(2);
      expect(this.endContainer).toBe(pre.lastChild.firstChild);
      expect(this.endOffset).toBe(3);
      return bounds(110, 120, 90, 20);
    });
    vi.spyOn(document, 'createRange').mockImplementation(() => {
      const range = new Range(); range.getBoundingClientRect = measure; return range;
    });
    expect(filePreviewSelectionBox(pre, 'text', { start: 2, end: 7, quote: '😀 se' }))
      .toEqual({ x: 110, y: 120, width: 90, height: 20 });
    expect(measure).toHaveBeenCalledTimes(1);
  });

  it('refuses changed text and changed sheets', () => {
    const pre = textPreview();
    expect(() => filePreviewSelectionBox(pre, 'text', { start: 0, end: 2, quote: 'old' })).toThrow(/原文已变化/);
    expect(() => filePreviewSelectionBox(pre, 'cells', { sheet: 'old' })).toThrow(/工作表已变化/);
  });

  it('uses every cell in the rectangular range, including reversed-diagonal drags', () => {
    root = document.createElement('div'); root.dataset.fileAnnotationSheet = 'Sheet1';
    root.innerHTML = '<table><tbody><tr><td data-file-annotation-row="1" data-file-annotation-column="1">A</td><td data-file-annotation-row="1" data-file-annotation-column="2">B</td></tr><tr><td data-file-annotation-row="2" data-file-annotation-column="1">C</td><td data-file-annotation-row="2" data-file-annotation-column="2">D</td></tr></tbody></table>';
    document.body.append(root);
    [...root.querySelectorAll('td')].forEach((node, i) => box(node, bounds(100 + (i % 2) * 100, 100 + Math.floor(i / 2) * 40, 100, 40)));
    expect(filePreviewSelectionBox(root.querySelector('table'), 'cells', {
      sheet: 'Sheet1', row_start: 1, row_end: 2, column_start: 1, column_end: 2,
    })).toEqual({ x: 100, y: 100, width: 200, height: 80 });
  });

  it('loads the signed local renderer, retries a load failure, preserves globals and emits bounded JPEG pairs', async () => {
    const pre = textPreview();
    pre.getBoundingClientRect = () => bounds(0, 0, 5000, 5000);
    vi.stubGlobal('innerWidth', 3000); vi.stubGlobal('innerHeight', 3000);
    vi.spyOn(document, 'createRange').mockImplementation(() => {
      const range = new Range(); range.getBoundingClientRect = () => bounds(100, 100, 200, 40); return range;
    });
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockReturnValue({ drawImage: vi.fn(), strokeRect: vi.fn(),
      fillRect: vi.fn(), getImageData: () => ({ data: [20, 30, 40, 255] }) });
    vi.spyOn(HTMLCanvasElement.prototype, 'toDataURL').mockReturnValue('data:image/jpeg;base64,/9j/2Q==');
    const renderer = vi.fn(async (_, options) => {
      expect(options).toMatchObject({ width: 3000, height: 3000, allowTaint: false, backgroundColor: 'rgb(20, 30, 40)' });
      const canvas = document.createElement('canvas');
      canvas.width = Math.floor(options.width * options.scale); canvas.height = Math.floor(options.height * options.scale);
      return canvas;
    });
    const originalGlobal = () => {};
    window.html2canvas = originalGlobal;
    let loads = 0;
    vi.spyOn(document.head, 'appendChild').mockImplementation(script => {
      expect(script.getAttribute('src')).toBe('/catsco-runtime/html2canvas-pro-1.6.7.min.js');
      expect(script.integrity).toBe('sha384-CqHBfwlOY3BunFNI9xxzy+h/+/df5g0tI05vSbd8kIwTTPk23b5jYDpyrlxfukc+');
      loads += 1;
      queueMicrotask(() => {
        if (loads === 1) script.onerror(new Event('error'));
        else { window.html2canvas = renderer; script.onload(new Event('load')); }
      });
      return script;
    });
    const target = { start: 0, end: 4, quote: '你好😀' };
    await expect(captureFilePreviewDOM(pre, 'text', target)).rejects.toMatchObject({ code: 'renderer-unavailable' });
    expect(window.html2canvas).toBe(originalGlobal);
    const result = await captureFilePreviewScreenshots(pre, 'text', target);
    expect(result.visiblePreview).toBe(true);
    expect(result.screenshots.map(image => image.role)).toEqual(['full', 'crop']);
    expect(result.screenshots[0]).toMatchObject({ width: 2048, height: 2048, mime_type: 'image/jpeg' });
    expect(result.screenshots[1].width).toBeLessThan(300);
    expect(window.html2canvas).toBe(originalGlobal);
    expect(loads).toBe(2);
    delete window.html2canvas;
  });
});
