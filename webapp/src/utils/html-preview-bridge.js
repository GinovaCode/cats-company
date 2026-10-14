// A private MessagePort connects the parent to its opaque HTML srcdoc. The
// frame receives preview controls only, never an auth token, source binding
// or upload capability. A separate static snapshot is rendered in the parent.
const CONTRACT = 'catsco.html-file-preview.v1';
const RENDERER = '/catsco-runtime/html2canvas-pro-1.6.7.min.js';
const INTEGRITY = 'sha384-CqHBfwlOY3BunFNI9xxzy+h/+/df5g0tI05vSbd8kIwTTPk23b5jYDpyrlxfukc+';
const connections = new WeakMap();
let rendererSource;

function failure(code, message) { return Object.assign(new Error(message), { code }); }

// Serialized into the srcdoc before application scripts. Keep self-contained.
function bootstrap({ contract, nonce, parentOrigin }) {
  const post = MessagePort.prototype.postMessage;
  let port, mode = false, busy = false, revision = 0;
  const nativeStart = MessagePort.prototype.start;
  const send = value => { if (port) post.call(port, value); };
  const viewport = () => ({ width: innerWidth, height: innerHeight, scroll_x: scrollX, scroll_y: scrollY, revision });
  const state = () => send({ type: 'state', viewport: viewport() });
  const changed = () => { revision++; state(); };
  addEventListener('resize', changed);
  if (typeof ResizeObserver === 'function') new ResizeObserver(changed).observe(document.documentElement);
  addEventListener('scroll', changed, true);
  new MutationObserver(changed).observe(document.documentElement,
    { subtree: true, childList: true, characterData: true, attributes: true });
  addEventListener('input', changed, true);
  addEventListener('change', changed, true);
  for (const type of ['pointerdown', 'pointermove', 'pointerup', 'pointercancel']) {
    document.addEventListener(type, event => {
      if (!mode || (type === 'pointerdown' && event.button !== 0)) return;
      if (type === 'pointerdown') {
        event.preventDefault();
        try { document.documentElement.setPointerCapture(event.pointerId); } catch { /* outside active pointer */ }
      }
      event.stopImmediatePropagation();
      send({ type: 'pointer', event: type, x: event.clientX, y: event.clientY });
    }, true);
  }
  document.addEventListener('click', event => { if (mode) { event.preventDefault(); event.stopImmediatePropagation(); } }, true);
  document.addEventListener('keydown', event => { if (event.key === 'Escape') send({ type: 'escape' }); }, true);
  addEventListener('message', event => {
    if (event.source !== parent || event.origin !== parentOrigin
      || event.data?.contract !== contract || event.data.nonce !== nonce || event.ports.length !== 1) return;
    event.stopImmediatePropagation();
    // Reconnect when the same document emits another load event.
    if (port) { port.onmessage = null; port.close(); }
    port = event.ports[0];
    port.onmessage = async ({ data }) => {
      if (data?.type === 'mode') { mode = data.active === true; state(); return; }
      if (data?.type !== 'capture' || typeof data.id !== 'string') return;
      const id = data.id;
      const snapshot = JSON.stringify(viewport());
      if (busy || snapshot !== JSON.stringify(data.viewport)) { state(); send({ type: 'result', id, error: 'stale' }); return; }
      busy = true;
      try {
        const clone = document.documentElement.cloneNode(true);
        const elements = [document.documentElement, ...document.documentElement.querySelectorAll('*')];
        const cloned = [clone, ...clone.querySelectorAll('*')];
        for (let i = 0; i < elements.length; i++) {
          if (elements[i].scrollTop || elements[i].scrollLeft) {
            cloned[i].setAttribute('data-catsco-scroll-top', elements[i].scrollTop);
            cloned[i].setAttribute('data-catsco-scroll-left', elements[i].scrollLeft);
          }
        }
        const originals = document.querySelectorAll('canvas, input, textarea, select');
        const copies = clone.querySelectorAll('canvas, input, textarea, select');
        originals.forEach((element, index) => {
          const copy = copies[index];
          if (element.tagName === 'CANVAS') {
            const image = document.createElement('img');
            image.src = element.toDataURL('image/png');
            image.width = element.width; image.height = element.height;
            image.style.cssText = element.style.cssText;
            copy.replaceWith(image);
          } else if (element.tagName === 'TEXTAREA') copy.textContent = element.value;
          else if (element.tagName === 'SELECT') [...copy.options].forEach((option, i) => option.toggleAttribute('selected', element.options[i].selected));
          else { copy.setAttribute('value', element.value); copy.toggleAttribute('checked', element.checked); }
        });
        clone.querySelectorAll('script, meta, base, iframe, object, embed').forEach(node => node.remove());
        const html = '<!doctype html>' + clone.outerHTML;
        if (html.length > 4 * 1024 * 1024) throw new Error('snapshot-too-large');
        if (snapshot !== JSON.stringify(viewport())) throw new Error('stale');
        send({ type: 'result', id, html, viewport: viewport(), partial: Boolean(document.querySelector('iframe,object,embed,video')) || [...document.querySelectorAll('*')].some(node => node.shadowRoot) });
      } catch (error) {
        send({ type: 'result', id, error: ['stale', 'snapshot-too-large'].includes(error.message) ? error.message : 'unavailable' });
      } finally { busy = false; }
    };
    nativeStart.call(port);
    state();
  }, true);
  const ready = () => parent.postMessage({ contract, nonce, type: 'ready' }, parentOrigin);
  if (document.readyState === 'loading') addEventListener('DOMContentLoaded', ready, { once: true });
  else ready();
}

