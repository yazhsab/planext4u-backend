import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {selectStages, requireLocalEngine, verificationDefinition, stageResult} from './verify.mjs';

test('verification selection is explicit, rejects unknown stages and keeps quality before compilation-heavy dependency tests',()=>{
  assert.deepEqual(selectStages([]),['quality','postgres','dependencies','scan']);
  assert.deepEqual(selectStages(['--only=dependencies,quality']),['quality','dependencies']);
  for(const args of [['--only='],['--only=production'],['--only=scan,scan'],['--only=scan','--extra']]) assert.throws(()=>selectStages(args));
});

test('a remote Docker endpoint cannot receive source or execute destructive integration fixtures',()=>{
  for(const endpoint of ['tcp://127.0.0.1:2375','tcp://production.example:2376','ssh://test@example','',null,'unix:///var/run/docker.sock\nmalformed']) assert.throws(()=>requireLocalEngine(endpoint));
  requireLocalEngine('unix:///var/run/docker.sock');
  requireLocalEngine('npipe:////./pipe/dockerDesktopLinuxEngine');
});

test('integration databases are private, ephemeral and cannot be confused with the running application database',()=>{
  const config=verificationDefinition('/repo','/repo/.local/completion/verification/run-test','planext4u-verify-012345abcdef');
  assert.equal(config.services.postgres.environment.POSTGRES_DB,'planext4u_phase0_verification');
  assert.notEqual(config.services.postgres.environment.POSTGRES_DB,'planext4u_local');
  assert.deepEqual(config.services.postgres.tmpfs,['/var/lib/postgresql/data']);
  assert.equal(config.services['migration-postgres'].environment.POSTGRES_DB,'planext4u_migration_verification');
  assert.notEqual(config.services['migration-postgres'],config.services.postgres);
  assert.deepEqual(config.services['migration-postgres'].tmpfs,['/var/lib/postgresql/data']);
  for(const service of Object.values(config.services)) assert.equal(service.ports,undefined);
  const mounts=config.services.verifier.volumes;
  assert.equal(mounts.some(mount=>typeof mount==='object' && mount.target==='/src'),false);
  assert.equal(mounts.some(mount=>typeof mount==='object' && mount.target==='/var/run/docker.sock'),false);
  assert.equal(config.services['dependency-verifier'].volumes.some(mount=>typeof mount==='object' && mount.target==='/var/run/docker.sock'),true);
  assert.throws(()=>verificationDefinition('/repo','/output','planext4u-phase0'));
});

test('timeouts, missing executables and failed tests never become successful verification records',()=>{
  for(const result of [{status:null,error:{code:'ETIMEDOUT'}},{status:null,error:{code:'ENOENT'}},{status:1}]) assert.equal(stageResult('quality',result,'quality.log').result,'FAILED');
  assert.equal(stageResult('quality',{status:0},'quality.log').result,'PASSED');
});

test('integration commands cannot silently select the persistent application services',()=>{
  const makefile=readFileSync(new URL('../../Makefile',import.meta.url),'utf8');
  assert.doesNotMatch(makefile,/\$\{[A-Z_]+_TEST_URL:-/);
  const runner=readFileSync(new URL('./verify.mjs',import.meta.url),'utf8');
  assert.match(runner,/'120','postgres','migration-postgres','redis','nats','object-store'/);
  const driver=readFileSync(new URL('./verify-linux.sh',import.meta.url),'utf8');
  assert.match(driver,/migration-postgres:5432\/planext4u_migration_verification/);
  assert.match(driver,/--- PASS: TestBEMigrate001PostgresFromZeroUpgradeIsolationAndRollback/);
});
