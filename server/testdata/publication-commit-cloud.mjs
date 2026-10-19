// Cross-repo fixture: uses the actual Cloud client, no copied validation.
// Each line constructs a fresh client to exercise stateless restart recovery.
import https from 'node:https';
import { createInterface } from 'node:readline';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';
const root=process.env.SKILLHUB_COMMIT_CONTRACT_ROOT;
if(!root)throw new Error('Cloud checkout required');
const {createPublicationCommitPermitClient,PublicationCommitPermitError}=
  await import(pathToFileURL(resolve(root,'packages/publication-delegation/commit-permit-client.js')).href);
for await(const line of createInterface({input:process.stdin,crlfDelay:Infinity})) {
  const {config,ca,input}=JSON.parse(line);
  const client=createPublicationCommitPermitClient(config,{
    requestImpl:(url,options,callback)=>https.request(url,{...options,ca},callback)});
  try {
    const permit=await client.authorize(input);
    process.stdout.write(JSON.stringify({permit})+'\n');
  } catch(error) {
    if(!(error instanceof PublicationCommitPermitError))throw error;
    process.stdout.write(JSON.stringify({error:error.code.replace('skillhub.publication_commit_permit_','')})+'\n');
  }
}

