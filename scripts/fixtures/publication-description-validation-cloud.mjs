import https from 'node:https';
import path from 'node:path';
import readline from 'node:readline';
import { pathToFileURL } from 'node:url';

const root = process.argv[2];
const moduleAt = relative => import(pathToFileURL(path.join(root, relative)).href);
const { createPublicationDescriptionDerivationHandler } = await moduleAt('apps/api/publication-description-derivation.js');
const { loadConfig } = await moduleAt('packages/config/index.js');
const { descriptionCopyFixture } = await moduleAt('tests/helpers/description-copy.js');
const { publicationPeerTLS } = await moduleAt('tests/helpers/publication-peer-tls.js');
const tls = await publicationPeerTLS();
const fixture = descriptionCopyFixture(); // Real validator/derivation; synthetic retained store/authority.
let applicationId = null, handler;
const store = {
  async getPublicationDescriptionDerivation(id) { return id === applicationId ? { ...fixture.row, application_id: id } : null; },
  async getPublicationDescriptionDerivationSource(id) { return id === fixture.request.taskId ? fixture.source : null; }
};
const server = https.createServer(tls, (req, res) => void handler(req, res).then(handled => {
  if (!handled) { res.writeHead(404); res.end(); }
}));
await new Promise(resolve => server.listen(0, '127.0.0.1', resolve));
const origin = `https://127.0.0.1:${server.address().port}`;
const config = loadConfig({ publicationTasksEnabled: true, publicationDescriptionDerivationEnabled: true,
  publicationDescriptionValidationEnabled: true, publicationDescriptionDerivationIssuer: 'https://cats.example',
  publicationDescriptionDerivationAudience: origin, publicationDescriptionDerivationPeerSecret: 'pubderive_' + 'a'.repeat(64),
  publicationDescriptionDerivationTrustedProxyAddresses: [] });
handler = createPublicationDescriptionDerivationHandler(config, store);
console.log(JSON.stringify({ origin, certificate: tls.cert.toString(), request: fixture.request,
  originalBytes: fixture.source.candidate.length, originalValidation: fixture.original.validation }));
try {
  for await (const line of readline.createInterface({ input: process.stdin, crlfDelay: Infinity })) {
    const command = JSON.parse(line);
    if (command.action === 'bind' && /^pubdesc_[a-f0-9]{64}$/.test(command.applicationId) && applicationId === null) applicationId = command.applicationId;
    else if (command.action === 'cancel') fixture.source.state = 'cancelled';
    else if (command.action === 'change-copy') fixture.row.derived_raw_json = Buffer.alloc(fixture.request.derivedBytes);
    else if (command.action === 'disable') handler = createPublicationDescriptionDerivationHandler({ ...config,
      publicationDescriptionDerivation: { ...config.publicationDescriptionDerivation, validationEnabled: false } }, store);
    else throw Error('unexpected fixture control');
    console.log(JSON.stringify({ ok: true }));
  }
} finally {
  server.closeAllConnections();
  await new Promise(resolve => server.close(resolve));
  await tls.close();
}
