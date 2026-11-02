import {readFileSync, existsSync} from 'node:fs';
import {resolve, relative, isAbsolute, sep} from 'node:path';
import {fileURLToPath} from 'node:url';
import {checksum, validateFinancialContract, validateFinancialReferences} from './finance.mjs';

export const repositoryRoot = fileURLToPath(new URL('../../', import.meta.url));
export const registerRoot = resolve(repositoryRoot, 'docs/completion/phase-0');
const text = value => typeof value === 'string' && value.trim().length > 0;
const date = value => typeof value === 'string' && /^\d{4}-\d{2}-\d{2}$/.test(value) &&
  !Number.isNaN(Date.parse(value)) && new Date(value).toISOString().slice(0, 10) === value;
const sha = value => typeof value === 'string' && /^[a-f0-9]{64}$/.test(value);
const unique = values => new Set(values).size === values.length;
const requiredLocalChecks = ['backend-linux','admin-verify','admin-e2e','mobile-quality','mobile-android','mobile-ios','local-stack','local-role-smoke'];
const inside = (base, path) => {
  if (!text(path) || isAbsolute(path) || path.includes('\\')) return false;
  const rel = relative(base, resolve(base, path));
  return rel !== '..' && !rel.startsWith(`..${sep}`) && !isAbsolute(rel);
};

export function loadRegister(base = registerRoot) {
  const read = name => JSON.parse(readFileSync(resolve(base, name), 'utf8'));
  return {
    phase: read('phase-0.json'), requirements: read('requirements.json'), baseline: read('coverage-baseline.json'),
    sources: read('sources.json'), decisions: read('decisions.json'), dependencies: read('dependencies.json'),
    catalogues: read('catalogues.json'), golden: read('golden-cases.json'),
  };
}

function receiptValid(path, base, requiredID, revisions) {
  if (!inside(base, path)) return false;
  try {
    const record = JSON.parse(readFileSync(resolve(base, path), 'utf8'));
    return record.check_id === requiredID && record.result === 'PASSED' &&
      text(record.command) && text(record.observed) && text(record.environment) &&
      text(record.executed_at) && !Number.isNaN(Date.parse(record.executed_at)) &&
      record.exit_code === 0 && record.source_revisions &&
      Object.values(record.source_revisions).length > 0 &&
      Object.values(record.source_revisions).every(value => /^[a-f0-9]{40}$/.test(value)) &&
      Object.entries(revisions).every(([name, revision]) => record.source_revisions[name] === revision);
  } catch { return false; }
}

function reviewed(review, base) {
  return review && Array.isArray(review.by_roles) && review.by_roles.length > 0 &&
    review.by_roles.every(text) && date(review.date) && text(review.summary) &&
    Array.isArray(review.evidence) && review.evidence.length > 0 &&
    review.evidence.every(path => inside(base, path) && existsSync(resolve(base, path)));
}

export function configurableFinancialBaseline(data, base = registerRoot) {
  const policy = data.phase.financial_baseline;
  if (policy?.mode !== 'USER_APPROVED_CONFIGURABLE_RULES') return false;
  if (![policy.user_decision,policy.contract,policy.verification].every(path => inside(base,path))) return false;
  try {
    const decision = JSON.parse(readFileSync(resolve(base,policy.user_decision),'utf8'));
    if (decision.kind !== 'direct_user_decisions' || !decision.decisions?.some(value => value.id === 'USER-FINANCE-CONFIGURABLE' && value.choice === 'CONFIGURABLE' && text(value.answer_verbatim))) return false;
    const bytes = readFileSync(resolve(base,policy.contract));
    const contract = JSON.parse(bytes);
    if (validateFinancialContract(contract).length || validateFinancialReferences(contract,data.golden).length) return false;
    if (!receiptValid(policy.verification,base,'financial-policy-contract',data.phase.source_revisions)) return false;
    const receipt = JSON.parse(readFileSync(resolve(base,policy.verification),'utf8'));
    return receipt.contract_sha256 === checksum(bytes) && receipt.references_sha256 === checksum(readFileSync(resolve(base,'golden-cases.json'))) &&
      receipt.tool_sha256 === checksum(readFileSync(new URL('./finance.mjs',import.meta.url))) && receipt.runtime_connected === false;
  } catch { return false; }
}

