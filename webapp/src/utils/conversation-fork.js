function uid(value) {
  const text = String(value ?? '').replace(/^usr/, '');
  return /^\d+$/.test(text) && Number(text) > 0 ? text.replace(/^0+/, '') : '';
}

export function canForkConversation({ topic, userId, agents = [], group, members = [] }) {
  const actor = uid(userId);
  if (!actor) return false;
  const ownedAgent = (id) => agents.some(agent => uid(agent.uid || agent.id) === id
    && (agent.is_owner === true || agent.relation === 'owner'));
  const direct = /^p2p_(\d+)_(\d+)$/.exec(topic || '');
  if (direct) {
    const participants = direct.slice(1).map(uid);
    return participants.includes(actor) && participants[0] !== participants[1]
      && ownedAgent(participants.find(id => id !== actor));
  }
  if (!/^grp_\d+$/.test(topic || '') || !(group?.kind === 'agent_task' || group?.is_agent_task)) return false;
  if (members.length !== 2) return false;
  const self = members.find(member => uid(member.user_id) === actor);
  const peer = members.find(member => uid(member.user_id) !== actor);
  return Boolean(self?.role === 'owner' && peer && ownedAgent(uid(peer.user_id)));
}

export function normalizeForkRecord(record, { requestId, sourceTopic, targetTopic } = {}) {
  if (!record || typeof record.request_id !== 'string' || !record.request_id
    || typeof record.source_topic_id !== 'string' || !record.source_topic_id
    || !/^grp_[1-9]\d*$/.test(record.target_topic_id || '')
    || !['pending', 'ready', 'failed'].includes(record.state)
    || (requestId && record.request_id !== requestId)
    || (sourceTopic && record.source_topic_id !== sourceTopic)
    || (targetTopic && record.target_topic_id !== targetTopic)) {
    throw new Error('会话复制状态无效，请重试查询。');
  }
  return record;
}

export function forkTargetTopic(record, conversations = []) {
  normalizeForkRecord(record);
  if (record.state !== 'ready') throw new Error('新会话尚未就绪');
  const summary = conversations.find(item => (item.id || item.topic_id) === record.target_topic_id);
  return {
    topicId: record.target_topic_id,
    groupId: Number(record.target_topic_id.slice(4)),
    name: summary?.name || '复制的会话',
    avatar_url: summary?.avatar_url || '',
    isGroup: true,
    isAgentTask: true,
    hasBot: true,
    memberCount: 2,
  };
}

export function describeForkError(error) {
  const text = String(error || '');
  const messages = {
    session_fork_source_interrupted: '原会话还有中断的任务，请先恢复或结束该任务，再重新创建副本。',
    session_fork_initialization_failed: '无法读取或复制本地会话上下文，请检查 Agent 的本地记录后重新创建。',
    session_fork_source_busy: '原会话正在执行，请等待完成后重试。',
    session_fork_agent_offline: 'Agent 当前离线，请连接后重试。',
    session_fork_forbidden: '只能复制自己的一人一 Agent 会话。',
    session_fork_request_conflict: '请求标识与原请求不一致，请重新查询原复制结果。',
    session_fork_source_unavailable: '原会话不可用或已变化，请检查原会话后重试。',
    session_fork_unavailable: '暂时无法复制会话，请稍后重试。',
    session_fork_not_found: '尚未找到此复制请求，可以使用原请求重试。',
  };
  return messages[text] || text;
}