export function htmlPreviewDocument(html, nonce, origin = window.location.origin) {
  const doc = new DOMParser().parseFromString(html || '<!doctype html><html><body></body></html>', 'text/html');
  const script = doc.createElement('script');
  const options = JSON.stringify({ contract: CONTRACT, nonce, parentOrigin: origin }).replace(/</g, '\\u003c');
  script.textContent = `(${bootstrap.toString()})(${options});`.replace(/<\/script/gi, '<\\/script');
  doc.head.prepend(script);
  const doctype = doc.doctype ? new XMLSerializer().serializeToString(doc.doctype) : '';
  return doctype + doc.documentElement.outerHTML;
}

export function htmlPreviewReady(event, frame, nonce) {
  return event.source === frame?.contentWindow && event.origin === 'null'
    && event.data?.contract === CONTRACT && event.data.nonce === nonce && event.data.type === 'ready';
}

export function normalizeHTMLViewport(value) {
  if (!value || !Number.isInteger(value.width) || !Number.isInteger(value.height)
    || value.width < 1 || value.height < 1 || value.width > 32768 || value.height > 32768
    || !Number.isFinite(value.scroll_x) || !Number.isFinite(value.scroll_y)
    || Math.abs(value.scroll_x) > 1e7 || Math.abs(value.scroll_y) > 1e7
    || !Number.isSafeInteger(value.revision) || value.revision < 0) return null;
  return { width: value.width, height: value.height, scroll_x: value.scroll_x, scroll_y: value.scroll_y, revision: value.revision };
}

export function htmlPreviewTarget(frame, rect) {
  const viewport = connections.get(frame)?.viewport;
  if (!viewport) throw failure('unavailable', 'HTML 页面尚未就绪，请稍候再试');
  return { rect, viewport: { ...viewport } };
}