export function audioScopeConfirmed(source, base = registerRoot) {
  const review=source.scope_confirmation;
  if (source.kind !== 'audio' || source.review_status !== 'SCOPE_CONFIRMED_BY_USER' ||
      !date(review?.date) || !text(review?.by_role) || !inside(base,review?.evidence) ||
      !text(review?.decision_id) || !source.segments?.length) return false;
  try {
    const receipt=JSON.parse(readFileSync(resolve(base,review.evidence),'utf8'));
    return receipt.kind === 'direct_user_decisions' && receipt.decisions?.some(decision =>
      decision.id === review.decision_id && ['ACCEPT_INTERPRETATION','ACCEPT_SCOPE'].includes(decision.choice) &&
      text(decision.answer_verbatim) && decision.scope_sources?.includes(source.id)) === true;
  } catch {return false;}
}

export function audioScopeReconciled(source, data, base = registerRoot) {
  const review=source.scope_reconciliation;
  if (source.kind !== 'audio' || source.review_status !== 'SCOPE_RECONCILED' || !reviewed(review,base) ||
      source.unresolved_intervals?.length !== 0 || !source.segments?.length || !inside(base,review.receipt)) return false;
  try {
    const receipt=JSON.parse(readFileSync(resolve(base,review.receipt),'utf8'));
    if (receipt.kind !== 'engineering_tamil_scope_review' || !inside(base,receipt.authorization_evidence)) return false;
    const authority=JSON.parse(readFileSync(resolve(base,receipt.authorization_evidence),'utf8'));
    if (authority.kind !== 'direct_user_decisions' || !authority.decisions?.some(decision=>
      decision.id===receipt.authorization_decision && decision.choice==='AUTHORIZE_ENGINEERING_SCOPE_REVIEW' &&
      text(decision.answer_verbatim) && decision.scope_sources?.includes(source.id))) return false;
    const record=receipt.records?.find(record=>record.id===source.id);
    return record?.result==='RECONCILED' && record.source_sha256===source.sha256 && sha(record.transcript_sha256) &&
      record.segments_sha256===checksum(JSON.stringify(source.segments)) && record.written_references?.length>0 &&
      record.written_references.every(ref=>text(ref.locator) && data.sources.sources.some(item=>
        item.id===ref.source && item.kind==='requirements' && item.sha256===ref.sha256));
  } catch {return false;}
}

export function deferredLocalCheck(data, id, base = registerRoot) {
  // Only the explicitly user-deferred iOS build can leave the local gate pending.
  // A failed build is a regression, not an unavailable platform prerequisite.
  if (id!=='mobile-ios') return false;
  const deferral=data.phase.deferred_local_checks?.find(item=>item.id===id);
  const actual=data.phase.verification_results.find(item=>item.id===id);
  if (!deferral || !['UNAVAILABLE','NOT_RUN'].includes(actual?.result) ||
      deferral.status!=='DEFERRED_BY_USER' || deferral.scope!=='LOCAL_PHASE_0_ONLY' ||
      deferral.release_blocking!==true || !text(deferral.needed_by) || !inside(base,deferral.evidence)) return false;
  try {
    const receipt=JSON.parse(readFileSync(resolve(base,deferral.evidence),'utf8'));
    return receipt.kind==='direct_user_decisions' && receipt.decisions?.some(decision=>
      decision.id===deferral.decision_id && decision.choice==='LOCAL_PHASE0_WITH_IOS_PENDING' &&
      decision.checks?.includes(id) && text(decision.answer_verbatim))===true;
  } catch {return false;}
}

