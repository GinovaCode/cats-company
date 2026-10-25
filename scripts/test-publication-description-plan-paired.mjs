import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const cloud = path.resolve(process.env.SKILLHUB_REPO || path.join(root, '..', 'skillhub'));
const pin = '18e99516806a04002dff067c6e7372ba4a97935f';
const revision = spawnSync('git', ['-C', cloud, 'rev-parse', 'HEAD'], { encoding: 'utf8' });
if (revision.status !== 0 || revision.stdout.trim() !== pin) throw Error(`Cloud checkout must equal ${pin}`);
if (!process.env.SKILLHUB_TEST_DATABASE_URL) throw Error('Real Cloud PostgreSQL is mandatory');
for (const file of ['apps/api/publication-description-derivation.js', 'packages/publication-description-derivations/release-plan.js',
  'infra/migrations/021_publication_description_release_plans.sql', 'tests/helpers/description-copy.js', 'node_modules/yaml/package.json']) {
  if (!fs.existsSync(path.join(cloud, file))) throw Error(`Missing mandatory Cloud dependency: ${file}`);
}
const result = spawnSync('go', ['test', './server', '-run', '^TestPublicationDescriptionReleasePlanPairedCloud$', '-count=1', '-json'], {
  cwd: root, shell: false, encoding: 'utf8', timeout: 240000, maxBuffer: 8 * 1024 * 1024,
  env: { ...process.env, SKILLHUB_DESCRIPTION_PLAN_ROOT: cloud, SKILLHUB_DESCRIPTION_PLAN_NODE: process.execPath }
});
process.stdout.write(result.stdout || ''); process.stderr.write(result.stderr || '');
if (result.error || result.status !== 0 || result.signal) throw Error(`Cloud plan contract failed: ${result.error?.message || result.signal || result.status}`);
const events = result.stdout.split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
const suite = 'TestPublicationDescriptionReleasePlanPairedCloud';
const expected = [suite, ...['normal', 'lose-response', 'cancel', 'publisher', 'disable'].map(name => `${suite}/${name}`)];
const passed = new Set(events.filter(event => event.Action === 'pass').map(event => event.Test));
const ran = events.filter(event => event.Action === 'run' && event.Test);
if (events.some(event => event.Action === 'skip' || event.Action === 'fail') || expected.some(name => !passed.has(name)) ||
  ran.length !== expected.length || ran.some(event => !passed.has(event.Test))) throw Error('All five paired cases must execute without skips');
console.log('[description-plan-paired] Real Cloud PostgreSQL/intake/worker/planner/HTTPS and CatsCo client/coordinator; synthetic CatsCo authority/acceptance/Runtime, no permit/model/deployed E2E.');
