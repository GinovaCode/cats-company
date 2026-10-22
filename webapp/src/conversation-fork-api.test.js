import { api, setToken } from './api';

afterEach(() => { setToken(null); vi.unstubAllGlobals(); });

it('uses authenticated POST and GET fork shapes without a client-side ready write', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
    request_id: 'request/1', source_topic_id: 'p2p_1_2', target_topic_id: 'grp_9', state: 'pending',
  }), { status: 200 }));
  vi.stubGlobal('fetch', fetchMock);
  setToken('fixture-token');
  await api.forkConversation({ source_topic_id: 'p2p_1_2', request_id: 'request/1' });
  const [url, options] = fetchMock.mock.calls[0];
  expect(url).toBe('/api/conversations/fork');
  expect(options.method).toBe('POST');
  expect(options.headers.Authorization).toBe('Bearer fixture-token');
  expect(JSON.parse(options.body)).toEqual({ source_topic_id: 'p2p_1_2', request_id: 'request/1' });
  await api.getConversationFork({ requestId: 'request/1' });
  expect(fetchMock.mock.calls[1][0]).toBe('/api/conversations/fork?request_id=request%2F1');
  await api.getConversationFork({ topicId: 'grp_9' });
  expect(fetchMock.mock.calls[2][0]).toBe('/api/conversations/fork?topic_id=grp_9');
  expect(fetchMock.mock.calls[2][1].method).toBe('GET');
});

it('cancels through an authenticated DELETE that identifies the request', async () => {
  const fetchMock = vi.fn().mockResolvedValue(new Response(JSON.stringify({
    request_id: 'request/1', source_topic_id: 'p2p_1_2', target_topic_id: 'grp_9', state: 'failed', error: 'session_fork_cancelled',
  }), { status: 200 }));
  vi.stubGlobal('fetch', fetchMock);
  setToken('fixture-token');
  const result = await api.cancelConversationFork({ requestId: 'request/1' });
  const [url, options] = fetchMock.mock.calls[0];
  expect(url).toBe('/api/conversations/fork?request_id=request%2F1');
  expect(options.method).toBe('DELETE');
  expect(options.headers.Authorization).toBe('Bearer fixture-token');
  expect(result).toMatchObject({ state: 'failed', error: 'session_fork_cancelled' });
});

it('preserves the explicit not-a-fork response needed to unlock an ordinary topic', async () => {
  vi.stubGlobal('fetch', vi.fn(async () => new Response(JSON.stringify({ error: 'session_fork_not_found' }), { status: 404 })));
  await expect(api.getConversationFork({ topicId: 'grp_9' })).rejects.toMatchObject({ status: 404, message: 'session_fork_not_found' });
});
