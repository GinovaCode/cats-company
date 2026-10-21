import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { useConversationFork } from './use-conversation-fork';
import { api } from '../api';

vi.mock('../api', () => ({ api: {
  forkConversation: vi.fn(), getConversationFork: vi.fn(), getConversations: vi.fn(),
} }));

const record = { request_id: 'r1', source_topic_id: 'p2p_1_2', target_topic_id: 'grp_9', state: 'pending' };
let value, root, container;
function Harness(props) { value = useConversationFork(props); return null; }
const mount = async (props = {}) => {
  await act(async () => root.render(<Harness topic="p2p_1_2" userId={1} {...props} />));
};

beforeEach(() => {
  localStorage.clear();
  vi.resetAllMocks();
  api.getConversationFork.mockRejectedValue(Object.assign(new Error('session_fork_not_found'), { status: 404 }));
  api.getConversations.mockResolvedValue({ conversations: [{ id: 'grp_9', name: 'Copy' }] });
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => { await act(async () => root.unmount()); container.remove(); vi.useRealTimers(); });

it('retains one request ID after uncertain failure, retry and remount', async () => {
  api.forkConversation.mockRejectedValueOnce(new Error('network interrupted'))
    .mockImplementation(async payload => ({ ...record, request_id: payload.request_id }));
  await mount();
  await act(async () => value.create());
  const first = api.forkConversation.mock.calls[0][0];
  expect(value.error).toContain('network interrupted');
  await act(async () => root.unmount());
  root = createRoot(container);
  await mount();
  await act(async () => value.create());
  expect(api.forkConversation.mock.calls[1][0]).toEqual(first);
  expect(value.operation.state).toBe('pending');
});

it('shares a pending request between header and sidebar instead of allocating a second copy', async () => {
  const handles = {};
  let complete;
  api.forkConversation.mockImplementation(payload => new Promise(resolve => {
    complete = () => resolve({ ...record, request_id: payload.request_id });
  }));
  api.getConversationFork.mockImplementation(async ({ requestId }) => ({ ...record, request_id: requestId }));
  function Control({ id }) {
    handles[id] = useConversationFork({ topic: 'p2p_1_2', userId: 1 });
    return null;
  }
  await act(async () => root.render(<><Control id="header" /><Control id="sidebar" /></>));
  let pending;
  await act(async () => { pending = handles.sidebar.create({ newRequest: true }); });
  expect(handles.header.operation.state).toBe('pending');
  await act(async () => handles.header.create({ newRequest: true }));
  expect(api.forkConversation).toHaveBeenCalledTimes(1);
  await act(async () => { complete(); await pending; });
  expect(handles.header.operation.request_id).toBe(handles.sidebar.operation.request_id);
});

it('rechecks after a delayed pre-creation 404 instead of losing a successful sidebar notification', async () => {
  const handles = {};
  let rejectLookup;
  let ready;
  api.getConversationFork.mockImplementationOnce(() => new Promise((_, reject) => { rejectLookup = reject; }))
    .mockImplementation(async () => ready);
  api.forkConversation.mockImplementation(async payload => {
    ready = { ...record, request_id: payload.request_id, state: 'ready' };
    return ready;
  });
  function Control({ id }) {
    handles[id] = useConversationFork({ topic: 'p2p_1_2', userId: 1 });
    return null;
  }
  await act(async () => root.render(<><Control id="header" /><Control id="sidebar" /></>));
  await act(async () => handles.sidebar.create({ newRequest: true }));
  expect(handles.header.busy).toBe(true);
  await act(async () => rejectLookup(Object.assign(new Error('session_fork_not_found'), { status: 404 })));
  expect(api.getConversationFork).toHaveBeenCalledTimes(2);
  expect(handles.header.error).toBe('');
  expect(handles.header.busy).toBe(false);
  expect(handles.header.operation.state).toBe('ready');
});

it('exposes source recovery as busy while reopening a saved request', async () => {
  localStorage.setItem('cc:conversation-fork:1:p2p_1_2', JSON.stringify({source_topic_id:'p2p_1_2',request_id:'r1'}));
  let resolveLookup;
  api.getConversationFork.mockImplementationOnce(() => new Promise(resolve => { resolveLookup=resolve; }));
  await mount();
  expect(value.busy).toBe(true);
  await act(async () => resolveLookup(record));
  expect(value.busy).toBe(false);
  expect(value.operation.state).toBe('pending');
});

it('blocks pending and failed destination composition, and unlocks only after authoritative ready', async () => {
  vi.useFakeTimers();
  api.getConversationFork.mockResolvedValueOnce(record)
    .mockResolvedValueOnce({ ...record, state: 'ready' });
  await mount({ topic: 'grp_9' });
  expect(value.blocked).toBe(true);
  expect(value.provenance.source_topic_id).toBe('p2p_1_2');
  await act(async () => vi.advanceTimersByTimeAsync(2000));
  expect(value.blocked).toBe(false);
  api.getConversationFork.mockResolvedValue({ ...record, state: 'failed', error: 'Source busy' });
  await act(async () => value.checkTarget());
  expect(value.blocked).toBe(true);
  expect(value.provenance.error).toBe('Source busy');
});

it('keeps a target blocked on network or malformed status and allows a confirmed non-fork', async () => {
  api.getConversationFork.mockRejectedValueOnce(new Error('offline'));
  await mount({ topic: 'grp_9' });
  expect(value.blocked).toBe(true);
  expect(value.targetError).toContain('offline');
  await act(async () => value.checkTarget());
  expect(value.blocked).toBe(false);
});

it('opens ready copies through the existing topic callback and refreshes normal lists', async () => {
  const activate = vi.fn();
  const changed = vi.fn();
  window.addEventListener('cc:data-changed', changed);
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id, state: 'ready' }));
  await mount({ onActivateTopic: activate });
  await act(async () => value.create());
  expect(activate).not.toHaveBeenCalled();
  await act(async () => value.open());
  expect(activate).toHaveBeenCalledWith(expect.objectContaining({ topicId: 'grp_9', name: 'Copy', isAgentTask: true }));
  expect(changed).toHaveBeenCalled();
  window.removeEventListener('cc:data-changed', changed);
});