export function connectHTMLPreview(frame, nonce, active = false) {
  connections.get(frame)?.dispose();
  const channel = new MessageChannel();
  const pending = new Map();
  const handshakeTimer = setTimeout(() => {
    if (connection.viewport) return;
    connection.dispose();
    frame.dataset.fileAnnotationError = 'HTML 页面批注未能就绪，请重新打开文件';
    frame.dispatchEvent(new Event('file-annotation-viewchange', { bubbles: true }));
  }, 10000);
  const connection = { viewport: null, active, port: channel.port1,
    dispose() {
      clearTimeout(handshakeTimer);
      channel.port1.onmessage = null; channel.port1.close(); channel.port2.close();
      for (const item of pending.values()) item.reject(failure('stale', 'HTML 页面已变化，请重新框选'));
      pending.clear();
      if (connections.get(frame) === connection) { connections.delete(frame); frame.dataset.fileAnnotationReady = 'false'; }
    },
    capture(viewport, signal) {
      return new Promise((resolve, reject) => {
        if (signal?.aborted) { reject(failure('aborted', 'capture aborted')); return; }
        const id = crypto.randomUUID();
        const cleanup = () => { clearTimeout(timer); signal?.removeEventListener('abort', abort); pending.delete(id); };
        const fail = error => { cleanup(); reject(error); };
        const abort = () => fail(failure('aborted', 'capture aborted'));
        const timer = setTimeout(() => fail(failure('capture-timeout', 'HTML 截图超时，请重试当前选区')), 15000);
        signal?.addEventListener('abort', abort, { once: true });
        pending.set(id, { resolve: data => { cleanup(); resolve(data); }, reject: fail });
        channel.port1.postMessage({ type: 'capture', id, viewport });
      });
    },
  };
  connections.set(frame, connection);
  frame.dataset.fileAnnotationReady = 'false';
  delete frame.dataset.fileAnnotationError;
  channel.port1.onmessage = ({ data }) => {
    if (connections.get(frame) !== connection) return;
    if (data?.type === 'state') {
      const view = normalizeHTMLViewport(data.viewport);
      if (!view) return;
      const changed = JSON.stringify(view) !== JSON.stringify(connection.viewport);
      clearTimeout(handshakeTimer);
      connection.viewport = view;
      frame.dataset.fileAnnotationReady = 'true';
      if (changed) frame.dispatchEvent(new Event('file-annotation-viewchange', { bubbles: true }));
    } else if (data?.type === 'pointer' && connection.active
      && ['pointerdown', 'pointermove', 'pointerup', 'pointercancel'].includes(data.event)
      && Number.isFinite(data.x) && Number.isFinite(data.y) && Math.abs(data.x) <= 65536 && Math.abs(data.y) <= 65536) {
      frame.dispatchEvent(new CustomEvent('file-annotation-pointer', { bubbles: true, detail: data }));
    } else if (data?.type === 'escape') {
      frame.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true }));
    } else if (data?.type === 'result') {
      const item = pending.get(data.id);
      if (!item) return;
      if (data.error) item.reject(failure(data.error, data.error === 'stale' ? 'HTML 页面已变化，请重新框选' : 'HTML 截图未能完成，请重试'));
      else if (typeof data.html !== 'string' || data.html.length > 4 * 1024 * 1024
        || JSON.stringify(normalizeHTMLViewport(data.viewport)) !== JSON.stringify(connection.viewport)) {
        item.reject(failure('stale', 'HTML 截图已失效，请重新框选'));
      } else item.resolve(data);
    }
  };
  channel.port1.start();
  frame.contentWindow.postMessage({ contract: CONTRACT, nonce }, '*', [channel.port2]);
  channel.port1.postMessage({ type: 'mode', active });
  return connection;
}

export function setHTMLPreviewMode(frame, active) {
  const connection = connections.get(frame);
  if (connection) { connection.active = active; connection.port.postMessage({ type: 'mode', active }); }
}

function loadRenderer() {
  if (!rendererSource) rendererSource = new Promise((resolve, reject) => {
    const script = document.createElement('script');
    const fail = () => { clearTimeout(timer); script.remove(); script.onload = null; script.onerror = null; rendererSource = null; reject(failure('renderer-unavailable', '截图组件加载失败，请重试')); };
    const timer = setTimeout(fail, 10000);
    script.src = RENDERER; script.integrity = INTEGRITY; script.crossOrigin = 'anonymous';
    script.onload = () => {
      clearTimeout(timer);
      const renderer = window.html2canvas?.default || window.html2canvas;
      if (typeof renderer === 'function') resolve(renderer);
      else fail();
    };
    script.onerror = fail;
    document.head.appendChild(script);
  });
  return rendererSource;
}

