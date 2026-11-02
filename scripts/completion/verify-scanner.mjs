// Real engine/S3 verification. Only UUID-named test buckets are created and
// removed by the integration test; the application bucket is never reset.
import {mkdtempSync, mkdirSync, readFileSync, writeFileSync, openSync, closeSync} from 'node:fs';
import {resolve} from 'node:path';
import {spawnSync} from 'node:child_process';
import {createHash} from 'node:crypto';
import {repositoryRoot} from './check.mjs';
import {requireLocalEngine} from './verify.mjs';
import {loadLocalProfile} from './runtime.mjs';

const command=(args,options={})=>spawnSync('docker',args,{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:30000,...options});
function manifest() {
  const result=spawnSync('git',['ls-files','--cached','--others','--exclude-standard','-z'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true});
  if(result.status!==0) throw new Error('Source inventory is unavailable.');
  const files=[...new Set(result.stdout.split('\0').filter(path=>/^(?:cmd\/|internal\/|go\.(?:mod|sum)$|deploy\/local\/(?:clamav\.compose\.yaml|clamd\.conf|freshclam\.conf)$|scripts\/completion\/verify-scanner\.mjs$)/.test(path)))].sort();
  return files.map(path=>`${createHash('sha256').update(readFileSync(resolve(repositoryRoot,path))).digest('hex')}  ${path}`).join('\n')+'\n';
}
let output,report;
try {
  if(process.env.DOCKER_HOST) requireLocalEngine(process.env.DOCKER_HOST);
  const context=command(['context','inspect','--format','{{(index .Endpoints "docker").Host}}']);
  if(context.status!==0) throw new Error('Local Docker context is unavailable.');
  requireLocalEngine(context.stdout.trim());
  mkdirSync(resolve(repositoryRoot,'.local/completion/scanner-verification'),{recursive:true,mode:0o700});
  output=mkdtempSync(resolve(repositoryRoot,'.local/completion/scanner-verification/run-'));
  const before=manifest();
  writeFileSync(resolve(output,'source.sha256'),before);
  report={check_id:'real-private-media-scanner',started_at:new Date().toISOString(),result:'RUNNING',source_manifest_sha256:createHash('sha256').update(before).digest('hex'),
    scanner_image:'clamav/clamav:1.4.6@sha256:3bba6dd5ac6663d55b80f86a3126e146612d93b593cc2617188d13f637d91260',
    note:'Actual private MinIO bytes and ClamAV engine; synthetic test documents only. No KYC/bank/provider approval or production failover is claimed.'};
  const save=()=>writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');
  save();
  const ready=command(['compose','-f','deploy/local/clamav.compose.yaml','up','--detach','--wait','--wait-timeout','240'],{timeout:300000});
  if(ready.status!==0) throw new Error('Private scanner is not ready.');
  const profile=loadLocalProfile('media');
  const environment=resolve(output,'test.env');
  writeFileSync(environment,`CLAMAV_TEST_SOCKET=/run/clamav/clamd.sock\nMEDIA_S3_TEST_ENDPOINT=http://object-store:9000\nMEDIA_S3_TEST_ACCESS_KEY=${readFileSync(profile.S3_ACCESS_KEY_ID_FILE,'utf8').trim()}\nMEDIA_S3_TEST_SECRET_KEY=${readFileSync(profile.S3_SECRET_ACCESS_KEY_FILE,'utf8').trim()}\n`,{mode:0o600});
  const descriptor=openSync(resolve(output,'scanner.log'),'w',0o600);
  let result;
  try {
    result=command(['run','--rm','--cpus','2','--network','planext4u-phase0_default',
      '--mount',`type=bind,source=${repositoryRoot.replaceAll('\\','/')},target=/src,readonly`,
      '--mount','type=volume,source=planext4u-phase0-verification-modules,target=/go/pkg/mod',
      '--mount','type=volume,source=planext4u-phase0-verification-build,target=/root/.cache/go-build',
      '--mount','type=volume,source=planext4u-phase2-clamav-socket,target=/run/clamav,readonly',
      '--env-file',environment,'-w','/src','golang:1.26.10-alpine3.23','go','test','-tags=integration','-count=1','-run','TestClamAVPrivateS3UploadQuarantineAndUnavailableRecovery','-v','./internal/media'],
      {stdio:['ignore',descriptor,descriptor],timeout:300000});
  } finally {closeSync(descriptor);}
  const log=readFileSync(resolve(output,'scanner.log'),'utf8');
  report.log_sha256=createHash('sha256').update(log).digest('hex');
  report.engine_database=log.match(/real scanner version\/database: (.+)/)?.[1]?.trim();
  report.exit_code=result.status;
  report.source_unchanged=manifest()===before;
  report.loaded_signature_freshness=log.includes('signature freshness: actual loaded engine rejected with aged consumer clock; private evidence remained pending and recovered');
  if(result.status!==0 || !report.source_unchanged || !report.engine_database || !report.loaded_signature_freshness || !log.includes('--- PASS: TestClamAVPrivateS3UploadQuarantineAndUnavailableRecovery')) throw new Error('Actual scanner verification failed; inspect the private local log.');
  report.result='PASSED';report.finished_at=new Date().toISOString();save();
  console.log(JSON.stringify({result:report.result,engine_database:report.engine_database,evidence:resolve(output,'report.json')},null,2));
} catch(error) {
  if(report) {report.result='FAILED';report.failure=error.message;report.finished_at=new Date().toISOString();writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');}
  console.error(error.message);process.exitCode=2;
}
