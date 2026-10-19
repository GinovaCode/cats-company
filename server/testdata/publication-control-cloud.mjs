// Isolated two-repository test peer. No production routes or test auth bypass.
import https from 'node:https';
import path from 'node:path';
import { pathToFileURL } from 'node:url';
import { createInterface } from 'node:readline';
import { createHash } from 'node:crypto';
const root = process.env.SKILLHUB_CONTROL_CONTRACT_ROOT;
if (!root || !process.env.SKILLHUB_DATABASE_URL) throw new Error('contract dependencies required');
const load = p => import(pathToFileURL(path.resolve(root, p)));
const { publicationPeerTLS } = await load('tests/helpers/publication-peer-tls.js');
const { loadConfig } = await load('packages/config/index.js');
const { createStore, closePools } = await load('packages/database/store.js');
const { validatePublishRequest } = await load('packages/publish-validation/index.js');
const { createDelegatedPublicationIntake } = await load('packages/publication-delegation/intake.js');
const { createPublicationControlHandler } = await load('apps/api/publication-control.js');
const tls = await publicationPeerTLS(), issuer = 'https://catsco.cancel-contract.example';
const config = loadConfig({nodeEnv:'test',publicationTasksEnabled:true,publicationPublishingEnabled:true});
const store = createStore(config);
let handle, lose = true, reference, count = 0;
const server = https.createServer(tls, async (req,res) => {
  count++;
  const end = res.end.bind(res);
  res.end = (...args) => { if(lose && res.statusCode === 200) {lose=false;res.destroy();return res;}return end(...args); };
  if (!await handle(req,res)) {res.writeHead(404);res.end();}
});
await new Promise(resolve=>server.listen(0,'127.0.0.1',resolve));
const audience=`https://127.0.0.1:${server.address().port}`;
handle=createPublicationControlHandler({publicationControl:{enabled:true,issuer,audience,peerSecret:'a'.repeat(64),trustedProxyAddresses:[]}},store);
const raw=Buffer.from(JSON.stringify({source:{type:'files',files:[{path:'SKILL.md',content:'---\nname: cancellation-contract\ndescription: Isolated contract fixture\n---\nSynthetic content'}]}}));
const sourceHash=validatePublishRequest(raw.toString()).body.validation.candidateHash;
const requestHash=createHash('sha256').update(raw).digest('hex');
const write=v=>process.stdout.write(JSON.stringify(v)+'\n');
write({issuer,audience,ca:tls.cert.toString(),sourceHash,requestHash,requestBytes:raw.length});
const lines=createInterface({input:process.stdin});
try {
 for await(const line of lines){
  const command=JSON.parse(line);
  if(command.action==='accept'){
   const g=command.grant;
   await store.claimCatsCoPublisherIdentity({catsCoUid:g.actor,catsCoIssuer:issuer,namespace:`cancel-contract-${g.entryRequestId.slice(-12)}`,displayName:'Contract fixture',contact:'',internalEmail:`${g.entryRequestId}@example.invalid`,passwordHash:'fixture'});
   const intake=createDelegatedPublicationIntake({enabled:true,config,store,issuer,audience,introspectionClient:{async checkUploadGrant(){return {
    issuer,audience,action:'candidate.upload',actorCatsCoUid:g.actor,botUid:g.bot,entryRequestId:g.entryRequestId,candidateRevision:1,authorityEpoch:1,
    requestHash,requestBytes:raw.length,sourceSnapshotHash:sourceHash,idempotencyKey:g.idempotencyKey,delegationId:g.delegationId,
    observedAt:new Date().toISOString(),expiresAt:new Date(Date.now()+90000).toISOString()
   };}}});
   const r=await intake({rawBody:raw,sourceSnapshotHash:sourceHash,entryRequestId:g.entryRequestId,token:'pubupload_'+'b'.repeat(64),idempotencyKey:g.idempotencyKey,authorityEpoch:1});
   reference={schemaVersion:1,entryRequestId:g.entryRequestId,candidateRevision:1,acceptanceEpoch:1,requestHash,sourceSnapshotHash:sourceHash,requestBytes:raw.length,delegationId:g.delegationId,idempotencyKey:g.idempotencyKey,taskId:r.association.taskId,eventId:r.association.eventId};write(reference);
  }else if(command.action==='state'){
   const row=(await store.query('SELECT state,candidate IS NULL AS cleared,delegated_cancelled_at FROM publication_tasks WHERE id=$1',[reference.taskId])).rows[0];write({...row,count});
  }else throw new Error('unknown command');
 }
} finally {server.closeAllConnections();await new Promise(resolve=>server.close(resolve));await closePools();await tls.close();}
