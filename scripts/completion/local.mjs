import {spawnSync} from 'node:child_process';
import {mkdirSync, writeFileSync} from 'node:fs';
import {resolve, delimiter} from 'node:path';
import {repositoryRoot} from './check.mjs';

const compose = ['compose', '--project-name', 'planext4u-phase0', '-f', 'deploy/local/compose.yaml'];
const outputDirectory = resolve(repositoryRoot, '.local/completion');
function run(executable, args, options = {}) {
  return spawnSync(executable, args, {cwd: repositoryRoot, encoding: 'utf8', timeout: 30_000, windowsHide: true, ...options});
}
function probe(id, executable, args, validate = result => result.status === 0) {
  const result = run(executable, args);
  const unavailable = result.error?.code === 'ENOENT' || /is not recognized|not found/i.test(result.stderr ?? '');
  return {id, result: unavailable ? 'UNAVAILABLE' : validate(result) ? 'PASSED' : 'FAILED',
    detail: result.error?.code === 'ENOENT' ? `${executable} is not installed/on PATH` :
      result.error?.code === 'ETIMEDOUT' ? 'Timed out' : (result.stdout || result.stderr || '').trim().slice(0, 500)};
}
function flutter() {
  // The Windows Flutter launcher is a batch file. This command is static and
  // contains no user-provided shell text or environment-file values.
  return process.platform === 'win32'
    ? probe('flutter', 'cmd.exe', ['/d','/c','flutter --version --machine'])
    : probe('flutter', 'flutter', ['--version','--machine']);
}
function save(value, name) {
  mkdirSync(outputDirectory, {recursive: true});
  writeFileSync(resolve(outputDirectory, name), `${JSON.stringify(value, null, 2)}\n`, {mode: 0o600});
}
const command = process.argv[2] ?? 'preflight';
if (command === 'preflight') {
  const goExecutable = process.argv.find(value => value.startsWith('--go='))?.slice(5) || 'go';
  const flutterBin = process.argv.find(value => value.startsWith('--flutter-bin='))?.slice(14);
  if (flutterBin) process.env.PATH = `${resolve(flutterBin)}${delimiter}${process.env.PATH ?? ''}`;
  const checks = [
    probe('docker-linux-engine', 'docker', ['version','--format','{{.Server.Os}}'], result => result.status === 0 && result.stdout.trim() === 'linux'),
    probe('docker-compose', 'docker', ['compose','version']), flutter(),
    probe('go', goExecutable, ['version']),
    probe('git', 'git', ['--version']),
  ];
  const report = {checked_at: new Date().toISOString(), platform: process.platform, node: process.version,
    result: checks.every(check => check.result === 'PASSED') ? 'PASSED' : 'OPEN', checks,
    ios: process.platform === 'darwin' ? 'Run the pinned iOS build matrix with Xcode.' : 'macOS/Xcode runner required for iOS; Windows cannot verify that matrix.',
    note: 'Tool availability does not verify API startup, persistent business flows or Phase 0 acceptance.'};
  save(report, 'preflight.json');
  console.log(JSON.stringify(report, null, 2));
  process.exitCode = report.result === 'PASSED' ? 0 : 2;
} else if (command === 'up' || command === 'status' || command === 'stop') {
  const arguments_ = command === 'up' ? ['up','--detach','--build','--wait','--wait-timeout','120'] :
    command === 'stop' ? ['stop'] : ['ps','--format','json'];
  // A fixed, isolated project avoids touching another checkout's containers or
  // data. stop preserves volumes. There is deliberately no reset/delete command.
  const result = run('docker', [...compose, ...arguments_], {stdio: 'inherit', timeout: command === 'up' ? 900_000 : 30_000});
  if (result.error) console.error(result.error.code === 'ENOENT' ? 'A Linux Docker engine and Compose are required.' : result.error.message);
  process.exitCode = result.status ?? 2;
} else if (command === 'verify-dependencies') {
  const raw = run('docker', [...compose, 'ps','--all','--format','json']);
  let containers = [];
  try {
    const value = raw.stdout.trim();
    containers = value.startsWith('[') ? JSON.parse(value) : value.split('\n').filter(Boolean).map(line => JSON.parse(line));
  } catch { /* An empty/unavailable engine must produce OPEN below. */ }
  const required = ['postgres','redis','nats','object-store','synthetic-provider'];
  const checks = required.map(service => ({service, result: containers.some(item => item.Service === service && item.State === 'running' && item.Health === 'healthy') ? 'PASSED' : 'OPEN'}));
  const report = {checked_at: new Date().toISOString(), project: 'planext4u-phase0', result: raw.status === 0 && checks.every(check => check.result === 'PASSED') ? 'PASSED' : 'OPEN', checks,
    note: 'Dependency health only. Run migrations, persistent API/admin startup and role smoke before recording local-stack as passed.'};
  save(report, 'dependencies.json');
  console.log(JSON.stringify(report, null, 2));
  process.exitCode = report.result === 'PASSED' ? 0 : 2;
} else {
  console.error('Usage: node scripts/completion/local.mjs preflight [--go=/path/to/go] [--flutter-bin=/path/to/flutter/bin] |up|status|stop|verify-dependencies');
  process.exitCode = 64;
}
