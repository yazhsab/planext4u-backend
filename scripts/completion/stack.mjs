import {readFileSync, writeFileSync, mkdirSync, lstatSync, existsSync} from 'node:fs';
import {resolve, relative, isAbsolute, sep} from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';
import {repositoryRoot} from './check.mjs';
import {prepareLocal, loadLocalProfile, runtimeDirectory, services} from './runtime.mjs';

export const stackDirectory = resolve(repositoryRoot, '.local/completion/stack');
export const ports = {identity: 8083, configuration: 8084, catalog: 8085, transaction: 8086, admin: 8090, gateway: 8082, media: 8087};
const dependencies = ['postgres','redis','nats','object-store','synthetic-provider'];
const image = 'planext4u-phase0-api:local';
const literal = value => String(value).replaceAll('$', () => '$$');

function localFile(path, directory) {
  const full = resolve(path);
  const rel = relative(directory, full);
  if (rel === '..' || rel.startsWith(`..${sep}`) || isAbsolute(rel) || !lstatSync(full).isFile() || lstatSync(full).isSymbolicLink()) {
    throw new Error('Container preparation only reads regular files in the isolated local runtime directory.');
  }
  return readFileSync(full, 'utf8');
}

export function createStackDefinition(directory = runtimeDirectory) {
  const containerEnvironment = service => {
    const profile = loadLocalProfile(service, directory);
    const result = {};
    for (const [key, value] of Object.entries(profile)) {
      if (key.endsWith('_FILE') && value) {
        let contents = localFile(value, directory);
        if (key === 'DATABASE_URL_FILE' || key === 'MIGRATION_DATABASE_URL_FILE') {
          const url = new URL(contents.trim());
          const expectedUser = service === 'migration' ? 'planext4u_local' : `planext4u_${service}_login`;
          if (url.username !== expectedUser || !url.password) throw new Error('Local containers require the expected restricted service login, or the dedicated migration login.');
          url.hostname = 'postgres';
          url.port = '5432';
          contents = url.toString();
        } else if (key === 'REDIS_URL_FILE') {
          if (contents.trim() !== 'redis://127.0.0.1:63790/0') throw new Error('Unexpected local Redis destination.');
          contents = 'redis://redis:6379/0';
        }
        result[`${key}_VALUE`] = literal(contents);
      } else {
        result[key] = literal(value);
      }
    }
    if (service !== 'migration') result.HTTP_ADDRESS = `0.0.0.0:${ports[service]}`;
    if (service === 'identity') result.PROVIDER_BASE_URL = 'http://synthetic-provider:8081';
    if (service === 'gateway') {
      result.UPSTREAM_URL = 'http://transaction:8086';
      result.UPSTREAM_ROUTES = JSON.stringify({'/v1/auth': 'http://identity:8083', '/v1/me': 'http://identity:8083',
        '/v1/startup': 'http://configuration:8084', '/v1/bootstrap': 'http://configuration:8084', '/v1/occupations': 'http://configuration:8084', '/v1/geography': 'http://configuration:8084', '/v1/pages': 'http://configuration:8084', '/v1/home': 'http://catalog:8085',
        '/v1/catalog': 'http://catalog:8085', '/v1/serviceability': 'http://catalog:8085', '/v1/media': 'http://media:8087'});
      result.ANONYMOUS_ROUTES = JSON.stringify({...JSON.parse(result.ANONYMOUS_ROUTES), '/v1/startup': ['GET']});
    }
    if (service === 'configuration') {
      result.PUBLIC_STARTUP_TENANT_ID = loadLocalProfile('identity', directory).TENANT_ID;
      result.PUBLIC_STARTUP_COUNTRIES = loadLocalProfile('identity', directory).ALLOWED_COUNTRIES;
    }
    if (service === 'admin') result.FULFILLMENT_BASE_URL = 'http://transaction:8086';
    if (service === 'transaction') {
      result.PRIVATE_MEDIA_BASE_URL = 'http://media:8087';
      // No inherited external provider activation in this synthetic local stack.
      for (const key of ['RAZORPAY_KEY_ID','RAZORPAY_API_SECRET_FILE','RAZORPAY_WEBHOOK_SECRET_FILE',
        'PAYSTACK_PUBLIC_KEY','PAYSTACK_API_SECRET_FILE','PAYSTACK_WEBHOOK_SECRET_FILE',
        'NOTIFICATION_BASE_URL','ORDER_NOTIFICATION_HMAC_KEY_FILE','BANK_VERIFICATION_BASE_URL','BANK_VERIFICATION_TOKEN_FILE','BANK_LINK_BASE_URL','BANK_LINK_TOKEN_FILE','BANK_LINK_LAUNCH_ORIGINS','KYC_VERIFICATION_BASE_URL','KYC_VERIFICATION_TOKEN_FILE','OCR_VERIFICATION_BASE_URL','OCR_VERIFICATION_TOKEN_FILE']) {
        delete result[`${key}_VALUE`];
        result[key] = '';
      }
      if (profile.LOCAL_SYNTHETIC_BANK_LINKS === 'true') {
        result.BANK_VERIFICATION_BASE_URL = 'http://synthetic-provider:8081';
        result.BANK_VERIFICATION_TOKEN_FILE = '/run/planext4u-secrets/bank-resolver-token';
        result.BANK_VERIFICATION_TOKEN_FILE_VALUE = 'synthetic-local-bank-provider-only-token';
        result.BANK_LINK_BASE_URL = 'http://synthetic-provider:8081';
        result.BANK_LINK_TOKEN_FILE = '/run/planext4u-secrets/bank-link-token';
        result.BANK_LINK_TOKEN_FILE_VALUE = 'synthetic-local-bank-provider-only-token';
        result.BANK_LINK_LAUNCH_ORIGINS = JSON.stringify(['http://127.0.0.1:18081']);
      }
      if (profile.LOCAL_SYNTHETIC_KYC === 'true') {
        result.KYC_VERIFICATION_BASE_URL = 'http://synthetic-provider:8081';
        result.KYC_VERIFICATION_TOKEN_FILE = '/run/planext4u-secrets/kyc-resolver-token';
        result.KYC_VERIFICATION_TOKEN_FILE_VALUE = 'synthetic-local-kyc-provider-only-token';
      }
      if (profile.LOCAL_SYNTHETIC_OCR === 'true') {
        result.OCR_VERIFICATION_BASE_URL = 'http://synthetic-provider:8081';
        result.OCR_VERIFICATION_TOKEN_FILE = '/run/planext4u-secrets/ocr-resolver-token';
        result.OCR_VERIFICATION_TOKEN_FILE_VALUE = 'synthetic-local-ocr-provider-only-token';
      }
    }
    if (service === 'media') {
      result.S3_ENDPOINT = 'http://object-store:9000';
      result.PLANEXT4U_SUPPLEMENTARY_GROUPS = '101';
    }
    return result;
  };
  const common = {
    image,
    build: {context: repositoryRoot, dockerfile: 'deploy/local/api.Dockerfile'},
    init: true,
    tmpfs: ['/run/planext4u-secrets:mode=0700'],
    labels: {'net.planext4u.environment': 'local-phase0', 'net.planext4u.synthetic': 'true'},
  };
  const definition = {services: {}};
  definition.volumes = {'clamav-socket': {external: true, name: 'planext4u-phase2-clamav-socket'}};
  // Ensure the psql login bootstrap and API profiles use exactly the same local
  // passwords, including when a developer deliberately changes a local profile.
  const passwords = {};
  for (const service of services.filter(value => value !== 'gateway')) {
    const profile = loadLocalProfile(service, directory);
    passwords[`PLANEXT4U_LOCAL_${service.toUpperCase()}_PASSWORD`] = literal(decodeURIComponent(new URL(localFile(profile.DATABASE_URL_FILE, directory).trim()).password));
    definition.services[service] = {...common,
      command: [`/app/${service}`], environment: containerEnvironment(service),
      ports: [`127.0.0.1:${ports[service]}:${ports[service]}`],
      depends_on: Object.fromEntries(dependencies.map(name => [name, {condition: 'service_healthy'}])),
      healthcheck: {test: ['CMD','curl','--fail','--silent','--max-time','2',`http://127.0.0.1:${ports[service]}/readyz`], interval: '3s', timeout: '3s', retries: 30, start_period: '15s'},
    };
  }
  definition.services.media.volumes = ['clamav-socket:/run/clamav:ro'];
  definition.services['media-init'] = {...common, profiles: ['phase0-tools'], command: ['/app/local-media-init'],
    environment: Object.fromEntries(Object.entries(containerEnvironment('media')).filter(([key]) => ['APP_ENV','AWS_REGION','MEDIA_BUCKET','S3_ENDPOINT','S3_ACCESS_KEY_ID_FILE_VALUE','S3_SECRET_ACCESS_KEY_FILE_VALUE'].includes(key)))};
  definition.services.gateway = {...common, command: ['/app/gateway'], environment: containerEnvironment('gateway'),
    ports: ['127.0.0.1:8082:8082'],
    depends_on: Object.fromEntries(['redis', ...services.filter(value => value !== 'gateway')].map(name => [name, {condition: 'service_healthy'}])),
    healthcheck: {test: ['CMD','curl','--fail','--silent','--max-time','2','http://127.0.0.1:8082/readyz'], interval: '3s', timeout: '3s', retries: 30, start_period: '15s'},
  };
  definition.services.postgres = {environment: passwords};
  definition.services.migration = {...common, profiles: ['phase0-tools'], command: ['/app/migrate','-root','/app/migrations','-service','all'], environment: containerEnvironment('migration')};
  return definition;
}