export function htmlSnapshotDocument(html) {
  const doc = new DOMParser().parseFromString(html, 'text/html');
  doc.querySelectorAll('script, meta, base, iframe, object, embed').forEach(node => node.remove());
  const csp = doc.createElement('meta'); csp.httpEquiv = 'Content-Security-Policy';
  csp.content = "default-src 'none'; script-src 'none'; style-src 'unsafe-inline' http: https:; img-src data: blob: http: https:; font-src data: http: https:; frame-src 'self'; base-uri 'none'; form-action 'none'";
  doc.head.prepend(csp);
  return '<!doctype html>' + doc.documentElement.outerHTML;
}

export async function captureHTMLPreview(frame, target, { signal } = {}) {
  const connection = connections.get(frame);
  const check = () => {
    if (signal?.aborted) throw failure('aborted', 'capture aborted');
    if (!connection?.viewport || connections.get(frame) !== connection
      || JSON.stringify(target.viewport) !== JSON.stringify(connection.viewport)) throw failure('stale', 'HTML 页面已变化，请重新框选');
  };
  check();
  const render = await loadRenderer(); check();
  const result = await connection.capture(target.viewport, signal); check();
  // This separate static snapshot allows DOM rendering while the interactive
  // preview keeps its opaque origin. Scripts, navigation and forms are disabled.
  const snapshot = document.createElement('iframe');
  snapshot.className = 'html2canvas-container';
  snapshot.sandbox = 'allow-same-origin';
  snapshot.setAttribute('aria-hidden', 'true'); snapshot.tabIndex = -1;
  const view = target.viewport;
  snapshot.style.cssText = `position:fixed;left:-100000px;top:0;width:${view.width}px;height:${view.height}px;border:0;pointer-events:none`;
  snapshot.srcdoc = htmlSnapshotDocument(result.html);
  let timer;
  let abort;
  try {
    await new Promise((resolve, reject) => {
      const finish = error => { clearTimeout(timer); signal?.removeEventListener('abort', abort); snapshot.onload = null; error ? reject(error) : resolve(); };
      abort = () => finish(failure('aborted', 'capture aborted'));
      timer = setTimeout(() => finish(failure('capture-timeout', 'HTML 截图超时，请重试')), 10000);
      signal?.addEventListener('abort', abort, { once: true });
      snapshot.onload = () => finish(); document.body.appendChild(snapshot);
      if (signal?.aborted) abort();
    });
    check();
    const copy = snapshot.contentDocument;
    copy.querySelectorAll('[data-catsco-scroll-top], [data-catsco-scroll-left]').forEach(node => {
      node.scrollTop = Number(node.getAttribute('data-catsco-scroll-top')) || 0;
      node.scrollLeft = Number(node.getAttribute('data-catsco-scroll-left')) || 0;
    });
    const rendering = render(copy.documentElement, {
      x: view.scroll_x, y: view.scroll_y, width: view.width, height: view.height,
      scrollX: view.scroll_x, scrollY: view.scroll_y,
      windowWidth: view.width, windowHeight: view.height,
      scale: Math.min(devicePixelRatio || 1, 2048 / Math.max(view.width, view.height)),
      backgroundColor: '#ffffff', logging: false, allowTaint: false, useCORS: false,
      imageTimeout: 5000, removeContainer: true,
    });
    const canvas = await Promise.race([rendering, new Promise((_, reject) => {
      timer = setTimeout(() => reject(failure('capture-timeout', 'HTML 截图超时，请重试')), 15000);
    })]);
    check();
    return { canvas, warnings: result.partial === true ? ['嵌入页面、视频或组件内部内容未包含在截图中'] : [] };
  } finally { clearTimeout(timer); signal?.removeEventListener('abort', abort); snapshot.remove(); }
}
