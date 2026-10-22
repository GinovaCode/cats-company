import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { api, isCurrentAuthSession } from '../api';
import PublicationRepairPanel from './skillhub-publication-repair-panel';

vi.mock('../api', () => ({
  getToken: vi.fn(() => 'session'), getAuthRevision: vi.fn(() => 1),
  isCurrentAuthSession: vi.fn(() => true),
  api: { getGroupInfo: vi.fn(), getSkillHubPublicationRepairContext: vi.fn() },
}));
const entryRequestId = `pubentry_${'a'.repeat(32)}`;
const reference = { schemaVersion: 1, entryRequestId, botUid: '42' };
const context = () => ({ ...reference, entrypoint: 'workspace', contextKind: 'publication_rejection',
  constraints: { maxFormatRepairRounds: 3, descriptionDraftRequiresConfirmation: true,
    stopOnFunctionalIdentitySecurityChanges: true, automaticRepairEnabled: false },
  validation: { schemaVersion: '1', outcome: 'rejected', issues: [{ code: 'metadata.description_missing',
    location: { relativePath: 'SKILL.md' }, message: '<script>untrusted report</script>', repairPolicy: 'confirmation_required' }] },
});
let root;
let container;
const render = async (props = {}) => act(async () => {
  root.render(<PublicationRepairPanel userUid={7} topic='grp_77' isGroup groupId={77} {...props} />);
});
beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.clearAllMocks();
  isCurrentAuthSession.mockReturnValue(true);
  sessionStorage.clear();
  sessionStorage.setItem('catsco.skillhub.publication.repair.7.grp_77', JSON.stringify(reference));
  api.getGroupInfo.mockResolvedValue({ group: { id: 77, kind: 'agent_task', owner_id: 7 }, members: [
    { user_id: 7 }, { user_id: 42, is_bot: true },
  ] });
  api.getSkillHubPublicationRepairContext.mockImplementation(async () => context());
  container = document.createElement('div');
  document.body.append(container);
  root = createRoot(container);
});
afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
  vi.restoreAllMocks();
});

it('restores the reference, revalidates membership and safely displays the report without sending messages', async () => {
  await render();
  expect(container.textContent).toContain('尚未启动自动修复');
  expect(api.getGroupInfo).toHaveBeenCalledWith(77);
  expect(api.getSkillHubPublicationRepairContext).toHaveBeenCalledWith(entryRequestId);
  const button = container.querySelector('button');
  await act(async () => Simulate.click(button));
  expect(api.getGroupInfo).toHaveBeenCalledTimes(2);
  expect(api.getSkillHubPublicationRepairContext).toHaveBeenCalledTimes(2);
  const dialog = document.querySelector('[role="dialog"]');
  expect(dialog.textContent).toContain('<script>untrusted report</script>');
  expect(dialog.querySelector('script')).toBeNull();
  expect(dialog.textContent).toContain('需确认后修改');
  expect(dialog.textContent).not.toContain('新建修复对话');
  await act(async () => Simulate.click(dialog.querySelector('[aria-label="关闭发布报告"]')));
  expect(document.activeElement).toBe(button);
});

it.each([
  { group: { id: 77, kind: 'standard', owner_id: 7 }, members: [{ user_id: 42, is_bot: true }] },
  { group: { id: 78, kind: 'agent_task', owner_id: 7 }, members: [{ user_id: 42, is_bot: true }] },
  { group: { id: 77, kind: 'agent_task', owner_id: 7 }, members: [{ user_id: 43, is_bot: true }] },
  { group: { id: 77, kind: 'agent_task', owner_id: 7 }, members: [{ user_id: 42, is_bot: true }, { user_id: 43, is_bot: true }] },
  { group: { id: 77, kind: 'agent_task', owner_id: 7 }, members: [] },
  { group: { id: 77, kind: 'agent_task', owner_id: 8 }, members: [{ user_id: 7 }, { user_id: 42, is_bot: true }] },
  { group: { id: 77, kind: 'agent_task' }, members: [{ user_id: 42, is_bot: true }] },
  { group: { id: 77, kind: 'agent_task', owner_id: 7 }, members: [{ user_id: 42, account_type: 'bot' }] },
])('refuses a forged or mismatched conversation hint: %j', async (response) => {
  api.getGroupInfo.mockResolvedValue(response);
  await render();
  expect(container.textContent).toContain('无法读取此修复报告');
  expect(api.getSkillHubPublicationRepairContext).not.toHaveBeenCalled();
});

it('does not query for another account, ordinary conversation or absent hint', async () => {
  await render({ userUid: 8 });
  await render({ isGroup: false });
  await render({ topic: 'grp_78', groupId: 78 });
  expect(api.getGroupInfo).not.toHaveBeenCalled();
  expect(container.textContent).toBe('');
});

it('hides old reports immediately on auth invalidation and ignores late responses', async () => {
  let finish;
  api.getSkillHubPublicationRepairContext.mockReturnValueOnce(new Promise(resolve => { finish = resolve; }));
  await render();
  await act(async () => {
    isCurrentAuthSession.mockReturnValue(false);
    window.dispatchEvent(new Event('cc:auth-changed'));
    finish(context());
  });
  expect(container.textContent).toBe('');
  expect(document.querySelector('[role="dialog"]')).toBeNull();
});

it('does not show a late report after navigating to another topic', async () => {
  let finish;
  api.getSkillHubPublicationRepairContext.mockReturnValueOnce(new Promise(resolve => { finish = resolve; }));
  await render();
  await render({ topic: 'grp_78', groupId: 78 });
  await act(async () => finish(context()));
  expect(container.textContent).toBe('');
});

it('removes stale content on re-read and shows only a safe, retryable failure', async () => {
  await render();
  api.getSkillHubPublicationRepairContext.mockRejectedValueOnce(new Error('upstream secret'));
  await act(async () => Simulate.click(container.querySelector('button')));
  const dialog = document.querySelector('[role="dialog"]');
  expect(dialog.textContent).toContain('无法读取此修复报告');
  expect(dialog.textContent).not.toContain('upstream secret');
  expect(dialog.textContent).not.toContain('untrusted report');
  await act(async () => Simulate.click([...dialog.querySelectorAll('button')].find(button => button.textContent === '重新读取')));
  expect(dialog.textContent).toContain('untrusted report');
});
