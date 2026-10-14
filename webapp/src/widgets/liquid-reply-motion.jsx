import React, { useLayoutEffect, useMemo, useRef, useState } from 'react';

const segmenter = typeof Intl.Segmenter === 'function' ? new Intl.Segmenter(undefined, { granularity: 'grapheme' }) : null;
const characters = text => segmenter ? Array.from(segmenter.segment(text), part => part.segment) : Array.from(text);
const TAIL_CHARS = 6;

function useReducedMotion() {
  const [reduced, setReduced] = useState(() => Boolean(window.matchMedia?.('(prefers-reduced-motion: reduce)').matches));
  useLayoutEffect(() => {
    const media = window.matchMedia?.('(prefers-reduced-motion: reduce)');
    const update = () => setReduced(Boolean(media?.matches));
    media?.addEventListener?.('change', update);
    update();
    return () => media?.removeEventListener?.('change', update);
  }, []);
  return reduced;
}

// Arrival tokens belong only to live WebSocket messages, never loaded history.
// Remember consumed tokens so returning to a mounted/virtualized history does not replay.
const seenArrivals = new WeakSet();
const NORMAL_RATE = 24;
const CHARACTER_MS = 1000 / NORMAL_RATE;

export function useReplyReveal(text, streaming, { arrival, immediate = false } = {}) {
  const reduced = useReducedMotion();
  const parts = useMemo(() => characters(text), [text]);
  const [progress, setProgress] = useState(() => {
    const active = !reduced && !immediate && (streaming || (arrival && !seenArrivals.has(arrival)));
    return { count: active ? 0 : parts.length, active: Boolean(active) };
  });
  const model = useRef({ text, parts, ...progress, arrival });
  useLayoutEffect(() => {
    const current = model.current;
    const fresh = arrival && arrival !== current.arrival && !seenArrivals.has(arrival);
    if (arrival) seenArrivals.add(arrival);
    if (reduced || immediate) {
      current.count = parts.length;
      current.active = false;
    } else if (fresh && !text.startsWith(current.text)) {
      current.count = 0;
      current.active = true;
    } else if (!text.startsWith(current.text)) {
      // A correction is authoritative, not another copy of the reply to replay.
      current.count = parts.length;
      current.active = false;
    } else if (streaming || fresh || current.active) {
      current.active = Boolean(streaming || current.count < parts.length);
    } else current.count = parts.length;
    Object.assign(current, { text, parts, arrival });
    setProgress({ count: current.count, active: current.active });
  }, [text, parts, streaming, arrival, immediate, reduced]);
  const animate = !reduced && !immediate && progress.active;
  useLayoutEffect(() => {
    if (!animate) return undefined;
    let frame;
    let last = performance.now();
    let credit = 0;
    const step = now => {
      const current = model.current;
      const backlog = current.parts.length - current.count;
      const elapsed = Math.min(64, now - last);
      last = now;
      if (backlog > 0) {
        // Short replies keep an observable pace, even when delivered in one payload.
        // Only substantial backlogs accelerate, with a hard upper bound.
        const rate = Math.min(120, NORMAL_RATE + Math.max(0, backlog - 100) / 5);
        credit += elapsed * rate / 1000;
        const advance = Math.floor(credit);
        if (advance > 0) {
          credit -= advance;
          current.count = Math.min(current.parts.length, current.count + advance);
          current.active = Boolean(streaming || current.count < current.parts.length);
          setProgress({ count: current.count, active: current.active });
        }
      } else credit = 0;
      if (current.active) frame = requestAnimationFrame(step);
    };
    frame = requestAnimationFrame(step);
    return () => cancelAnimationFrame(frame);
  }, [animate, streaming]);
  return { parts, count: animate ? Math.min(progress.count, parts.length) : parts.length, animate };
}

export function FadingText({ text, enabled, reveal, tail = false }) {
  const own = useReplyReveal(text, enabled && !reveal);
  const state = reveal || own;
  if (!state.animate && !tail) return text;
  const visible = state.parts.slice(0, state.count);
  const start = Math.max(0, visible.length - TAIL_CHARS);
  return <>{visible.slice(0, start).join('')}{visible.slice(start).map((char, index) => (
    <span className="cc-reply-new-text" key={start + index}
      style={{ animationDelay: `${-(visible.length - start - index - 1) * CHARACTER_MS}ms` }}>{char}</span>
  ))}</>;
}

function htmlText(html) {
  const template = document.createElement('template');
  template.innerHTML = html;
  return template.content.textContent || '';
}

function progressiveHtml(html, visibleCount) {
  const template = document.createElement('template');
  template.innerHTML = html;
  const walker = document.createTreeWalker(template.content, NodeFilter.SHOW_TEXT);
  const nodes = [];
  let offset = 0;
  while (walker.nextNode()) {
    const node = walker.currentNode;
    const parts = characters(node.nodeValue);
    nodes.push({ node, parts, offset });
    offset += parts.length;
  }
  for (const { node, parts, offset: start } of nodes) {
    const end = Math.max(0, Math.min(parts.length, visibleCount - start));
    const stable = Math.max(0, Math.min(end, visibleCount - TAIL_CHARS - start));
    const fragment = document.createDocumentFragment();
    fragment.appendChild(document.createTextNode(parts.slice(0, stable).join('')));
    for (let i = stable; i < end; i += 1) {
      const span = document.createElement('span');
      span.className = 'cc-reply-new-text';
      // Preserve a character's arrival time across sanitized HTML replacements.
      span.style.animationDelay = `${-Math.max(0, visibleCount - start - i - 1) * CHARACTER_MS}ms`;
      span.textContent = parts[i];
      fragment.appendChild(span);
    }
    node.replaceWith(fragment);
  }
  // Future paragraphs/list items must not reserve empty rows in the growing bubble.
  for (const element of Array.from(template.content.querySelectorAll('*')).reverse()) {
    if (!element.textContent && !element.querySelector('img, hr')) element.remove();
  }
  return template.innerHTML;
}

export function FadingMarkdown({ html, enabled, receivedVisible = false, className }) {
  const text = useMemo(() => htmlText(html), [html]);
  const { count, animate } = useReplyReveal(text, enabled && !receivedVisible);
  const decorated = useMemo(() => receivedVisible && enabled ? progressiveHtml(html, characters(text).length)
    : animate ? progressiveHtml(html, count) : html, [animate, enabled, receivedVisible, text, html, count]);
  return <div className={className} dangerouslySetInnerHTML={{ __html: decorated }} />;
}

export function LiquidReplySurface({ children, enabled, streaming, waiting, contentKey, className = '', ...props }) {
  const ref = useRef(null);
  useLayoutEffect(() => {
    const element = ref.current;
    if (!element || !enabled) return undefined;
    const notify = () => element.dispatchEvent(new Event('cc:reply-layout', { bubbles: true }));
    const observer = typeof ResizeObserver === 'function' ? new ResizeObserver(notify) : null;
    observer?.observe(element);
    notify();
    return () => observer?.disconnect();
  }, [enabled]);
  return <div ref={ref} className={`v3-message-text${className ? ` ${className}` : ''}`} {...props}>{children}</div>;
}
