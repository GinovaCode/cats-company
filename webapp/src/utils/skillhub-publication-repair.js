import { api, getAuthRevision, getToken, isCurrentAuthSession } from '../api';
import { createAgentTaskTopicRecord } from './agent-task-topic';
import { normalizePublicationReport } from './skillhub-publication-report';

const entryPattern = /^pubentry_[a-f0-9]{32}$/;
const uidPattern = /^[1-9][0-9]*$/;

// References are browser recovery hints, never permission to read or edit files.
export function publicationRepairReference(value) {
  if (value?.schemaVersion !== 1 || !entryPattern.test(value.entryRequestId || '')
    || typeof value.botUid !== 'string' || !uidPattern.test(value.botUid)) return null;
  return { schemaVersion: 1, entryRequestId: value.entryRequestId, botUid: value.botUid };
}

export function normalizePublicationRepairContext(response, entryRequestId, botUid) {
  const reference = publicationRepairReference(response);
  const constraints = response?.constraints;
  if (!reference || reference.entryRequestId !== entryRequestId || reference.botUid !== String(botUid)
    || response.entrypoint !== 'workspace' || response.contextKind !== 'publication_rejection'
    || constraints?.maxFormatRepairRounds !== 3 || constraints.descriptionDraftRequiresConfirmation !== true
    || constraints.stopOnFunctionalIdentitySecurityChanges !== true || constraints.automaticRepairEnabled !== false) {
    throw new Error('发布修复上下文不匹配，请回到运行工作区重新读取报告。');
  }
  const report = normalizePublicationReport(response, entryRequestId);
  if (report.outcome !== 'rejected') throw new Error('该发布记录当前不需要修复。');
  return { reference, report };
}

function storageKey(userUid, topicId) {
  return uidPattern.test(String(userUid)) && /^grp_[1-9][0-9]*$/.test(topicId || '')
    ? `catsco.skillhub.publication.repair.${userUid}.${topicId}` : '';
}

export function readPublicationRepairHint(userUid, topicId) {
  const key = storageKey(userUid, topicId);
  if (!key) return null;
  try { return publicationRepairReference(JSON.parse(window.sessionStorage.getItem(key) || 'null')); }
  catch { return null; }
}

export async function createPublicationRepairTopic({ agent, entryRequestId, userUid, isCurrent = () => true }) {
  const botUid = String(agent?.uid || agent?.id || '');
  if (!uidPattern.test(botUid) || !uidPattern.test(String(userUid)) || !entryPattern.test(entryRequestId || '')) {
    throw new Error('发布修复记录无效，请重新读取报告。');
  }
  const token = getToken();
  const revision = getAuthRevision();
  const current = () => Boolean(token) && isCurrentAuthSession(token, revision) && isCurrent();
  if (!current()) return null;
  const loadContext = async () => normalizePublicationRepairContext(
    await api.getSkillHubPublicationRepairContext(entryRequestId), entryRequestId, botUid,
  );
  await loadContext();
  if (!current()) return null;
  let created;
  let hintKey = '';
  let hintStored = false;
  try {
    created = await createAgentTaskTopicRecord({ agent, taskName: 'Skill 发布修复' });
    if (!current()) return null;
    // Creating a topic is a separate request. Recheck ownership and the rejected
    // record before attaching anything to the resulting conversation.
    const { reference } = await loadContext();
    if (!current()) return null;
    hintKey = storageKey(userUid, created.topicId);
    if (!hintKey) throw new Error('无法关联修复对话，请重试。');
    try {
      window.sessionStorage.setItem(hintKey, JSON.stringify(reference));
      if (!readPublicationRepairHint(userUid, created.topicId)) throw new Error('storage unavailable');
      hintStored = true;
      if (!current()) return null;
    } catch {
      throw new Error('浏览器无法保存修复报告引用，请允许此页面使用会话存储后重试。');
    }
    return created;
  } catch (error) {
    if (current()) throw error;
    return null;
  } finally {
    // Never make a cleanup request under a replacement account or session.
    if (created && (!hintStored || !current())
      && isCurrentAuthSession(token, revision)) {
      if (hintKey) {
        try { window.sessionStorage.removeItem(hintKey); } catch { /* Recovery hint is disposable. */ }
      }
      try { await api.disbandGroup(created.groupId); } catch { /* Empty task remains visible for manual removal. */ }
    }
  }
}
