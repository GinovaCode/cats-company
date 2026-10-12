import React, { act } from 'react';
import { createRoot } from 'react-dom/client';
import { Simulate } from 'react-dom/test-utils';
import { SkillVisibilityControl } from './skillhub-content';
import { marketplaceApi } from '../skillhub-marketplace-api';

vi.mock('../skillhub-marketplace-api', () => ({
  marketplaceApi: {
    skillVisibility: vi.fn(),
    updateSkillVisibility: vi.fn(),
  shareTargetFriends: vi.fn(),
  searchShareTargets: vi.fn(),
  },
}));

let root;
let container;

beforeEach(() => {
  globalThis.IS_REACT_ACT_ENVIRONMENT = true;
  vi.resetAllMocks();
  container = document.createElement('div');
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(async () => {
  await act(async () => root.unmount());
  container.remove();
});

async function render(ui) {
  await act(async () => root.render(ui));
  await act(async () => Promise.resolve());
}

it('loads owner visibility and saves shared CatsCo UIDs with the revision', async () => {
  marketplaceApi.skillVisibility.mockResolvedValue({
    visibility: { visibilityScope: 'public', sharedUserUids: [], visibilityRevision: 3 },
  });
  marketplaceApi.updateSkillVisibility.mockResolvedValue({
    visibility: { visibilityScope: 'shared', sharedUserUids: ['951', '42001'], visibilityRevision: 4 },
  });
  marketplaceApi.shareTargetFriends.mockResolvedValue({
    users: [
      { uid: '951', username: 'john', displayName: 'John' },
      { uid: '42001', username: 'friend-two', displayName: 'Friend Two' },
    ],
  });
  await render(<SkillVisibilityControl skillId='author/read' enabled writesEnabled />);
  expect(container.textContent).toContain('公开');
  await act(async () => Simulate.change(container.querySelector('select'), { target: { value: 'shared' } }));
  await act(async () => new Promise((resolve) => setTimeout(resolve, 0)));
  const targetCheckboxes = container.querySelectorAll('.cc-skillhub-share-target-option input');
  await act(async () => Simulate.change(targetCheckboxes[0], { target: { checked: true } }));
  await act(async () => Simulate.change(targetCheckboxes[1], { target: { checked: true } }));
  const save = [...container.querySelectorAll('button')].find((button) => button.textContent.includes('保存可见范围'));
  await act(async () => save.click());
  expect(marketplaceApi.updateSkillVisibility).toHaveBeenCalledWith({
    skillId: 'author/read',
    visibilityScope: 'shared',
    sharedUserUids: ['951', '42001'],
    expectedRevision: 3,
  });
  expect(container.textContent).toContain('指定共享');
  expect(container.textContent).toContain('可见范围已保存');
});

it('searches CatsCo usernames and submits selected target UIDs', async () => {
  marketplaceApi.skillVisibility.mockResolvedValue({
    visibility: { visibilityScope: 'shared', sharedUserUids: [], visibilityRevision: 8 },
  });
  marketplaceApi.shareTargetFriends.mockResolvedValue({ users: [] });
  marketplaceApi.searchShareTargets.mockResolvedValue({
    users: [{ uid: '42001', username: 'friend-two', displayName: 'Friend Two' }],
  });
  marketplaceApi.updateSkillVisibility.mockResolvedValue({
    visibility: { visibilityScope: 'shared', sharedUserUids: ['42001'], visibilityRevision: 9 },
  });
  await render(<SkillVisibilityControl skillId='author/read' enabled writesEnabled />);
  const search = container.querySelector('.cc-skillhub-share-target-search input');
  await act(async () => Simulate.change(search, { target: { value: 'friend-two' } }));
  await act(async () => container.querySelector('.cc-skillhub-share-target-search').dispatchEvent(new Event('submit', { bubbles: true, cancelable: true })));
  await act(async () => new Promise((resolve) => setTimeout(resolve, 0)));
  await act(async () => Simulate.change(container.querySelector('.cc-skillhub-share-target-option input'), { target: { checked: true } }));
  const save = [...container.querySelectorAll('button')].find((button) => button.textContent.includes('保存可见范围'));
  await act(async () => save.click());
  expect(marketplaceApi.searchShareTargets).toHaveBeenCalledWith('friend-two', expect.anything());
  expect(marketplaceApi.updateSkillVisibility).toHaveBeenCalledWith({
    skillId: 'author/read',
    visibilityScope: 'shared',
    sharedUserUids: ['42001'],
    expectedRevision: 8,
  });
});

it('does not disclose visibility controls for another publisher', async () => {
  marketplaceApi.skillVisibility.mockRejectedValue({ status: 403 });
  await render(<SkillVisibilityControl skillId='other/read' enabled writesEnabled />);
  expect(container.querySelector('.cc-skillhub-visibility')).toBeNull();
});

it('keeps the read-only state when visibility writes are not enabled', async () => {
  marketplaceApi.skillVisibility.mockResolvedValue({
    visibility: { visibilityScope: 'private', sharedUserUids: [], visibilityRevision: 1 },
  });
  await render(<SkillVisibilityControl skillId='author/read' enabled={true} writesEnabled={false} />);
  expect(container.textContent).toContain('当前环境只开放查看');
  expect(container.querySelector('select')).toBeNull();
});
