import { describe, expect, it } from 'vitest';
import { normalizePublicationReport, publicationReportStorageKey, readPublicationReportHint, writePublicationReportHint } from './skillhub-publication-report';

const entryRequestId = `pubentry_${'a'.repeat(32)}`;
const skill = { localSkillId: 'local-1', publication_source: { workspace_generation: 'c'.repeat(64) } };

describe('publication report display contract', () => {
  it('preserves structured locations, all 50 issues, confirmation policy and truncation', () => {
    const issue = { code: 'metadata.description_missing', category: 'format', blocking: true,
      location: { entryIndex: 0, relativePath: 'SKILL.md', field: 'description', secret: 'discard' },
      message: '缺少 description', repairPolicy: 'confirmation_required', credential: 'discard' };
    const result = normalizePublicationReport({ schemaVersion: 1, entryRequestId, validation: {
      schemaVersion: '1', outcome: 'rejected', ruleVersion: 'skillhub-publish-v1.0.1',
      issues: Array.from({ length: 50 }, () => issue), truncated: true, private: 'discard',
    } }, entryRequestId);
    expect(result.issues).toHaveLength(50);
    expect(result.issues[0]).toEqual({ code: issue.code, category: 'format', blocking: true,
      location: { entryIndex: 0, relativePath: 'SKILL.md', field: 'description' },
      message: '缺少 description', repairPolicy: 'confirmation_required' });
    expect(result.truncated).toBe(true);
    expect(JSON.stringify(result)).not.toContain('discard');
  });

  it('refuses an unrelated or malformed report instead of creating a fake rejection', () => {
    const report = { schemaVersion: 1, entryRequestId, validation: { schemaVersion: '1', outcome: 'rejected', issues: [] } };
    expect(() => normalizePublicationReport(report, 'another-entry')).toThrow();
    expect(() => normalizePublicationReport({ ...report, schemaVersion: 2 }, entryRequestId)).toThrow();
    expect(() => normalizePublicationReport({ ...report, validation: { ...report.validation, issues: null } }, entryRequestId)).toThrow();
  });

  it('stores only a request reference scoped to account, device, Bot, Skill and source generation', () => {
    const key = publicationReportStorageKey(7, 'device', 42, skill);
    writePublicationReportHint(key, entryRequestId);
    expect(JSON.parse(sessionStorage.getItem(key))).toEqual({ entryRequestId });
    expect(readPublicationReportHint(key)).toEqual({ entryRequestId });
    for (const different of [
      publicationReportStorageKey(8, 'device', 42, skill),
      publicationReportStorageKey(7, 'other-device', 42, skill),
      publicationReportStorageKey(7, 'device', 43, skill),
      publicationReportStorageKey(7, 'device', 42, { ...skill, localSkillId: 'other' }),
      publicationReportStorageKey(7, 'device', 42, { ...skill, publication_source: { workspace_generation: 'd'.repeat(64) } }),
    ]) expect(readPublicationReportHint(different)).toBeNull();
    writePublicationReportHint(key, null);
    expect(readPublicationReportHint(key)).toBeNull();
  });
});