it('does not apply late responses or open a copy after navigation', async () => {
  let finish;
  api.forkConversation.mockImplementation(payload => new Promise(resolve => { finish = () => resolve({ ...record, request_id: payload.request_id, state: 'ready' }); }));
  const activate = vi.fn();
  await mount({ onActivateTopic: activate });
  let pending;
  await act(async () => { pending = value.create(); });
  await mount({ topic: 'p2p_1_3', onActivateTopic: activate });
  await act(async () => { finish(); await pending; });
  expect(value.operation).toBeNull();
  expect(activate).not.toHaveBeenCalled();
});

it('never retries a terminal record and creates a different request only on explicit recreation', async () => {
  api.forkConversation.mockImplementation(async payload => ({ ...record, request_id: payload.request_id, state: 'failed', error: 'session_fork_source_interrupted' }));
  await mount();
  await act(async () => value.create());
  const first = api.forkConversation.mock.calls[0][0];
  await act(async () => value.create());
  expect(api.forkConversation).toHaveBeenCalledTimes(1);
  await act(async () => value.create({ newRequest: true }));
  expect(api.forkConversation.mock.calls[1][0].request_id).not.toBe(first.request_id);
});

it('does not unlock composition for an unrelated endpoint 404 or invalid readiness payload', async () => {
  api.getConversationFork.mockRejectedValueOnce(Object.assign(new Error('Not Found'), { status: 404 }))
    .mockResolvedValueOnce({ ...record, state: 'ready', target_topic_id: 'grp_10' });
  await mount({ topic: 'grp_9' });
  expect(value.blocked).toBe(true);
  await act(async () => value.checkTarget());
  expect(value.blocked).toBe(true);
  expect(value.targetError).toContain('状态无效');
});

it('coalesces duplicate clicks until the first request is answered', async () => {
  let finish;
  api.forkConversation.mockImplementation(payload => new Promise(resolve => { finish = () => resolve({ ...record, request_id: payload.request_id }); }));
  await mount();
  let pending;
  await act(async () => { pending = value.create(); await value.create(); });
  expect(api.forkConversation).toHaveBeenCalledTimes(1);
  await act(async () => { finish(); await pending; });
});

it('does not borrow readiness or provenance from the previous topic while querying a new target', async () => {
  api.getConversationFork.mockResolvedValueOnce({ ...record, state: 'ready' })
    .mockImplementationOnce(() => new Promise(() => {}));
  await mount({ topic: 'grp_9' });
  expect(value.blocked).toBe(false);
  await mount({ topic: 'grp_10' });
  expect(value.blocked).toBe(true);
  expect(value.provenance).toBeNull();
});
