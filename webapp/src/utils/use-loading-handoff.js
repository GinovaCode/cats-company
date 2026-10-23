import { useLayoutEffect, useRef } from 'react';

const activeHandoffs = new WeakMap();
const loaderSelector = '.cc-loading-state, .cc-history-loading';

// Keep only a visual copy during the handoff. The real content mounts immediately,
// so requests, focus, scrolling and assistive announcements never wait for motion.
export default function useLoadingHandoff() {
  const ref = useRef(null);
  useLayoutEffect(() => {
    const node = ref.current;
    if (!node) return undefined;
    const region = node.closest('[data-loading-region]') || node.parentElement;
    activeHandoffs.get(region)?.();

    return () => {
      if (!region?.isConnected || !node.isConnected || typeof node.animate !== 'function'
        || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) return;
      const style = getComputedStyle(node);
      if (Number(style.opacity) < 0.05) return;
      const bounds = node.getBoundingClientRect();
      if (!bounds.width || !bounds.height) return;
      const clip = region.getBoundingClientRect();
      const previousChildren = new Set(region.children);
      const ghost = node.cloneNode(true);
      const layer = document.createElement('div');
      layer.className = 'cc-loading-handoff';
      layer.setAttribute('aria-hidden', 'true');
      layer.inert = true;
      Object.assign(layer.style, {
        position: 'fixed', left: `${clip.left}px`, top: `${clip.top}px`,
        width: `${clip.width}px`, height: `${clip.height}px`,
        overflow: 'hidden', pointerEvents: 'none', zIndex: '10000',
      });
      // Freeze the visible frame rather than restarting the loop as it leaves.
      const originals = [node, ...node.querySelectorAll('*')];
      const copies = [ghost, ...ghost.querySelectorAll('*')];
      originals.forEach((original, index) => {
        const current = getComputedStyle(original);
        Object.assign(copies[index].style, {
          animation: 'none', transition: 'none', opacity: current.opacity, transform: current.transform,
        });
        copies[index].removeAttribute('id');
        copies[index].removeAttribute('role');
        copies[index].removeAttribute('aria-live');
      });
      for (const property of style) {
        if (property.startsWith('--')) ghost.style.setProperty(property, style.getPropertyValue(property));
      }
      Object.assign(ghost.style, {
        position: 'absolute', inset: 'auto', left: `${bounds.left - clip.left}px`,
        top: `${bounds.top - clip.top}px`, width: `${bounds.width}px`, height: `${bounds.height}px`,
        minHeight: '0', maxHeight: 'none', margin: '0', boxSizing: 'border-box',
        color: style.color, font: style.font, transform: 'none',
      });
      layer.appendChild(ghost);
      let observer;
      let exit;
      let finished = false;
      const entrances = [];
      const finish = () => {
        if (finished) return;
        finished = true;
        observer?.disconnect();
        window.removeEventListener('scroll', finish, true);
        window.removeEventListener('resize', finish);
        exit?.cancel();
        entrances.forEach(animation => animation.cancel());
        layer.remove();
        if (activeHandoffs.get(region) === finish) activeHandoffs.delete(region);
      };
      activeHandoffs.get(region)?.();
      activeHandoffs.set(region, finish);
      // Run after React's DOM commit but before paint. Waiting for the next
      // frame can briefly paint the new content at full opacity first.
      Promise.resolve().then(() => {
        if (finished) return;
        const replacementLoading = [...region.querySelectorAll(loaderSelector)]
          .some(loader => (loader.closest('[data-loading-region]') || loader.parentElement) === region);
        if (!region.isConnected || replacementLoading
          || window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) {
          finish();
          return;
        }
        document.body.appendChild(layer);
        observer = new MutationObserver(() => {
          if (!region.isConnected) finish();
        });
        observer.observe(document.body, { childList: true, subtree: true });
        window.addEventListener('scroll', finish, { capture: true, passive: true });
        window.addEventListener('resize', finish, { passive: true });
        exit = layer.animate([{ opacity: 1 }, { opacity: 0 }], { duration: 140, easing: 'ease-out', fill: 'forwards' });
        const targets = new Set([
          ...[...region.children].filter(child => !previousChildren.has(child)),
          ...region.querySelectorAll('[data-loading-content]'),
        ]);
        for (const target of targets) {
          if ([...targets].some(parent => parent !== target && parent.contains(target))) continue;
          if (target.getBoundingClientRect().height && typeof target.animate === 'function') {
            entrances.push(target.animate([{ opacity: 0 }, { opacity: 1 }], {
              duration: 200, easing: 'cubic-bezier(.2,.7,.2,1)',
            }));
          }
        }
        Promise.allSettled([exit.finished, ...entrances.map(animation => animation.finished)]).then(finish);
      });
    };
  }, []);
  return ref;
}
