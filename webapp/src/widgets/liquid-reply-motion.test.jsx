import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { FadingMarkdown, FadingText, useReplyReveal } from './liquid-reply-motion';

let root, container, frames, time, nextId;
const tick = async elapsed => {
  time += elapsed;
  const pending = [...frames.values()];
  frames.clear();
  await act(async () => pending.forEach(callback => callback(time)));
};
beforeEach(() => {
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
  frames = new Map(); time = 0; nextId = 0;
  vi.spyOn(performance, 'now').mockImplementation(() => time);
  vi.stubGlobal('requestAnimationFrame', callback => { frames.set(++nextId, callback); return nextId; });
  vi.stubGlobal('cancelAnimationFrame', id => frames.delete(id));
  vi.stubGlobal('matchMedia', () => ({ matches: false, addEventListener() {}, removeEventListener() {} }));
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks(); vi.unstubAllGlobals();
});

it('reveals a network burst continuously and keeps the pace across appended chunks', async () => {
  await act(async () => root.render(<FadingText text="我可以帮你核对资料" enabled />));
  expect(container.textContent).toBe('');
  await tick(43);
  expect(container.textContent).toBe('我');
  await act(async () => root.render(<FadingText text="我可以帮你核对资料，填写表格" enabled />));
  expect(container.textContent).toBe('我');
  await tick(43);
  expect(container.textContent).toBe('我可');
  expect(container.querySelectorAll('.cc-reply-new-text')).toHaveLength(2);
});
it('never splits emoji graphemes', async () => {
  await act(async () => root.render(<FadingText text="👩‍💻完成" enabled />));
  await tick(43);
  expect(container.textContent).toBe('👩‍💻');
});
it('shows corrections immediately and keeps previously settled content immediate', async () => {
  await act(async () => root.render(<FadingText text="原来的文本" enabled />));
  await tick(43);
  await act(async () => root.render(<FadingText text="更正后的文本" enabled />));
  expect(container.textContent).toBe('更正后的文本');
  await act(async () => root.render(<FadingText text="更正后的文本，已停止" enabled={false} />));
  expect(container.textContent).toBe('更正后的文本，已停止');
  expect(container.querySelector('.cc-reply-new-text')).toBeNull();
  expect(frames.size).toBe(0);
});
it('shows history and reduced motion immediately', async () => {
  await act(async () => root.render(<FadingText text="历史回复" enabled={false} />));
  expect(container.textContent).toBe('历史回复');
  await act(async () => root.unmount());
  root = createRoot(container);
  vi.stubGlobal('matchMedia', () => ({ matches: true }));
  await act(async () => root.render(<FadingText text="减少动态" enabled />));
  expect(container.textContent).toBe('减少动态');
  expect(frames.size).toBe(0);
});
it('preserves safe Markdown structure and drains a finalized reply', async () => {
  await act(async () => root.render(<FadingMarkdown html="<p><strong>你好</strong></p><p>后续段落</p>" enabled />));
  expect(container.textContent).toBe('');
  expect(container.querySelectorAll('p')).toHaveLength(0);
  await tick(43);
  expect(container.querySelector('strong').textContent).toBe('你');
  expect(container.querySelectorAll('p')).toHaveLength(1);
  await act(async () => root.render(<FadingMarkdown html="<p><strong>你好</strong></p><p>后续段落</p>" enabled={false} />));
  expect(container.textContent).toBe('你');
  for (let i = 0; i < 8; i += 1) await tick(43);
  expect(container.querySelectorAll('p')).toHaveLength(2);
  expect(container.textContent).toBe('你好后续段落');
});
it('catches a large burst up without hundreds of trailing spans', async () => {
  await act(async () => root.render(<FadingText text={'字'.repeat(1000)} enabled />));
  for (let i = 0; i < 60; i += 1) await tick(32);
  expect(container.textContent.length).toBeGreaterThan(150);
  expect(container.textContent.length).toBeLessThan(260);
  expect(container.querySelectorAll('.cc-reply-new-text').length).toBeLessThanOrEqual(6);
});

function Reply({ text, streaming = false, arrival, immediate = false }) {
  const reveal = useReplyReveal(text, streaming, { arrival, immediate });
  return <span data-animating={reveal.animate}>{reveal.parts.slice(0, reveal.count).join('')}</span>;
}
it('animates a live whole reply but never replays it after remount', async () => {
  const arrival = {};
  const text = '我可以帮你核对资料，填写表格。';
  await act(async () => root.render(<Reply text={text} arrival={arrival} />));
  expect(container.textContent).toBe('');
  for (let i = 0; i < 5; i += 1) await tick(43);
  expect(container.textContent).toBe('我可以帮你');
  await act(async () => root.unmount());
  root = createRoot(container);
  await act(async () => root.render(<Reply text={text} arrival={arrival} />));
  expect(container.textContent).toBe(text);
  expect(container.firstChild.dataset.animating).toBe('false');
});
it('does not dump the unread queue when a final payload immediately follows a delta', async () => {
  const text = '我可以帮你核对资料，填写表格。';
  await act(async () => root.render(<Reply text="我可以" streaming />));
  await tick(43);
  await act(async () => root.render(<Reply text={text} arrival={{}} />));
  expect(container.textContent).toBe('我');
  for (let i = 0; i < 5; i += 1) await tick(43);
  expect(container.textContent.length).toBe(6);
  for (let i = 0; i < 20; i += 1) await tick(43);
  expect(container.textContent).toBe(text);
  expect(container.firstChild.dataset.animating).toBe('false');
  expect(frames.size).toBe(0);
});
it('flushes remaining received text when stopped or leaving the theme', async () => {
  await act(async () => root.render(<Reply text="已收到的回复" streaming />));
  await tick(43);
  await act(async () => root.render(<Reply text="已收到的回复" immediate />));
  expect(container.textContent).toBe('已收到的回复');
  expect(frames.size).toBe(0);
});
