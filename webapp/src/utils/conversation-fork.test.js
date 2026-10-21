import { canForkConversation, describeForkError, forkTargetTopic, normalizeForkRecord } from './conversation-fork';

const agent = { uid: 2, is_owner: true };

describe('conversation fork eligibility', () => {
  it('allows the current user and an owned or friend agent in p2p', () => {
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 'usr1', agents: [agent] })).toBe(true);
    expect(canForkConversation({ topic: 'p2p_3_2', userId: 1, agents: [agent] })).toBe(false);
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 1, agents: [{ uid: 2 }] })).toBe(false);
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 1, agents: [] })).toBe(false);
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 1, agents: [{ uid: 2, relation: 'friend' }] })).toBe(true);
    expect(canForkConversation({ topic: 'p2p_1_2', userId: 1, agents: [{ uid: 2, relation: 'friend', deployment_status: 'running', visibility: 'cloud' }] })).toBe(true);
  });

  it('requires exactly the current user and one accessible agent, regardless of group kind or ownership', () => {
    const source = {
      topic: 'grp_8', userId: 1, agents: [agent],
      group: { kind: 'agent_task' },
      members: [{ user_id: 'usr1', role: 'owner' }, { user_id: 2, is_bot: true }],
    };
    expect(canForkConversation(source)).toBe(true);
    expect(canForkConversation({ ...source, group: { kind: 'group' } })).toBe(true);
    expect(canForkConversation({ ...source, members: [...source.members, { user_id: 3 }] })).toBe(false);
    expect(canForkConversation({ ...source, members: [source.members[0]] })).toBe(false);
    expect(canForkConversation({ ...source, members: [{ user_id: 1, role: 'member' }, source.members[1]] })).toBe(true);
    expect(canForkConversation({ ...source, agents: [{ uid: 2, relation: 'friend' }] })).toBe(true);
    expect(canForkConversation({ ...source, members: [{ user_id: 3 }, source.members[1]] })).toBe(false);
    expect(canForkConversation({ ...source, agents: [{ uid: 2, relation: 'friend' }, { uid: 3, relation: 'friend' }], members: [{ user_id: 2 }, { user_id: 3 }] })).toBe(false);
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

describe('fork error copy', () => {
  it('turns an unanswered runtime into device advice instead of a raw code', () => {
    const text = describeForkError('session_fork_ack_timeout');
    expect(text).not.toBe('session_fork_ack_timeout');
    expect(text).toContain('设备');
    expect(text).toContain('升级');
    expect(text).toContain('重新创建');
  });
  it('states that a user cancellation leaves the source conversation untouched', () => {
    const text = describeForkError('session_fork_cancelled');
    expect(text).not.toBe('session_fork_cancelled');
    expect(text).toContain('已取消');
  });
  it('returns an unmapped code unchanged so diagnostics stay visible', () => {
    expect(describeForkError('session_fork_source_busy')).toBe('原会话正在执行，请等待完成后重试。');
    expect(describeForkError('unexpected_failure')).toBe('unexpected_failure');
    expect(describeForkError(undefined)).toBe('');
  });
});
