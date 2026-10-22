// Display-only projection of the human report API. Never turn these fields
// into file paths, repair instructions, or publication authority.
export function normalizePublicationReport(response, entryRequestId) {
  const value = response?.validation;
  if (response?.schemaVersion !== 1 || response?.entryRequestId !== entryRequestId
    || value?.schemaVersion !== '1' || !['passed', 'rejected'].includes(value?.outcome)
    || !Array.isArray(value.issues) || value.issues.length > 50) {
    throw new Error('发布报告格式或请求标识不匹配，请重试读取。');
  }
  const text = (value, max) => typeof value === 'string' ? value.slice(0, max) : '';
  return {
    entryRequestId,
    outcome: value.outcome,
    ruleVersion: text(value.ruleVersion, 128),
    truncated: value.truncated === true,
    issues: value.issues.map(issue => ({
      code: text(issue?.code, 128),
      message: text(issue?.message, 1024),
      category: text(issue?.category, 64),
      blocking: issue?.blocking === true,
      repairPolicy: text(issue?.repairPolicy, 64),
      location: {
        relativePath: text(issue?.location?.relativePath, 128),
        field: text(issue?.location?.field, 64),
        entryIndex: Number.isSafeInteger(issue?.location?.entryIndex) && issue.location.entryIndex >= 0
          ? issue.location.entryIndex : null,
      },
    })),
  };
}

export function publicationReportStorageKey(userUID, deviceID, botUID, skill) {
  const generation = skill?.publication_source?.workspace_generation;
  if (!userUID || !deviceID || !botUID || !skill?.localSkillId || !/^[a-f0-9]{64}$/.test(generation || '')) return '';
  return `catsco.skillhub.publication.report.${[userUID, deviceID, botUID, skill.localSkillId, generation]
    .map(value => encodeURIComponent(String(value))).join('.')}`;
}

export function readPublicationReportHint(key) {
  try {
    const value = JSON.parse(window.sessionStorage.getItem(key) || 'null');
    if (!/^pubentry_[a-f0-9]{32}$/.test(value?.entryRequestId || '')) return null;
    return { entryRequestId: value.entryRequestId };
  } catch { return null; }
}

export function writePublicationReportHint(key, entryRequestId) {
  if (!key) return;
  try {
    if (entryRequestId) window.sessionStorage.setItem(key, JSON.stringify({ entryRequestId }));
    else window.sessionStorage.removeItem(key);
  } catch { /* Storage is a recovery hint, never authority. */ }
}