export function validateRegister(data, base = registerRoot) {
  const errors = [];
  const require = (condition, message) => { if (!condition) errors.push(message); };
  const items = data.requirements.items;
  require(data.phase.schema_version === 1 && data.phase.phase === 0, 'Unknown Phase 0 schema.');
  require(data.phase.deployment_target === 'LOCAL' && data.phase.ownership_policy === 'ROLE_ONLY', 'Retain the user-approved local and role-only scope.');
  require(Array.isArray(items) && items.length > 0, 'Requirements are missing.');
  if (!Array.isArray(items)) return errors;
  const ids = items.map(item => item.id);
  require(unique(ids), 'Duplicate requirement IDs.');
  require(data.baseline.baseline_ids.length === 71 && unique(data.baseline.baseline_ids), 'Retain the 71 baseline IDs.');
  require(data.baseline.additional_ids.length >= 24 && unique(data.baseline.additional_ids), 'Retain all 24 additional capability checks.');
  for (const id of [...data.baseline.baseline_ids, ...data.baseline.additional_ids]) require(ids.includes(id), `Missing requirement ${id}.`);
  const sourceIDs = data.sources.sources.map(source => source.id);
  require(unique(sourceIDs), 'Duplicate source IDs.');
  const dependencyIDs = data.dependencies.dependencies.map(dependency => dependency.id);
  require(unique(dependencyIDs), 'Duplicate dependency IDs.');
  for (const item of items) {
    const label = item.id ?? 'Unnamed requirement';
    require(/^P4U-[A-Z]+-\d{3}$/.test(label), `Invalid requirement ID ${label}.`);
    require(text(item.owner_role) && item.accountable_roles?.length > 0, `${label}: owner role missing.`);
    require(Number.isInteger(item.primary_closure_phase) && item.primary_closure_phase >= 0 && item.primary_closure_phase <= 10, `${label}: invalid delivery phase.`);
    require(['P0','P1','P2'].includes(item.priority), `${label}: priority missing.`);
    require(item.acceptance_refs?.length > 0 && item.acceptance_refs.every(ref => /^AC-\d+\.\d+$/.test(ref)), `${label}: acceptance references missing.`);
    for (const field of ['actor','action','expected','verification']) require(text(item.acceptance_scenario?.[field]), `${label}: acceptance ${field} missing.`);
    require(sourceIDs.includes(item.source_reference?.source) && text(item.source_reference?.locator), `${label}: source reference missing.`);
    require(item.evidence_location === `evidence/requirements/${label}.json`, `${label}: invalid evidence location.`);
    require(item.dependencies?.length > 0 && item.dependencies.every(id => dependencyIDs.includes(id)), `${label}: unknown dependency.`);
    require(['NOT_RUN','PARTIAL','VERIFIED'].includes(item.verification_status), `${label}: invalid verification status.`);
    if (item.verification_status === 'VERIFIED') require(receiptValid(item.evidence_location, base, label, data.phase.source_revisions), `${label}: VERIFIED requires an execution receipt.`);
  }
  const audio = data.sources.sources.filter(source => source.kind === 'audio');
  require(audio.length === 9, 'Inventory all nine supplied audio recordings.');
  for (const source of data.sources.sources) {
    if (source.path !== null) require(sha(source.sha256), `${source.id}: missing source checksum.`);
    if (source.kind !== 'audio') continue;
    if (source.review_status === 'SCOPE_CONFIRMED_BY_USER') require(audioScopeConfirmed(source,base), `${source.id}: scope confirmation needs direct user evidence mapped to this recording.`);
    if (source.review_status === 'SCOPE_RECONCILED') require(audioScopeReconciled(source,data,base), `${source.id}: scope reconciliation needs matching transcript/mapping hashes, written sources and user authorization.`);
    require(Array.isArray(source.segments), `${source.id}: audio segments must be an array.`);
    for (const segment of source.segments ?? []) {
      require(Number.isFinite(segment.start_seconds) && Number.isFinite(segment.end_seconds) &&
        segment.start_seconds >= 0 && segment.end_seconds > segment.start_seconds &&
        (!source.duration_seconds || segment.end_seconds <= source.duration_seconds + 1), `${source.id}: invalid audio timestamp.`);
      require(text(segment.summary) && segment.requirement_ids?.length > 0 && segment.requirement_ids.every(id => ids.includes(id)), `${source.id}: map each reviewed segment to requirements.`);
    }
  }
  const decisions = data.decisions.decisions;
  require(unique(decisions.map(decision => decision.id)), 'Duplicate decision IDs.');
  for (const decision of decisions) {
    require(text(decision.owner_role) && text(decision.question) && decision.requirements.every(id => ids.includes(id)), `${decision.id}: missing role, question or requirement mapping.`);
    require(['OPEN','RESOLVED'].includes(decision.status), `${decision.id}: invalid decision status.`);
    if (decision.status === 'RESOLVED') require(text(decision.resolution) && date(decision.reviewed_date) &&
      decision.reviewed_by_roles?.length > 0 && decision.evidence?.length > 0 &&
      decision.evidence.every(path => inside(base, path) && existsSync(resolve(base, path))), `${decision.id}: resolution needs a dated role review and evidence.`);
  }
  for (const dependency of data.dependencies.dependencies) {
    require(text(dependency.owner_role) && text(dependency.needed_by), `${dependency.id}: role or milestone missing.`);
    require(dependency.target_date === null || date(dependency.target_date), `${dependency.id}: invalid target date.`);
    require(['OPEN','VERIFIED','DEFERRED_BY_USER'].includes(dependency.status), `${dependency.id}: invalid dependency status.`);
    if (dependency.status === 'VERIFIED') require(dependency.evidence?.length > 0 &&
      dependency.evidence.every(path => inside(base, path) && existsSync(resolve(base, path))), `${dependency.id}: readiness evidence missing.`);
  }
  for (const work of data.dependencies.work_packages) require(text(work.owner_role) &&
    work.estimate_engineer_days?.length === 2 && work.estimate_engineer_days.every(Number.isFinite) &&
    work.estimate_engineer_days[0] > 0 && work.estimate_engineer_days[1] >= work.estimate_engineer_days[0] &&
    work.dependencies.every(id => dependencyIDs.includes(id)), `${work.id}: invalid estimate or dependency.`);
  const checks = data.phase.required_local_checks;
  require(Array.isArray(checks) && requiredLocalChecks.every(id => checks.includes(id)) && unique(checks), 'Required local verification checks missing.');
  require(unique(data.phase.verification_results.map(result => result.id)), 'Duplicate verification results.');
  for (const result of data.phase.verification_results) {
    require(checks.includes(result.id) && ['PASSED','PARTIAL','FAILED','UNAVAILABLE','NOT_RUN'].includes(result.result), `${result.id}: invalid local result.`);
    if (result.result === 'PASSED') require(receiptValid(result.evidence, base, result.id, data.phase.source_revisions), `${result.id}: PASSED requires a valid execution receipt.`);
  }
  require(unique((data.phase.deferred_local_checks??[]).map(item=>item.id)), 'Duplicate local check deferrals.');
  for (const item of data.phase.deferred_local_checks??[]) require(deferredLocalCheck(data,item.id,base), `${item.id}: invalid local-only deferral; direct user evidence and an unavailable iOS check are required.`);
  require(unique(data.golden.cases.map(value => value.id)), 'Duplicate golden case IDs.');
  for (const id of ['GOLDEN-SAMPLE','GOLDEN-DISCOUNT-BEARER','GOLDEN-PRODUCT-TAX','GOLDEN-WALLET','GOLDEN-SUPPLIER-SETTLEMENT','GOLDEN-RIDER']) require(data.golden.cases.some(value => value.id === id), `${id}: required reference case missing.`);
  for (const value of data.golden.cases) {
    require(text(value.source) && text(value.expected_behavior) && ['PENDING_FINANCE_REVIEW','CONFIGURABLE_REFERENCE','APPROVED'].includes(value.policy_status), `${value.id}: source, scenario or policy state missing.`);
    if (value.policy_status === 'APPROVED') require(reviewed(value.review, base), `${value.id}: approval needs evidence.`);
    if (value.policy_status === 'CONFIGURABLE_REFERENCE') require(reviewed(value.review, base), `${value.id}: configurable reference needs a documented design review.`);
  }
  return errors;
}

