import { afterEach, describe, expect, it, vi } from 'vitest';
import { captureHTMLPreview, connectHTMLPreview, htmlPreviewDocument, htmlPreviewReady, htmlPreviewTarget, htmlSnapshotDocument, setHTMLPreviewMode } from './html-preview-bridge';

const VIEW = { width: 800, height: 600, scroll_x: 0, scroll_y: 300, revision: 0 };
const RECT = { x: .1, y: .2, width: .3, height: .4 };
let connection;
afterEach(() => { connection?.dispose(); connection = null; vi.unstubAllGlobals(); vi.restoreAllMocks(); });
function fixture() {
  const ports = [];
  class Channel {
    constructor() {
      this.port1 = { onmessage: null, postMessage: vi.fn(), start: vi.fn(), close: vi.fn() };
      this.port2 = { close: vi.fn() }; ports.push(this);
    }
  }
  vi.stubGlobal('MessageChannel', Channel);
  const frame = document.createElement('iframe'); document.body.append(frame);
  const post = vi.spyOn(frame.contentWindow, 'postMessage');
  connection = connectHTMLPreview(frame, 'nonce', false);
  const port = ports[0].port1;
  const send = data => port.onmessage({ data });
  return { frame, post, port, send };
}
describe('opaque HTML preview bridge', () => {
  it('prepends the private bridge before document scripts without enabling same-origin access', () => {
    const result = htmlPreviewDocument('<!DOCTYPE html><html lang="zh"><head><script>window.report=1</script><style>p{color:red}</style></head><body><p>报告</p></body></html>', '</script><script>bad</script>');
    const doc = new DOMParser().parseFromString(result, 'text/html');
    expect(doc.head.firstElementChild.tagName).toBe('SCRIPT');
    expect(doc.querySelectorAll('script')).toHaveLength(2);
    expect(doc.head.firstElementChild.textContent).toContain('MessagePort.prototype.postMessage');
    expect(doc.head.firstElementChild.textContent).toContain('\\u003c/script>');
    expect(doc.querySelector('p').textContent).toBe('报告');
    expect(doc.querySelector('style').textContent).toBe('p{color:red}');
  });
  it('accepts readiness only from the expected opaque frame and nonce', () => {
    const frame = document.createElement('iframe'); document.body.append(frame);
    const event = { source: frame.contentWindow, origin: 'null',
      data: { contract: 'catsco.html-file-preview.v1', nonce: 'current', type: 'ready' } };
    expect(htmlPreviewReady(event, frame, 'current')).toBe(true);
    expect(htmlPreviewReady({ ...event, source: window }, frame, 'current')).toBe(false);
    expect(htmlPreviewReady({ ...event, origin: location.origin }, frame, 'current')).toBe(false);
    expect(htmlPreviewReady(event, frame, 'old')).toBe(false);
    frame.remove();
  });
  it('routes only valid private-port events, preserves viewport location, and cleans up on reload', () => {
    const f = fixture();
    const pointer = vi.fn(), changed = vi.fn();
    f.frame.addEventListener('file-annotation-pointer', pointer);
    f.frame.addEventListener('file-annotation-viewchange', changed);
    window.dispatchEvent(new MessageEvent('message', { data: { type: 'state', viewport: VIEW } }));
    expect(() => htmlPreviewTarget(f.frame, RECT)).toThrow();
    f.send({ type: 'state', viewport: { ...VIEW, width: Infinity } });
    expect(f.frame.dataset.fileAnnotationReady).toBe('false');
    f.send({ type: 'state', viewport: VIEW });
    expect(htmlPreviewTarget(f.frame, RECT)).toEqual({ rect: RECT, viewport: VIEW });
    expect(changed).toHaveBeenCalledTimes(1);
    f.send({ type: 'pointer', event: 'pointerdown', x: 100, y: 100 });
    expect(pointer).not.toHaveBeenCalled();
    setHTMLPreviewMode(f.frame, true);
    f.send({ type: 'pointer', event: 'pointerdown', x: NaN, y: 100 });
    f.send({ type: 'pointer', event: 'pointerdown', x: 100, y: 100 });
    expect(pointer).toHaveBeenCalledTimes(1);
    const target = htmlPreviewTarget(f.frame, RECT);
    f.send({ type: 'state', viewport: { ...VIEW, scroll_y: 400, revision: 1 } });
    expect(changed).toHaveBeenCalledTimes(2);
    expect(captureHTMLPreview(f.frame, target)).rejects.toMatchObject({ code: 'stale' });
    const previous = connection;
    connection = connectHTMLPreview(f.frame, 'new-nonce');
    expect(f.port.close).toHaveBeenCalled();
    expect(f.frame.dataset.fileAnnotationReady).toBe('false');
    previous.dispose();
    f.frame.remove();
  });
  it('exports a static snapshot of live form and scroll state, and accepts parent reconnection', async () => {
    const content = document.createElement('div');
    content.innerHTML = '<input value="old"><textarea>old</textarea><div class="scroll-area"><p>content</p></div><script>window.untrusted=1</script><iframe src="/other"></iframe>';
    document.body.append(content);
    content.querySelector('input').value = 'edited';
    content.querySelector('textarea').value = 'draft';
    content.querySelector('.scroll-area').scrollTop = 120;
    const messages = new Map();
    class Port {
      constructor() { this.sent = []; this.close = vi.fn(); }
      start() {}
      postMessage(data) { this.sent.push(data); }
    }
    class Observer { observe() {} }
    const script = new DOMParser().parseFromString(htmlPreviewDocument('<p>test</p>', 'nonce'), 'text/html').querySelector('script').textContent;
    new Function('MessagePort', 'addEventListener', 'MutationObserver', 'ResizeObserver', script)(Port,
      (type, callback) => messages.set(type, callback), Observer, Observer);
    const handshake = port => messages.get('message')({ source: window.parent, origin: location.origin,
      data: { contract: 'catsco.html-file-preview.v1', nonce: 'nonce' }, ports: [port], stopImmediatePropagation() {} });
    try {
      const old = new Port(); handshake(old);
      const replacement = new Port(); handshake(replacement);
      expect(old.close).toHaveBeenCalled();
      const viewport = replacement.sent[0].viewport;
      await replacement.onmessage({ data: { type: 'capture', id: 'one', viewport } });
      const result = replacement.sent.find(data => data.id === 'one');
      expect(result.error).toBeUndefined();
      const doc = new DOMParser().parseFromString(result.html, 'text/html');
      expect(doc.querySelector('input').getAttribute('value')).toBe('edited');
      expect(doc.querySelector('textarea').textContent).toBe('draft');
      expect(doc.querySelector('.scroll-area').getAttribute('data-catsco-scroll-top')).toBe('120');
      expect(doc.querySelector('script,iframe')).toBeNull();
      await replacement.onmessage({ data: { type: 'capture', id: 'stale', viewport: { ...viewport, height: 1 } } });
      expect(replacement.sent.at(-1)).toMatchObject({ error: 'stale' });
    } finally { content.remove(); }
  });
  it('strips active snapshot content and prohibits scripts and form submission', () => {
    const html = htmlSnapshotDocument('<html><head><base href="https://other.test/"><meta http-equiv="refresh" content="0;url=https://other.test"></head><body><h1 style="color:red">Report</h1><script>fetch("/api")</script><iframe src="/api"></iframe><object data="/api"></object><form><input value="keep"></form></body></html>');
    const doc = new DOMParser().parseFromString(html, 'text/html');
    expect(doc.querySelector('script,iframe,object,base')).toBeNull();
    expect(doc.querySelectorAll('meta')).toHaveLength(1);
    expect(doc.head.firstElementChild.content).toContain("script-src 'none'");
    expect(doc.head.firstElementChild.content).toContain("form-action 'none'");
    expect(doc.querySelector('h1').style.color).toBe('red');
    expect(doc.querySelector('input').value).toBe('keep');
  });
  it('rejects in-flight capture on disposal and ignores unsolicited results', async () => {
    const f = fixture(); f.send({ type: 'state', viewport: VIEW });
    const pending = connection.capture(VIEW);
    const result = expect(pending).rejects.toMatchObject({ code: 'stale' });
    f.send({ type: 'result', id: 'unrelated', blob: new Blob() });
    connection.dispose(); await result;
    expect(f.port.close).toHaveBeenCalled(); f.frame.remove();
  });
});
