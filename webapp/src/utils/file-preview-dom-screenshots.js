// Render ordinary text/table preview DOM using the same pinned modern-CSS
// renderer shipped for Gateway annotations. No CDN or capture permission.
const RENDERER_URL = '/catsco-runtime/html2canvas-pro-1.6.7.min.js';
const RENDERER_INTEGRITY = 'sha384-CqHBfwlOY3BunFNI9xxzy+h/+/df5g0tI05vSbd8kIwTTPk23b5jYDpyrlxfukc+';
let rendererPromise;

function failure(code, message) {
  return Object.assign(new Error(message), { code });
}

function loadRenderer() {
  if (rendererPromise) return rendererPromise;
  rendererPromise = new Promise((resolve, reject) => {
    const script = document.createElement('script');
    const hadGlobal = 'html2canvas' in window;
    const previous = window.html2canvas;
    const restore = () => {
      if (hadGlobal) window.html2canvas = previous;
      else delete window.html2canvas;
      script.remove();
    };
    const timer = setTimeout(() => {
      restore(); reject(failure('renderer-unavailable', '截图组件加载超时，请重试'));
    }, 10000);
    script.src = RENDERER_URL;
    script.integrity = RENDERER_INTEGRITY;
    script.crossOrigin = 'anonymous';
    script.onload = () => {
      clearTimeout(timer);
      const renderer = window.html2canvas;
      restore();
      if (typeof renderer !== 'function') reject(failure('renderer-unavailable', '截图组件未能加载，请重试'));
      else resolve(renderer);
    };
    script.onerror = () => {
      clearTimeout(timer); restore();
      reject(failure('renderer-unavailable', '截图组件未能加载，请重试'));
    };
    document.head.appendChild(script);
  }).catch(error => { rendererPromise = null; throw error; });
  return rendererPromise;
}

// Composite ancestor backgrounds through the browser's own color parser so
// transparent previews keep their actual backdrop in light and dark themes.
function previewBackground(element) {
  const canvas = document.createElement('canvas'); canvas.width = 1; canvas.height = 1;
  const context = canvas.getContext('2d');
  if (!context) throw failure('unavailable', 'canvas is unavailable');
  context.fillStyle = '#ffffff'; context.fillRect(0, 0, 1, 1);
  const ancestors = [];
  for (let node = element.parentElement; node; node = node.parentElement) ancestors.push(node);
  for (const node of ancestors.reverse()) {
    context.fillStyle = getComputedStyle(node).backgroundColor;
    context.fillRect(0, 0, 1, 1);
  }
  const [red, green, blue] = context.getImageData(0, 0, 1, 1).data;
  return `rgb(${red}, ${green}, ${blue})`;
}

export function visiblePreviewBox(element) {
  const bounds = element?.getBoundingClientRect();
  if (!bounds) throw failure('unavailable', '当前预览不可见，请重新打开文件');
  let left = Math.max(0, bounds.left), top = Math.max(0, bounds.top);
  let right = Math.min(window.innerWidth, bounds.right), bottom = Math.min(window.innerHeight, bounds.bottom);
  for (let parent = element.parentElement; parent; parent = parent.parentElement) {
    const style = getComputedStyle(parent);
    const box = parent.getBoundingClientRect();
    if (/^(auto|scroll|hidden|clip)$/.test(style.overflowX) || /^(auto|scroll|hidden|clip)$/.test(style.overflow)) {
      left = Math.max(left, box.left + parent.clientLeft);
      right = Math.min(right, box.left + parent.clientLeft + parent.clientWidth);
    }
    if (/^(auto|scroll|hidden|clip)$/.test(style.overflowY) || /^(auto|scroll|hidden|clip)$/.test(style.overflow)) {
      top = Math.max(top, box.top + parent.clientTop);
      bottom = Math.min(bottom, box.top + parent.clientTop + parent.clientHeight);
    }
  }
  if (right <= left || bottom <= top) throw failure('unavailable', '选区已离开可见预览，请滚动到选区后重试');
  return { x: left, y: top, width: right - left, height: bottom - top };
}