export function evaluateGate(data, base = registerRoot) {
  const errors = validateRegister(data, base);
  if (errors.length) return {phase: 0, result: 'INVALID', errors, next_phase_allowed: false, criteria: []};
  const scope = [];
  for (const source of data.sources.sources) {
    if (source.kind === 'audio' && !((source.review_status === 'VERIFIED' && text(source.verified_by_role) && date(source.verified_date) && source.segments.length > 0) || audioScopeConfirmed(source,base) || audioScopeReconciled(source,data,base))) scope.push(`${source.id}: timestamped scope verification remains open`);
    if (source.kind === 'original_requirement' && !text(source.resolution)) scope.push(`${source.id}: replacement/source decision remains open`);
  }
  if (!reviewed(data.phase.scope_review, base)) scope.push('Product/QA scope reconciliation evidence is missing');
  if (!reviewed(data.catalogues.review, base)) scope.push('Widget/report/provider/media/field/state/permission catalogue reconciliation is open');
  for (const decision of data.decisions.decisions) if (['DEC-HIERARCHY','DEC-GATEWAYS','DEC-ADS','DEC-MEDIA','DEC-SCOPE'].includes(decision.id) && decision.status !== 'RESOLVED') scope.push(`${decision.id}: catalogue/scope decision remains open`);
  const deferred=data.phase.required_local_checks.filter(id=>deferredLocalCheck(data,id,base));
  const local = data.phase.required_local_checks.filter(id => !data.phase.verification_results.some(result => result.id === id && result.result === 'PASSED') && !deferred.includes(id)).map(id => `${id}: execution has not passed`);
  const configured = configurableFinancialBaseline(data,base);
  const financial = data.decisions.decisions.filter(decision => ['DEC-DISCOUNT','DEC-TAX','DEC-WALLET','DEC-RIDER'].includes(decision.id) &&
    !(decision.status === 'RESOLVED' && ((decision.reviewed_by_roles.some(role => /Finance/i.test(role)) && decision.reviewed_by_roles.some(role => /Product/i.test(role))) ||
      (configured && decision.resolution_type === 'CONFIGURABLE_BY_USER' && decision.evidence.includes(data.phase.financial_baseline.user_decision)))))
    .map(decision => `${decision.id}: Product/Finance rule decision remains open`);
  for (const value of data.golden.cases) if (!((value.policy_status === 'APPROVED' && reviewed(value.review, base) &&
    value.review.by_roles.some(role => /Finance/i.test(role)) && value.review.by_roles.some(role => /Product/i.test(role))) ||
    (configured && value.policy_status === 'CONFIGURABLE_REFERENCE' && reviewed(value.review,base)))) financial.push(`${value.id}: reference case requires a reviewed policy or verified user-approved configuration contract`);
  const mobilePrerequisiteDeferred=deferred.includes('mobile-ios') && ['mobile-quality','mobile-android'].every(id=>data.phase.verification_results.some(result=>result.id===id && result.result==='PASSED'));
  const readiness = data.dependencies.dependencies.filter(dependency => dependency.needed_before_phase === 0 && dependency.status !== 'VERIFIED' &&
    !(dependency.id==='DEP-MOBILE-TOOLS' && dependency.status==='DEFERRED_BY_USER' && dependency.pending_check==='mobile-ios' && mobilePrerequisiteDeferred)).map(dependency => `${dependency.id}: prerequisite remains open`);
  if (!reviewed(data.phase.readiness_review, base)) readiness.push('Readiness/capacity review and explicit provider/export schedule evidence is missing');
  if (data.decisions.decisions.some(decision => decision.id === 'DEC-RECOVERY' && decision.status !== 'RESOLVED')) readiness.push('DEC-RECOVERY: domain recovery objectives remain open');
  const criteria = [scope, local, financial, readiness].map((issues, index) => ({id: `AC-0.${index + 1}`, result: issues.length ? 'OPEN' : 'PASSED', issues}));
  const passed = criteria.every(value => value.result === 'PASSED');
  return {phase: 0, result: passed ? (deferred.length ? 'PASSED_WITH_DEFERRALS' : 'PASSED') : 'OPEN', deployment_target: data.phase.deployment_target,
    ownership_policy: data.phase.ownership_policy, coverage: {baseline: data.baseline.baseline_ids.length, additional: data.baseline.additional_ids.length, total: data.requirements.items.length},
    next_phase_allowed: passed, deferred_checks:deferred.map(id=>({id,result:data.phase.verification_results.find(item=>item.id===id).result,release_blocking:true,scope:'LOCAL_PHASE_0_ONLY'})), release_ready:false, criteria};
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const command = process.argv[2] ?? 'validate';
    if (!['validate','gate'].includes(command)) throw new Error('Usage: node scripts/completion/check.mjs validate|gate');
    const data = loadRegister();
    if (command === 'validate') {
      const errors = validateRegister(data);
      if (errors.length) { console.error(errors.join('\n')); process.exitCode = 1; }
      else console.log(`Register valid: ${data.requirements.items.length} requirements. This does not pass the Phase 0 exit gate.`);
    } else {
      const result = evaluateGate(data);
      console.log(JSON.stringify(result, null, 2));
      process.exitCode = ['PASSED','PASSED_WITH_DEFERRALS'].includes(result.result) ? 0 : result.result === 'OPEN' ? 2 : 1;
    }
  } catch (error) { console.error(error.message); process.exitCode = 1; }
}
