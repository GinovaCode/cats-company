import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { ConversationForkAction, ConversationForkNotice } from './conversation-fork';

let root, container;
beforeEach(() => { container = document.createElement('div'); document.body.append(container); root = createRoot(container); });
afterEach(async () => { await act(async () => root.unmount()); container.remove(); });

it('supports keyboard focus and returns focus when the menu closes', async () => {
  const create = vi.fn();
  await act(async () => root.render(<ConversationForkAction eligible onCreate={create} />));
  const trigger = container.querySelector('[aria-haspopup="menu"]');
  await act(async () => trigger.click());
  const item = container.querySelector('[role="menuitem"]');
  expect(document.activeElement).toBe(item);
  await act(async () => item.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true })));
  expect(document.activeElement).toBe(trigger);
  expect(container.querySelector('[role="menu"]')).toBeNull();
  await act(async () => trigger.click());
  await act(async () => container.querySelector('[role="menuitem"]').click());
  expect(create).toHaveBeenCalledWith({ newRequest: true });
});

it('does not offer open while pending and exposes retry without a new request', async () => {
  const create = vi.fn();
  await act(async () => root.render(<ConversationForkNotice canCreate fork={{
    operation: { state: 'pending' }, error: 'Network interrupted', create,
  }} />));
  expect(container.querySelector('[role="alert"]').textContent).toContain('Network interrupted');
  expect(container.textContent).not.toContain('打开新会话');
  await act(async () => container.querySelector('button').click());
  expect(create).toHaveBeenCalledWith();
});

it('shows explicit terminal failure with a distinct new request action', async () => {
  const create = vi.fn();
  await act(async () => root.render(<ConversationForkNotice canCreate fork={{
    operation: { state: 'failed', error: 'session_fork_source_interrupted' }, create,
  }} />));
  expect(container.querySelector('[role="alert"]').textContent).toContain('恢复或结束该任务');
  expect(container.textContent).not.toContain('打开新会话');
  await act(async () => container.querySelector('button').click());
  expect(create).toHaveBeenCalledWith({ newRequest: true });
});
