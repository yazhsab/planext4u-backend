import {existsSync, mkdirSync, mkdtempSync, lstatSync, realpathSync, writeFileSync, readFileSync, openSync, closeSync} from 'node:fs';
import {resolve, relative, isAbsolute, sep} from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';
import {createHash, randomUUID} from 'node:crypto';
import {repositoryRoot} from './check.mjs';

export const stages = ['quality','postgres','dependencies','scan'];
export function selectStages(args) {
  if (!args.length) return [...stages];
  if (args.length!==1 || !args[0].startsWith('--only=')) throw new Error('Usage: node scripts/completion/verify.mjs [--only=quality,postgres,dependencies,scan]');
  const selected = args[0].slice(7).split(',');
  if (new Set(selected).size!==selected.length || selected.some(value=>!stages.includes(value))) throw new Error('Unknown or duplicate verification stage.');
  return stages.filter(stage=>selected.includes(stage));
}

export function requireLocalEngine(endpoint) {
  if (typeof endpoint!=='string' || !/^(?:unix:\/\/\/[^\r\n]+|npipe:\/\/\/\/\.\/pipe\/[^\r\n]+)$/.test(endpoint)) {
    throw new Error('Verification requires a local Docker Unix socket or Windows named pipe; remote contexts are not allowed.');
  }
}

export function verificationDefinition(root, output, project) {
  if (!/^planext4u-verify-[a-f0-9]{12}$/.test(project)) throw new Error('Unexpected verification project name.');
  const cacheVolumes = ['go-mod:/go/pkg/mod','go-build:/root/.cache/go-build'];
  const verifier = {image:`${project}:source`, build:{context:root,dockerfile:'deploy/local/verification.Dockerfile',target:'verification'},
    cpus:4, init:true, volumes:[{type:'bind',source:output,target:'/evidence'},...cacheVolumes],
    labels:{'net.planext4u.purpose':'phase0-verification'},
    depends_on:Object.fromEntries(['postgres','redis','nats'].map(name=>[name,{condition:'service_healthy'}]))};
  return {services:{
    postgres:{image:'postgres:17.10-alpine3.22',environment:{POSTGRES_DB:'planext4u_phase0_verification',POSTGRES_USER:'planext4u_verify',POSTGRES_PASSWORD:'synthetic-verification-only'},
      tmpfs:['/var/lib/postgresql/data'],healthcheck:{test:['CMD','pg_isready','-U','planext4u_verify','-d','planext4u_phase0_verification'],interval:'2s',timeout:'3s',retries:30}},
    'migration-postgres':{image:'postgres:17.10-alpine3.22',environment:{POSTGRES_DB:'planext4u_migration_verification',POSTGRES_USER:'planext4u_verify',POSTGRES_PASSWORD:'synthetic-verification-only'},
      tmpfs:['/var/lib/postgresql/data'],healthcheck:{test:['CMD','pg_isready','-U','planext4u_verify','-d','planext4u_migration_verification'],interval:'2s',timeout:'3s',retries:30}},
    redis:{image:'redis:8.10.0-alpine3.23',command:['redis-server','--save','','--appendonly','no'],healthcheck:{test:['CMD','redis-cli','ping'],interval:'2s',timeout:'3s',retries:30}},
    nats:{image:'nats:2.14.5-alpine3.22',command:['--jetstream','--store_dir','/data','--http_port','8222'],tmpfs:['/data'],
      healthcheck:{test:['CMD','wget','--spider','--quiet','http://127.0.0.1:8222/healthz'],interval:'2s',timeout:'3s',retries:30}},
    'object-store':{image:'planext4u-verification-object-store:local',build:{context:root,dockerfile:'deploy/local/minio.Dockerfile'},
      command:['server','/data'],tmpfs:['/data:uid=10001,gid=10001,mode=0700'],
      environment:{MINIO_ROOT_USER:'synthetic-verification',MINIO_ROOT_PASSWORD:'synthetic-verification-object-only'},
      healthcheck:{test:['CMD','curl','--fail','--silent','http://127.0.0.1:9000/minio/health/live'],interval:'2s',timeout:'3s',retries:30}},
    verifier,
    'dependency-verifier':{...verifier,profiles:['docker-tests'],volumes:[...verifier.volumes,{type:'bind',source:'/var/run/docker.sock',target:'/var/run/docker.sock'}],
      environment:{TESTCONTAINERS_HOST_OVERRIDE:'host.docker.internal',DOCKER_AUTH_CONFIG:'{"auths":{}}'},extra_hosts:['host.docker.internal:host-gateway']},
  },volumes:{'go-mod':{name:'planext4u-phase0-verification-modules'},'go-build':{name:'planext4u-phase0-verification-build'}}};
}

