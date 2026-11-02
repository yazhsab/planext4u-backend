import {generateKeyPairSync, randomBytes} from 'node:crypto';
import {existsSync, mkdirSync, readFileSync, writeFileSync, lstatSync} from 'node:fs';
import {resolve} from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawn, spawnSync} from 'node:child_process';
import {repositoryRoot} from './check.mjs';

export const runtimeDirectory = resolve(repositoryRoot, '.local/completion/runtime');
export const services = ['identity','configuration','catalog','transaction','admin','gateway','media'];
const tenantID = 'afc1e0db-73cf-40b3-9927-33590133da0b';

export function prepareLocal(directory = runtimeDirectory) {
  mkdirSync(directory, {recursive: true, mode: 0o700});
  if (lstatSync(directory).isSymbolicLink()) throw new Error('Local runtime directory must not be a symbolic link.');
  const create = (name, contents) => {
    const path = resolve(directory, name);
    if (!existsSync(path)) writeFileSync(path, contents, {mode: 0o600, flag: 'wx'});
    if (!lstatSync(path).isFile() || lstatSync(path).isSymbolicLink()) throw new Error('Local runtime files must be regular files.');
    return path;
  };
  const secret = name => {
    // Runtime encryption keys are exactly 32 bytes after trimming. These local
    // synthetic keys use 32 printable random characters so trimming is stable.
    const path = create(name, randomBytes(24).toString('base64'));
    if (readFileSync(path).length !== 32) throw new Error('Existing local key has an invalid length; restore it before resuming.');
    return path;
  };
  const privatePath = resolve(directory, 'jwt-private.pem');
  const publicPath = resolve(directory, 'jwt-public.pem');
  if (existsSync(privatePath) !== existsSync(publicPath)) throw new Error('Incomplete local JWT key pair. Restore the matching key; do not silently rotate sessions.');
  if (!existsSync(privatePath)) {
    const keys = generateKeyPairSync('rsa', {modulusLength: 2048,
      publicKeyEncoding: {type: 'spki', format: 'pem'}, privateKeyEncoding: {type: 'pkcs8', format: 'pem'}});
    create('jwt-private.pem', keys.privateKey);
    create('jwt-public.pem', keys.publicKey);
  }
  const database = service => create(`${service}.database.url`,
    `postgres://planext4u_${service}_login:local-${service}-only@127.0.0.1:54320/planext4u_local?sslmode=disable`);
  const common = {APP_ENV: 'development', DATABASE_URL: '', DATABASE_MAX_CONNS: '10', DATABASE_MIN_CONNS: '1'};
  const jwt = {JWT_ISSUER: 'http://identity.local', JWT_AUDIENCE: 'planext4u-mobile', JWT_KEY_ID: 'local-phase0-v1'};
  const profiles = {
    identity: {...common, ...jwt, HTTP_ADDRESS: '127.0.0.1:8083', DATABASE_URL_FILE: database('identity'), TENANT_ID: tenantID,
      ALLOWED_COUNTRIES: 'IN', PROVIDER_BASE_URL: 'http://127.0.0.1:18081', JWT_PRIVATE_KEY_FILE: privatePath,
      REFRESH_HMAC_KEY_FILE: secret('refresh.key'), GUEST_SESSION_HMAC_KEY_FILE: secret('guest.key')},
    configuration: {...common, HTTP_ADDRESS: '127.0.0.1:8084', DATABASE_URL_FILE: database('configuration'), WEB_DEPLOYMENT_ID: 'local-phase0', PUBLIC_STARTUP_TENANT_ID: tenantID, PUBLIC_STARTUP_COUNTRIES: 'IN'},
    catalog: {...common, HTTP_ADDRESS: '127.0.0.1:8085', DATABASE_URL_FILE: database('catalog'),
      SERVICEABILITY_ZONES_FILE: create('catalog-zones.json', JSON.stringify([{tenant_id: tenantID, id: 'phase0-chennai', country: 'IN', locality: 'Chennai', minimum_latitude: 12.75, maximum_latitude: 13.35, minimum_longitude: 79.9, maximum_longitude: 80.5, postal_codes: ['600001']}]))},
    transaction: {...common, HTTP_ADDRESS: '127.0.0.1:8086', DATABASE_URL_FILE: database('transaction'), TENANT_ID: tenantID, SUPPORTED_COUNTRIES: 'IN',
      BOOKING_OTP_KEY_FILE: secret('booking.key'), EMERGENCY_DATA_KEY_FILE: secret('emergency.key'), LOCAL_VERTICALS_CONTACT_KEY_FILE: secret('contacts.key'),
      RAZORPAY_KEY_ID: '', RAZORPAY_API_SECRET_FILE: '', RAZORPAY_WEBHOOK_SECRET_FILE: '',
      PAYSTACK_PUBLIC_KEY: '', PAYSTACK_API_SECRET_FILE: '', PAYSTACK_WEBHOOK_SECRET_FILE: '',
      WALLET_PROGRAM_FILE: '', NOTIFICATION_BASE_URL: '', ORDER_NOTIFICATION_HMAC_KEY_FILE: '', PRIVATE_MEDIA_BASE_URL: '', BANK_VERIFICATION_BASE_URL: '', BANK_VERIFICATION_TOKEN_FILE: '', BANK_LINK_BASE_URL: '', BANK_LINK_TOKEN_FILE: '', BANK_LINK_LAUNCH_ORIGINS: '', LOCAL_SYNTHETIC_BANK_LINKS: 'false', KYC_VERIFICATION_BASE_URL: '', KYC_VERIFICATION_TOKEN_FILE: '', LOCAL_SYNTHETIC_KYC: 'false', OCR_VERIFICATION_BASE_URL: '', OCR_VERIFICATION_TOKEN_FILE: '', LOCAL_SYNTHETIC_OCR: 'false'},
    admin: {...common, HTTP_ADDRESS: '127.0.0.1:8090', DATABASE_URL_FILE: database('admin'), TENANT_ID: tenantID, ADMIN_ALLOWED_ORIGINS: 'https://localhost:4173,https://127.0.0.1:4173',
      ADMIN_REQUIRE_MFA: 'true', ADMIN_EXCHANGE_SECRET_FILE: secret('admin-exchange.key')},
    media: {...common, HTTP_ADDRESS: '127.0.0.1:8087', DATABASE_URL_FILE: database('media'), AWS_REGION: 'ap-south-1',
      MEDIA_BUCKET: 'planext4u-phase2-private', S3_ENDPOINT: 'http://127.0.0.1:9000', S3_PUBLIC_ENDPOINT: 'http://127.0.0.1:9000', S3_FORCE_PATH_STYLE: 'true',
      S3_ACCESS_KEY_ID_FILE: create('media.s3-access', 'planext4u-local'), S3_SECRET_ACCESS_KEY_FILE: create('media.s3-secret', 'local-only-object-password'),
      MALWARE_SCANNER_MODE: 'clamav', CLAMAV_SOCKET: '/run/clamav/clamd.sock'},
    gateway: {APP_ENV: 'development', ...jwt, HTTP_ADDRESS: '127.0.0.1:8082', JWT_PUBLIC_KEY_FILE: publicPath,
      REDIS_URL_FILE: create('redis.url', 'redis://127.0.0.1:63790/0'), UPSTREAM_URL: 'http://127.0.0.1:8086',
      UPSTREAM_ROUTES: JSON.stringify({'/v1/auth': 'http://127.0.0.1:8083', '/v1/me': 'http://127.0.0.1:8083', '/v1/startup': 'http://127.0.0.1:8084', '/v1/bootstrap': 'http://127.0.0.1:8084', '/v1/occupations': 'http://127.0.0.1:8084', '/v1/geography': 'http://127.0.0.1:8084', '/v1/pages': 'http://127.0.0.1:8084', '/v1/home': 'http://127.0.0.1:8085', '/v1/catalog': 'http://127.0.0.1:8085', '/v1/serviceability': 'http://127.0.0.1:8085', '/v1/media': 'http://127.0.0.1:8087'}),
      ANONYMOUS_ROUTES: JSON.stringify({'/v1/auth/exchange': ['POST'], '/v1/auth/enroll': ['POST'], '/v1/auth/recover': ['POST'], '/v1/auth/refresh': ['POST'], '/v1/startup': ['GET']})},
    migration: {MIGRATION_DATABASE_URL_FILE: create('migration.database.url', 'postgres://planext4u_local:local-only-password@127.0.0.1:54320/planext4u_local?sslmode=disable')},
  };
  for (const [service, profile] of Object.entries(profiles)) create(`${service}.json`, `${JSON.stringify(profile, null, 2)}\n`);
  return Object.keys(profiles);
}