export function filePreviewSelectionBox(element, kind, target) {
  if (kind === 'cells') {
    if (element.closest('[data-file-annotation-sheet]')?.dataset.fileAnnotationSheet !== target.sheet) {
      throw failure('stale', '工作表已变化，请重新选择单元格');
    }
    const cells = [...element.querySelectorAll('[data-file-annotation-row]')].filter(cell => {
      const row = Number(cell.dataset.fileAnnotationRow), column = Number(cell.dataset.fileAnnotationColumn);
      return row >= target.row_start && row <= target.row_end && column >= target.column_start && column <= target.column_end;
    });
    if (!cells.length) throw failure('unavailable', '找不到所选单元格，请重新选择');
    const boxes = cells.map(cell => cell.getBoundingClientRect());
    const left = Math.min(...boxes.map(box => box.left)), top = Math.min(...boxes.map(box => box.top));
    return { x: left, y: top, width: Math.max(...boxes.map(box => box.right)) - left,
      height: Math.max(...boxes.map(box => box.bottom)) - top };
  }
  if (element.textContent.slice(target.start, target.end) !== target.quote) {
    throw failure('stale', '原文已变化，请重新选择文本');
  }
  const walker = document.createTreeWalker(element, NodeFilter.SHOW_TEXT);
  const range = document.createRange();
  let offset = 0, start = false, end = false;
  for (let node = walker.nextNode(); node; node = walker.nextNode()) {
    const next = offset + node.textContent.length;
    if (!start && target.start >= offset && target.start < next) {
      range.setStart(node, target.start - offset); start = true;
    }
    if (start && target.end > offset && target.end <= next) {
      range.setEnd(node, target.end - offset); end = true; break;
    }
    offset = next;
  }
  if (!start || !end) throw failure('unavailable', '找不到所选文本，请重新选择');
  const box = range.getBoundingClientRect();
  return { x: box.left, y: box.top, width: box.width, height: box.height };
}

export async function captureFilePreviewDOM(element, kind, target, { signal, maxDimension = 2048 } = {}) {
  if (signal?.aborted) throw failure('aborted', 'capture aborted');
  // Measure at selection time, before renderer loading can change the viewport.
  const box = visiblePreviewBox(element);
  const selected = filePreviewSelectionBox(element, kind, target);
  if (selected.width <= 0 || selected.height <= 0
    || selected.x >= box.x + box.width || selected.y >= box.y + box.height
    || selected.x + selected.width <= box.x || selected.y + selected.height <= box.y) {
    throw failure('stale', '选区已离开可见预览，请滚动到选区后重试');
  }
  const bounds = element.getBoundingClientRect();
  const renderer = await loadRenderer();
  const backgroundColor = previewBackground(element);
  if (signal?.aborted) throw failure('aborted', 'capture aborted');
  const scale = Math.min(window.devicePixelRatio || 1, maxDimension / Math.max(box.width, box.height));
  let timer;
  const canvas = await Promise.race([
    renderer(element, {
      x: box.x - bounds.left, y: box.y - bounds.top, width: box.width, height: box.height,
      scale, windowWidth: window.innerWidth, windowHeight: window.innerHeight,
      backgroundColor, logging: false, allowTaint: false, useCORS: false,
      imageTimeout: 5000, removeContainer: true,
    }),
    new Promise((_, reject) => {
      timer = setTimeout(() => reject(failure('capture-timeout', '截图超时，请重试当前选区')), 15000);
    }),
  ]).finally(() => clearTimeout(timer));
  if (signal?.aborted) throw failure('aborted', 'capture aborted');
  const changed = (before, after) => ['x', 'y', 'width', 'height'].some(key => Math.abs(before[key] - after[key]) > 1);
  if (changed(box, visiblePreviewBox(element)) || changed(selected, filePreviewSelectionBox(element, kind, target))) {
    throw failure('stale', '预览布局已变化，请重试当前选区');
  }
  // Reuse the bounded bitmap JPEG/crop path; the synthetic box maps CSS pixels
  // to the renderer's output rather than to the full, possibly enormous file.
  return { canvas, box, selected };
}
