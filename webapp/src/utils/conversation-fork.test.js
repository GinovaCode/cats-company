import { canForkConversation, forkTargetTopic, normalizeForkRecord } from './conversation-fork';

const agent = { uid: 2, is_owner: true };

describe('conversation fork eligibility', () => {
  it('allows only the current user and an owned agent in p2p', () => {
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 'usr1', agents: [agent] })).toBe(true);
    expect(canForkConversation({ topic: 'p2p_3_2', userId: 1, agents: [agent] })).toBe(false);
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 1, agents: [{ uid: 2 }] })).toBe(false);
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 1, agents: [] })).toBe(false);
  });

  it('requires an owned agent_task with exactly the user and one agent', () => {
    const source = {
      topic: 'grp_8', userId: 1, agents: [agent],
      group: { kind: 'agent_task' },
      members: [{ user_id: 'usr1', role: 'owner' }, { user_id: 2, is_bot: true }],
    };
    expect(canForkConversation(source)).toBe(true);
    expect(canForkConversation({ ...source, group: { kind: 'group' } })).toBe(false);
    expect(canForkConversation({ ...source, members: [...source.members, { user_id: 3 }] })).toBe(false);
    expect(canForkConversation({ ...source, members: [source.members[0]] })).toBe(false);
    expect(canForkConversation({ ...source, members: [{ user_id: 1, role: 'member' }, source.members[1]] })).toBe(false);
  });
});

describe('fork wire records', () => {
  const record = { request_id: 'r1', source_topic_id: 'p2p_1_2', target_topic_id: 'grp_9', state: 'ready' };
  it('resolves an ordinary target topic from IDs without manufacturing messages', () => {
    expect(forkTargetTopic(record, [{ id: 'grp_9', name: 'Copied task', avatar_url: '/a.png' }])).toEqual({
      topicId: 'grp_9', groupId: 9, name: 'Copied task', avatar_url: '/a.png',
      isGroup: true, isAgentTask: true, hasBot: true, memberCount: 2,
    });
    expect(forkTargetTopic(record).topicId).toBe('grp_9');
    expect(() => forkTargetTopic({ ...record, state: 'pending' })).toThrow();
  });
  it('rejects malformed, mismatched and unknown states instead of treating them as ready', () => {
    expect(normalizeForkRecord(record, { requestId: 'r1' })).toEqual(record);
    expect(() => normalizeForkRecord(record, { requestId: 'r2' })).toThrow();
    expect(() => normalizeForkRecord(record, { targetTopic: 'grp_10' })).toThrow();
    expect(() => normalizeForkRecord({ ...record, state: 'initializing' })).toThrow();
    expect(() => normalizeForkRecord({ ...record, target_topic_id: 'p2p_1_2' })).toThrow();
  });
});