export function loadLocalProfile(service, directory = runtimeDirectory) {
  if (![...services,'migration'].includes(service)) throw new Error('Unknown local service.');
  const profile = JSON.parse(readFileSync(resolve(directory, `${service}.json`), 'utf8'));
  if (service !== 'migration' && (profile.APP_ENV !== 'development' || !profile.HTTP_ADDRESS.startsWith('127.0.0.1:'))) throw new Error('Local launcher requires development mode and a loopback bind.');
  const databaseFile = profile.DATABASE_URL_FILE ?? profile.MIGRATION_DATABASE_URL_FILE;
  if (databaseFile) {
    let url;
    try { url = new URL(readFileSync(databaseFile, 'utf8').trim()); } catch { throw new Error('Local database profile is invalid.'); }
    if (url.protocol !== 'postgres:' || url.hostname !== '127.0.0.1' || url.port !== '54320' || url.pathname !== '/planext4u_local') throw new Error('Local launcher only permits the isolated loopback database.');
  }
  return profile;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const command = process.argv[2];
    if (command === 'prepare') {
      prepareLocal();
      console.log('Prepared isolated local runtime profiles under .local/completion/runtime. Existing keys and profiles are preserved.');
    } else if (command === 'tls') {
      prepareLocal();
      const key = resolve(runtimeDirectory, 'localhost.key');
      const cert = resolve(runtimeDirectory, 'localhost.crt');
      if (existsSync(key) !== existsSync(cert)) throw new Error('Incomplete local TLS pair; restore the matching files.');
      if (!existsSync(key)) {
        const requested = process.argv.find(value => value.startsWith('--openssl='))?.slice(10);
        const gitOpenSSL = resolve(process.env.ProgramFiles ?? 'C:/Program Files', 'Git/usr/bin/openssl.exe');
        const executable = requested || (process.platform === 'win32' && existsSync(gitOpenSSL) ? gitOpenSSL : 'openssl');
        const result = spawnSync(executable, ['req','-x509','-newkey','rsa:2048','-sha256','-nodes','-days','30',
          '-subj','/CN=localhost','-addext','subjectAltName=DNS:localhost,IP:127.0.0.1','-keyout',key,'-out',cert],
          {cwd: repositoryRoot, encoding: 'utf8', windowsHide: true, timeout: 30_000});
        if (result.status !== 0) throw new Error('OpenSSL is required to create the local TLS certificate; pass --openssl=/path/to/openssl.');
      }
      console.log('Local TLS files are available under .local/completion/runtime. Use HTTPS for administrator cookies and origins.');
    } else if (command === 'admin-web') {
      const key = resolve(runtimeDirectory, 'localhost.key');
      const cert = resolve(runtimeDirectory, 'localhost.crt');
      if (!existsSync(key) || !existsSync(cert)) throw new Error('Run the tls command before starting the administrator UI.');
      const child = spawn(process.execPath, ['node_modules/vite/bin/vite.js','--host','127.0.0.1'], {
        cwd: resolve(repositoryRoot, 'admin-web'), stdio: 'inherit', windowsHide: true,
        env: {...process.env, ADMIN_API_ORIGIN: 'http://127.0.0.1:8090', ADMIN_DEV_TLS_KEY_FILE: key, ADMIN_DEV_TLS_CERT_FILE: cert}});
      child.on('error', error => { console.error(error.message); process.exitCode = 2; });
      child.on('exit', code => { process.exitCode = code ?? 1; });
    } else if (command === 'start' || command === 'migrate') {
      const service = command === 'migrate' ? 'migration' : process.argv[3];
      const profile = loadLocalProfile(service);
      const executable = process.argv.find(value => value.startsWith('--go='))?.slice(5) || 'go';
      const args = command === 'migrate' ? ['run','./cmd/migrate','-service','all'] : ['run',`./cmd/${service}`];
      const child = spawn(executable, args, {cwd: repositoryRoot, env: {...process.env, ...profile}, stdio: 'inherit', windowsHide: true});
      child.on('error', error => { console.error(error.code === 'ENOENT' ? 'Go is required; put it on PATH or pass --go=/absolute/path/to/go.' : error.message); process.exitCode = 2; });
      child.on('exit', code => { process.exitCode = code ?? 1; });
    } else {
      console.error('Usage: node scripts/completion/runtime.mjs prepare|tls|admin-web|migrate|start SERVICE [--go=/path/to/go] [--openssl=/path/to/openssl]');
      process.exitCode = 64;
    }
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