function outputDirectory(root) {
  const realRoot=realpathSync(root);
  let parent=root;
  for(const component of ['.local','completion','verification']) {
    parent=resolve(parent,component);
    if(!existsSync(parent)) mkdirSync(parent,{mode:0o700});
    const rel=relative(realRoot,realpathSync(parent));
    if(!lstatSync(parent).isDirectory() || lstatSync(parent).isSymbolicLink() || rel==='..' || rel.startsWith(`..${sep}`) || isAbsolute(rel)) throw new Error('Verification output must remain inside the repository.');
  }
  return mkdtempSync(resolve(parent,'run-'));
}

export function stageResult(name, result, logfile) {
  return {name,result:result.status===0?'PASSED':'FAILED',exit_code:result.status,error:result.error?.code,log:logfile};
}

export function runVerification(args=process.argv.slice(2)) {
  const selected=selectStages(args);
  const raw=arguments_=>spawnSync('docker',arguments_,{cwd:repositoryRoot,encoding:'utf8',windowsHide:true,timeout:30000});
  if(process.env.DOCKER_HOST) requireLocalEngine(process.env.DOCKER_HOST);
  const context=raw(['context','inspect','--format','{{(index .Endpoints "docker").Host}}']);
  if(context.status!==0) throw new Error('A running local Docker engine and Compose are required.');
  requireLocalEngine(context.stdout.trim());
  const engine=raw(['info','--format','{{.OSType}}']);
  if(engine.status!==0 || engine.stdout.trim()!=='linux') throw new Error('Verification requires a running local Linux Docker engine.');
  const output=outputDirectory(repositoryRoot);
  const project=`planext4u-verify-${randomUUID().replaceAll('-','').slice(0,12)}`;
  const definition=verificationDefinition(repositoryRoot,output,project);
  const composeFile=resolve(output,'compose.json');
  writeFileSync(composeFile,JSON.stringify(definition,null,2)+'\n',{mode:0o600});
  const compose=['compose','--project-name',project,'-f',composeFile];
  const revision=spawnSync('git',['rev-parse','HEAD'],{cwd:repositoryRoot,encoding:'utf8',windowsHide:true});
  const report={check_id:'backend-verification-runner',started_at:new Date().toISOString(),source_revision:revision.status===0?revision.stdout.trim():null,
    project,selected_stages:selected,suite_complete:false,result:'RUNNING',steps:[],
    note:'Isolated source-image verification; no application runtime database, local profiles or host credentials. This report alone does not close Phase 0.'};
  const save=()=>writeFileSync(resolve(output,'report.json'),JSON.stringify(report,null,2)+'\n');
  const step=(name,arguments_)=>{
    console.log(name);
    const logfile=`${name}.log`;
    const descriptor=openSync(resolve(output,logfile),'w',0o600);
    let result;
    try {result=spawnSync('docker',[...compose,...arguments_],{cwd:repositoryRoot,stdio:['ignore',descriptor,descriptor],windowsHide:true,timeout:1500000});}
    finally {closeSync(descriptor);}
    const record=stageResult(name,result,logfile);
    record.log_sha256=createHash('sha256').update(readFileSync(resolve(output,logfile))).digest('hex');
    report.steps.push(record);save();
    if(record.result!=='PASSED') throw new Error(`${name} failed; see ${resolve(output,logfile)}`);
  };
  save();
  let failure;
  try {
    step('configuration',['config','--quiet']);
    step('build-source-image',['build','verifier']);
    const image=raw(['image','inspect','--format','{{.Id}}',`${project}:source`]);
    if(image.status!==0) throw new Error('Could not record the verification source image.');
    report.source_image=image.stdout.trim();
    step('source-manifest',['run','--rm','--no-deps','verifier','manifest']);
    report.source_manifest_sha256=createHash('sha256').update(readFileSync(resolve(output,'source.sha256'))).digest('hex');
    if(selected.includes('postgres')) {
      step('build-object-store',['build','object-store']);
      step('test-dependencies',['up','--detach','--wait','--wait-timeout','120','postgres','migration-postgres','redis','nats','object-store']);
    }
    // Sequential execution keeps compilation outside the timing-sensitive quality gate.
    for(const stage of selected) step(stage,['run','--rm','--no-deps',stage==='dependencies'?'dependency-verifier':'verifier',stage]);
  } catch(error) {failure=error.message;}
  finally {
    try {step('cleanup',['down','--remove-orphans','--timeout','15']);} catch(error) {failure??=error.message;}
    report.result=failure?'FAILED':'PASSED';
    report.suite_complete=!failure && selected.length===stages.length;
    report.finished_at=new Date().toISOString();
    if(failure) report.failure=failure;
    save();
    console.log(JSON.stringify({result:report.result,suite_complete:report.suite_complete,evidence:resolve(output,'report.json'),...(failure?{failure}:{})},null,2));
  }
  return failure?2:0;
}

if(process.argv[1] && resolve(process.argv[1])===fileURLToPath(import.meta.url)) {
  try {process.exitCode=runVerification();} catch(error) {console.error(error.message);process.exitCode=2;}
}
