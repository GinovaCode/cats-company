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

it('offers a cancel action while the copy is still pending or unconfirmed', async () => {
  const cancel = vi.fn();
  const create = vi.fn();
  await act(async () => root.render(<ConversationForkNotice canCreate fork={{
    operation: { state: 'pending' }, create, cancel,
  }} />));
  const button = [...container.querySelectorAll('button')].find(item => item.textContent.includes('取消复制'));
  expect(button).toBeTruthy();
  await act(async () => button.click());
  expect(cancel).toHaveBeenCalledTimes(1);
  expect(create).not.toHaveBeenCalled();
  await act(async () => root.render(<ConversationForkNotice canCreate fork={{ error: 'offline', create, cancel }} />));
  expect(container.textContent).toContain('取消复制');
});

it('hides the cancel action once the copy is ready or failed', async () => {
  const cancel = vi.fn();
  await act(async () => root.render(<ConversationForkNotice fork={{ operation: { state: 'ready' }, cancel }} />));
  expect(container.textContent).toContain('新会话已就绪');
  expect(container.textContent).not.toContain('取消复制');
  await act(async () => root.render(<ConversationForkNotice fork={{
    operation: { state: 'failed', error: 'session_fork_ack_timeout' }, cancel,
  }} />));
  expect(container.textContent).toContain('升级');
  expect(container.textContent).not.toContain('取消复制');
});

it('lets the user close a failed copy report without pretending it is writable', async () => {
  const dismissNotice = vi.fn();
  const failed = { request_id: 'r1', source_topic_id: 'p2p_1_2', target_topic_id: 'grp_9', state: 'failed', error: 'session_fork_ack_timeout' };
  await act(async () => root.render(<ConversationForkNotice fork={{ provenance: failed, blocked: true, dismissNotice }} />));
  expect(container.textContent).toContain('升级');
  const close = container.querySelector('[aria-label="关闭提示"]');
  expect(close).toBeTruthy();
  await act(async () => close.click());
  expect(dismissNotice).toHaveBeenCalledTimes(1);
  // The notice is only a report; closing it must not invent a write action.
  expect(container.textContent).not.toContain('打开新会话');
});

it('keeps showing a failure report until it is dismissed', async () => {
  const failed = { request_id: 'r1', source_topic_id: 'p2p_1_2', target_topic_id: 'grp_9', state: 'failed', error: 'session_fork_ack_timeout' };
  await act(async () => root.render(<ConversationForkNotice fork={{ provenance: failed, blocked: true, dismissNotice: vi.fn() }} />));
  expect(container.querySelector('[role="alert"]')).toBeTruthy();
  await act(async () => root.render(<ConversationForkNotice fork={{
    provenance: failed, blocked: true, noticeDismissed: true, dismissNotice: vi.fn(),
  }} />));
  expect(container.querySelector('[role="alert"]')).toBeNull();
});

it('does not offer a close control for a copy that is only still initializing', async () => {
  const pending = { request_id: 'r1', source_topic_id: 'p2p_1_2', target_topic_id: 'grp_9', state: 'pending' };
  await act(async () => root.render(<ConversationForkNotice fork={{ provenance: pending, blocked: true, dismissNotice: vi.fn() }} />));
  expect(container.textContent).toContain('初始化');
  expect(container.querySelector('[aria-label="关闭提示"]')).toBeNull();
});