export function prepareStack(directory = stackDirectory) {
  prepareLocal();
  const definition = createStackDefinition();
  mkdirSync(directory, {recursive: true, mode: 0o700});
  const path = resolve(directory, 'compose.json');
  if (lstatSync(directory).isSymbolicLink()) throw new Error('Stack output directory must not be a symbolic link.');
  if (existsSync(path) && (!lstatSync(path).isFile() || lstatSync(path).isSymbolicLink())) throw new Error('Stack output must be a regular file.');
  writeFileSync(path, `${JSON.stringify(definition, null, 2)}\n`, {mode: 0o600, flag: 'w'});
  return path;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const command = process.argv[2] ?? 'prepare';
    if (!['prepare','up','verify','status','stop'].includes(command)) throw new Error('Usage: node scripts/completion/stack.mjs prepare|up|verify|status|stop');
    const path = prepareStack();
    const compose = ['compose','--project-name','planext4u-phase0','-f',resolve(repositoryRoot, 'deploy/local/compose.yaml'),'-f',path];
    const run = (args, options = {}) => spawnSync('docker', [...compose,...args], {cwd: repositoryRoot, encoding: 'utf8', timeout: 30_000, windowsHide: true, ...options});
    if (command === 'prepare') {
      console.log('Prepared ignored container configuration. Local secrets are materialized by the existing bootstrap; generated configuration is not an acceptance pass.');
    } else if (command === 'up') {
      const steps = [];
      const step = (name, args, options = {}) => {
        console.log(name);
        const result = run(args, {stdio: ['pipe','inherit','inherit'], timeout: 1_200_000, ...options});
        steps.push({name, exit_code: result.status, result: result.status === 0 ? 'PASSED' : 'OPEN', error: result.error?.code});
        writeFileSync(resolve(stackDirectory, 'setup.json'), `${JSON.stringify({checked_at: new Date().toISOString(), steps}, null, 2)}\n`);
        if (result.status !== 0) throw new Error(result.error?.code === 'ENOENT' ? 'Docker/Compose and a running Linux engine are required. Windows first-time WSL setup needs an administrator.' : `${name} did not complete. Existing data is retained.`);
      };
      step('Validate composed local configuration', ['config','--quiet']);
      step('Build and start persistent dependencies', ['up','--detach','--build','--wait','--wait-timeout','180',...dependencies]);
      step('Build the local API/migration image', ['build','migration']);
      step('Apply all migrations', ['run','--rm','--no-deps','migration']);
      step('Create restricted service logins', ['exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],
        {input: readFileSync(resolve(repositoryRoot, 'deploy/local/create-service-logins.sql'), 'utf8')});
      step('Initialize absent synthetic local readiness policy', ['exec','-T','postgres','psql','-v','ON_ERROR_STOP=1','-U','planext4u_local','-d','planext4u_local'],
        {input: readFileSync(resolve(repositoryRoot, 'deploy/local/phase0-reference.sql'), 'utf8')});
      console.log('Start the isolated private scanner and signature updater');
      const scanner = spawnSync('docker', ['compose','-f',resolve(repositoryRoot, 'deploy/local/clamav.compose.yaml'),'up','--detach','--wait','--wait-timeout','240'], {cwd: repositoryRoot, stdio:'inherit', windowsHide:true, timeout:300_000});
      if (scanner.status !== 0) throw new Error('Private scanner is not ready. Existing data is retained.');
      step('Initialize the absent private local media bucket', ['run','--rm','--no-deps','media-init']);
      step('Start persistent APIs and administrator BFF', ['up','--detach','--no-build','--wait','--wait-timeout','180',...services]);
      console.log('Runtime startup completed. Run verify, then real role/tenant and administrator session smoke; no financial fixtures were reset.');
    } else if (command === 'verify') {
      const raw = run(['ps','--all','--format','json']);
      let containers = [];
      try {
        const value = raw.stdout.trim();
        containers = value.startsWith('[') ? JSON.parse(value) : value.split('\n').filter(Boolean).map(line => JSON.parse(line));
      } catch { /* No engine/invalid output cannot pass. */ }
      const checks = [...dependencies,...services].map(service => ({service,
        result: raw.status === 0 && containers.some(item => item.Service === service && item.State === 'running' && item.Health === 'healthy') ? 'PASSED' : 'OPEN'}));
      const http = await Promise.all(services.map(async service => {
        try {
          const response = await fetch(`http://127.0.0.1:${ports[service]}/readyz`, {signal: AbortSignal.timeout(5000)});
          return {service, result: response.status === 200 ? 'PASSED' : 'OPEN', status: response.status};
        } catch { return {service, result: 'OPEN'}; }
      }));
      const report = {check_id: 'local-runtime-health', checked_at: new Date().toISOString(), result: [...checks,...http].every(value => value.result === 'PASSED') ? 'PASSED' : 'OPEN', containers: checks, http,
        note: 'Runtime health only. Configuration publication, real role/tenant isolation and administrator MFA/session smoke remain separate acceptance checks.'};
      writeFileSync(resolve(stackDirectory, 'health.json'), `${JSON.stringify(report, null, 2)}\n`);
      console.log(JSON.stringify(report, null, 2));
      process.exitCode = report.result === 'PASSED' ? 0 : 2;
    } else {
      const result = run(command === 'stop' ? ['stop'] : ['ps'], {stdio: 'inherit'});
      if (result.error) throw new Error('Docker/Compose is unavailable.');
      process.exitCode = result.status ?? 2;
    }
  } catch (error) { console.error(error.message); process.exitCode = 2; }
}
