import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

const root = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..');
const cloud = path.resolve(process.env.SKILLHUB_REPO || path.join(root, '..', 'skillhub'));
const pin = '4d661ac7ae07fb5c923f186be548f14ba0673c11';
const revision = spawnSync('git', ['-C', cloud, 'rev-parse', 'HEAD'], { encoding: 'utf8' });
if (revision.status !== 0 || revision.stdout.trim() !== pin) throw Error(`Cloud checkout must equal ${pin}`);
for (const file of ['apps/api/publication-description-derivation.js', 'packages/publication-description-derivations/read.js',
  'tests/helpers/description-copy.js', 'tests/helpers/publication-peer-tls.js', 'node_modules/yaml/package.json']) {
  if (!fs.existsSync(path.join(cloud, file))) throw Error(`Missing mandatory Cloud dependency: ${file}`);
}
const result = spawnSync('go', ['test', './server', '-run', '^TestPublicationDescriptionValidationPairedCloud$', '-count=1', '-json'], {
  cwd: root, shell: false, encoding: 'utf8', timeout: 240000, maxBuffer: 8 * 1024 * 1024,
  env: { ...process.env, SKILLHUB_DESCRIPTION_PAIRED_ROOT: cloud, SKILLHUB_DESCRIPTION_PAIRED_NODE: process.execPath }
});
process.stdout.write(result.stdout || ''); process.stderr.write(result.stderr || '');
if (result.error || result.status !== 0 || result.signal) throw Error(`Cloud report contract failed: ${result.error?.message || result.signal || result.status}`);
const events = result.stdout.split(/\r?\n/).filter(Boolean).map(line => JSON.parse(line));
const suite = 'TestPublicationDescriptionValidationPairedCloud';
const expected = [suite, ...['normal', 'cancel', 'change-copy', 'disable'].map(name => `${suite}/${name}`)];
const passed = new Set(events.filter(event => event.Action === 'pass').map(event => event.Test));
const ran = events.filter(event => event.Action === 'run' && event.Test);
if (events.some(event => event.Action === 'skip' || event.Action === 'fail') || expected.some(name => !passed.has(name)) ||
  ran.length !== expected.length || ran.some(event => !passed.has(event.Test))) throw Error('The paired suite and all four cases must pass without skips');
console.log('[description-validation-paired] Real Cloud HTTPS handler/validator/worker and CatsCo client/coordinator; synthetic stores/authority/Runtime, not deployed E2E.');
