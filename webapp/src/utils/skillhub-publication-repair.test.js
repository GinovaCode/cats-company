import { api, getAuthRevision, isCurrentAuthSession } from '../api';
import { createPublicationRepairTopic, normalizePublicationRepairContext, publicationRepairReference, readPublicationRepairHint } from './skillhub-publication-repair';

vi.mock('../api', () => ({
  getToken: vi.fn(() => 'owner-session'),
  getAuthRevision: vi.fn(() => 1),
  isCurrentAuthSession: vi.fn((_token, revision) => revision === 1),
  api: { getSkillHubPublicationRepairContext: vi.fn(), createGroup: vi.fn(), disbandGroup: vi.fn() },
}));

const entryRequestId = `pubentry_${'a'.repeat(32)}`;
const reference = { schemaVersion: 1, entryRequestId, botUid: '42' };
const key = 'catsco.skillhub.publication.repair.7.grp_77';
const context = () => ({ ...reference, entrypoint: 'workspace', contextKind: 'publication_rejection',
  constraints: { maxFormatRepairRounds: 3, descriptionDraftRequiresConfirmation: true,
    stopOnFunctionalIdentitySecurityChanges: true, automaticRepairEnabled: false },
  validation: { schemaVersion: '1', outcome: 'rejected', issues: [{ code: 'metadata.description_missing',
    location: { relativePath: 'SKILL.md', field: 'description' }, repairPolicy: 'confirmation_required' }] },
});
const create = (extra = {}) => createPublicationRepairTopic({ agent: { uid: 42 }, entryRequestId, userUid: 7, ...extra });

beforeEach(() => {
  vi.clearAllMocks();
  sessionStorage.clear();
  getAuthRevision.mockReturnValue(1);
  isCurrentAuthSession.mockImplementation((_token, revision) => revision === 1);
  api.getSkillHubPublicationRepairContext.mockImplementation(async () => context());
  api.createGroup.mockResolvedValue({ group_id: 77, topic: 'grp_77' });
  api.disbandGroup.mockResolvedValue({ ok: true });
});
afterEach(() => vi.restoreAllMocks());

describe('publication repair handoff', () => {
  it('rechecks the report around same-Bot task creation and persists only a reference', async () => {
    api.getSkillHubPublicationRepairContext.mockResolvedValue({ ...context(), secret: 'discard' });
    const topic = await create();
    expect(topic).toMatchObject({ topicId: 'grp_77', groupId: 77 });
    expect(api.createGroup).toHaveBeenCalledWith('Skill 发布修复', [42], { kind: 'agent_task' });
    expect(api.getSkillHubPublicationRepairContext).toHaveBeenCalledTimes(2);
    expect(api.getSkillHubPublicationRepairContext.mock.invocationCallOrder[0])
      .toBeLessThan(api.createGroup.mock.invocationCallOrder[0]);
    expect(api.createGroup.mock.invocationCallOrder[0])
      .toBeLessThan(api.getSkillHubPublicationRepairContext.mock.invocationCallOrder[1]);
    expect(JSON.parse(sessionStorage.getItem(key))).toEqual(reference);
    expect(readPublicationRepairHint(8, 'grp_77')).toBeNull();
    expect(api.disbandGroup).not.toHaveBeenCalled();
  });

  it.each([
    { schemaVersion: 2 }, { botUid: '43' }, { entryRequestId: `pubentry_${'b'.repeat(32)}` },
    { entrypoint: 'chat' }, { contextKind: 'other' }, { constraints: {} },
    { validation: { schemaVersion: '1', outcome: 'passed', issues: [] } },
  ])('rejects mismatched or unsafe context before creating a task: %j', async (changes) => {
    api.getSkillHubPublicationRepairContext.mockResolvedValue({ ...context(), ...changes });
    await expect(create()).rejects.toThrow();
    expect(api.createGroup).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(key)).toBeNull();
  });

  it('does not create after the page scope changes during the first read', async () => {
    let current = true;
    api.getSkillHubPublicationRepairContext.mockImplementationOnce(async () => { current = false; return context(); });
    expect(await create({ isCurrent: () => current })).toBeNull();
    expect(api.createGroup).not.toHaveBeenCalled();
  });

  it('cleans up an empty task if the scope changes during creation', async () => {
    let current = true;
    api.createGroup.mockImplementationOnce(async () => { current = false; return { group_id: 77, topic: 'grp_77' }; });
    expect(await create({ isCurrent: () => current })).toBeNull();
    expect(api.disbandGroup).toHaveBeenCalledWith(77);
    expect(sessionStorage.getItem(key)).toBeNull();
  });

  it('does not clean up under a replacement login', async () => {
    api.createGroup.mockImplementationOnce(async () => {
      isCurrentAuthSession.mockReturnValue(false);
      return { group_id: 77, topic: 'grp_77' };
    });
    expect(await create()).toBeNull();
    expect(api.disbandGroup).not.toHaveBeenCalled();
    expect(sessionStorage.getItem(key)).toBeNull();
  });

  it('cleans up if ownership is revoked after task creation, without hiding the failure', async () => {
    api.getSkillHubPublicationRepairContext.mockResolvedValueOnce(context()).mockRejectedValueOnce(new Error('revoked'));
    await expect(create()).rejects.toThrow('revoked');
    expect(api.disbandGroup).toHaveBeenCalledWith(77);
    expect(sessionStorage.getItem(key)).toBeNull();
  });

  it('cleans up if storage is unavailable and preserves a safe error when cleanup fails', async () => {
    vi.spyOn(window.sessionStorage, 'setItem').mockImplementation(() => { throw new Error('private storage detail'); });
    api.disbandGroup.mockRejectedValueOnce(new Error('cleanup failed'));
    await expect(create()).rejects.toThrow('浏览器无法保存修复报告引用');
    expect(api.disbandGroup).toHaveBeenCalledWith(77);
  });

  it('does not create a task without a current authenticated session', async () => {
    getAuthRevision.mockReturnValue(2);
    expect(await create()).toBeNull();
    expect(api.getSkillHubPublicationRepairContext).not.toHaveBeenCalled();
    expect(api.createGroup).not.toHaveBeenCalled();
  });

  it('treats stored hints as untrusted and strips all report and instruction fields', () => {
    expect(publicationRepairReference({ ...reference, validation: context().validation, prompt: 'discard' })).toEqual(reference);
    sessionStorage.setItem(key, 'not json');
    expect(readPublicationRepairHint(7, 'grp_77')).toBeNull();
    expect(publicationRepairReference({ ...reference, botUid: 'usr42' })).toBeNull();
    expect(publicationRepairReference({ ...reference, entryRequestId: '../42' })).toBeNull();
    expect(() => normalizePublicationRepairContext(context(), entryRequestId, 43)).toThrow();
  });
});
