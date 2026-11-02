import {test} from 'node:test';
import assert from 'node:assert/strict';
import {mkdtempSync, mkdirSync, readFileSync, writeFileSync, unlinkSync, rmdirSync} from 'node:fs';
import {tmpdir} from 'node:os';
import {join} from 'node:path';
import {loadRegister, validateRegister, evaluateGate, configurableFinancialBaseline, audioScopeConfirmed, audioScopeReconciled, deferredLocalCheck, registerRoot} from './check.mjs';
import {checksum} from './finance.mjs';

const fresh = () => {
  const data = structuredClone(loadRegister());
  // Tests model outstanding execution even after the live register is closed.
  data.phase.verification_results = [];
  delete data.phase.financial_baseline;
  delete data.phase.deferred_local_checks;
  return data;
};
test('all 71 baseline and 24 additional requirements have usable scenarios', () => {
  assert.deepEqual(validateRegister(fresh()), []);
});
test('a valid register with outstanding work cannot allow the next phase', () => {
  const data = fresh();
  data.phase.status = 'COMPLETE';
  const gate = evaluateGate(data);
  assert.equal(gate.next_phase_allowed, false);
  assert.equal(gate.result, 'OPEN');
  assert.equal(gate.criteria.length, 4);
});
test('removing a requirement fails coverage even if totals still look plausible', () => {
  const data = fresh();
  data.requirements.items.pop();
  assert.ok(validateRegister(data).some(error => error.includes('Missing requirement')));
});
test('duplicate IDs cannot substitute for missing coverage', () => {
  const data = fresh();
  data.requirements.items[1].id = data.requirements.items[0].id;
  assert.ok(validateRegister(data).some(error => error.includes('Duplicate requirement')));
});
test('invalid phase, missing scenario, source and milestone fail validation', () => {
  for (const change of [
    data => { data.requirements.items[0].primary_closure_phase = 11; },
    data => { data.requirements.items[0].acceptance_scenario.expected = ''; },
    data => { data.requirements.items[0].source_reference.source = 'unknown'; },
    data => { data.dependencies.dependencies[0].needed_by = ''; },
  ]) { const data = fresh(); change(data); assert.ok(validateRegister(data).length > 0); }
});
test('individual names are not required by the role-only user scope', () => {
  const data = fresh();
  assert.equal(data.phase.ownership_policy, 'ROLE_ONLY');
  assert.deepEqual(validateRegister(data), []);
});
test('remote deployment cannot silently replace the local gate', () => {
  const data = fresh();
  data.phase.deployment_target = 'REMOTE';
  assert.ok(validateRegister(data).some(error => error.includes('local and role-only')));
});
test('a claimed pass without a receipt is rejected', () => {
  const data = fresh();
  data.phase.verification_results = [{id: 'local-stack', result: 'PASSED', evidence: 'evidence/missing.json'}];
  assert.equal(evaluateGate(data).result, 'INVALID');
});
test('failed, wrong-check and malformed receipts cannot certify a local check', () => {
  const base = mkdtempSync(join(tmpdir(), 'planext4u-phase0-test-'));
  try {
    mkdirSync(join(base, 'evidence'));
    for (const record of [{check_id: 'local-stack', result: 'FAILED'}, {check_id: 'other', result: 'PASSED'}, {}]) {
      writeFileSync(join(base, 'evidence/result.json'), JSON.stringify(record));
      const data = fresh();
      data.phase.verification_results = [{id: 'local-stack', result: 'PASSED', evidence: 'evidence/result.json'}];
      assert.equal(evaluateGate(data, base).result, 'INVALID');
    }
  } finally {
    unlinkSync(join(base, 'evidence/result.json'));
    rmdirSync(join(base, 'evidence'));
    rmdirSync(base);
  }
});
test('evidence paths cannot escape the register directory', () => {
  const data = fresh();
  data.phase.verification_results = [{id: 'local-stack', result: 'PASSED', evidence: '../phase-0.json'}];
  assert.equal(evaluateGate(data).result, 'INVALID');
  data.requirements.items[0].evidence_location = 'C:/personal/passed.json';
  assert.ok(validateRegister(data).some(error => error.includes('evidence location')));
});
test('verified features require their own execution evidence', () => {
  const data = fresh();
  data.requirements.items[0].verification_status = 'VERIFIED';
  assert.ok(validateRegister(data).some(error => error.includes('VERIFIED requires')));
});
test('audio timestamps and requirement mappings must be usable', () => {
  for (const segment of [
    {start_seconds: -1, end_seconds: 2, summary: 'draft', requirement_ids: ['P4U-ID-001']},
    {start_seconds: 4, end_seconds: 2, summary: 'draft', requirement_ids: ['P4U-ID-001']},
    {start_seconds: 1, end_seconds: 2, summary: 'draft', requirement_ids: ['P4U-UNKNOWN-999']},
  ]) {
    const data = fresh();
    data.sources.sources.find(source => source.kind === 'audio').segments = [segment];
    assert.ok(validateRegister(data).length > 0);
  }
});
test('transcribed audio remains open until its scope has been reviewed', () => {
  const data = fresh();
  const audio = data.sources.sources.find(source => source.kind === 'audio');
  audio.review_status = 'MACHINE_DRAFT';
  audio.segments = [{start_seconds: 0, end_seconds: 1, summary: 'Unverified draft', requirement_ids: ['P4U-VND-001']}];
  assert.equal(evaluateGate(data).criteria[0].result, 'OPEN');
});

test('direct user intent confirmation applies only to its mapped recording and does not verify the transcript',()=>{
  const base=mkdtempSync(join(tmpdir(),'planext4u-audio-scope-'));
  const source={id:'AUDIO-005',kind:'audio',review_status:'SCOPE_CONFIRMED_BY_USER',segments:[{start_seconds:0,end_seconds:1}],
    scope_confirmation:{date:'2026-10-24',by_role:'Product',evidence:'review.json',decision_id:'SYNTHETIC-SCOPE'}};
  try {
    writeFileSync(join(base,'review.json'),JSON.stringify({kind:'direct_user_decisions',decisions:[{id:'SYNTHETIC-SCOPE',choice:'ACCEPT_INTERPRETATION',answer_verbatim:'Synthetic unit-test evidence only',scope_sources:['AUDIO-005']}]}));
    assert.equal(audioScopeConfirmed(source,base),true);
    source.id='AUDIO-006';assert.equal(audioScopeConfirmed(source,base),false);
    source.id='AUDIO-005';source.review_status='MACHINE_DRAFT';assert.equal(audioScopeConfirmed(source,base),false);
    source.review_status='SCOPE_CONFIRMED_BY_USER';source.scope_confirmation.evidence='../outside.json';assert.equal(audioScopeConfirmed(source,base),false);
  } finally {unlinkSync(join(base,'review.json'));rmdirSync(base);}
});

test('engineering audio reconciliation requires authorization, matching mappings and corroborating written sources',()=>{
  const base=mkdtempSync(join(tmpdir(),'planext4u-audio-reconcile-'));
  const source={id:'AUDIO-001',kind:'audio',sha256:'a'.repeat(64),review_status:'SCOPE_RECONCILED',
    unresolved_intervals:[],segments:[{start_seconds:0,end_seconds:1,summary:'Synthetic scope',requirement_ids:['P4U-ID-001']}],
    scope_reconciliation:{by_roles:['Engineering'],date:'2026-10-24',summary:'Synthetic test review',evidence:['review.json'],receipt:'review.json'}};
  const data={sources:{sources:[{id:'SOW-09',kind:'requirements',sha256:'b'.repeat(64)}]}};
  const record={id:source.id,result:'RECONCILED',source_sha256:source.sha256,transcript_sha256:'c'.repeat(64),segments_sha256:checksum(JSON.stringify(source.segments)),written_references:[{source:'SOW-09',sha256:'b'.repeat(64),locator:'§2'}]};
  try {
    writeFileSync(join(base,'authority.json'),JSON.stringify({kind:'direct_user_decisions',decisions:[{id:'TEST',choice:'AUTHORIZE_ENGINEERING_SCOPE_REVIEW',answer_verbatim:'Synthetic review request',scope_sources:['AUDIO-001']}]}));
    writeFileSync(join(base,'review.json'),JSON.stringify({kind:'engineering_tamil_scope_review',authorization_evidence:'authority.json',authorization_decision:'TEST',records:[record]}));
    assert.equal(audioScopeReconciled(source,data,base),true);
    source.segments[0].end_seconds=2;assert.equal(audioScopeReconciled(source,data,base),false);
    source.segments[0].end_seconds=1;
    data.sources.sources[0].sha256='d'.repeat(64);assert.equal(audioScopeReconciled(source,data,base),false);
    data.sources.sources[0].sha256='b'.repeat(64);
    source.unresolved_intervals=[{reason:'Unknown scope'}];assert.equal(audioScopeReconciled(source,data,base),false);
  } finally {unlinkSync(join(base,'review.json'));unlinkSync(join(base,'authority.json'));rmdirSync(base);}
});

test('local iOS deferral requires explicit authority and preserves unavailable versus failed results',()=>{
  const base=mkdtempSync(join(tmpdir(),'planext4u-ios-deferral-'));
  const data=fresh();
  data.phase.verification_results=[{id:'mobile-ios',result:'UNAVAILABLE'}];
  data.phase.deferred_local_checks=[{id:'mobile-ios',status:'DEFERRED_BY_USER',scope:'LOCAL_PHASE_0_ONLY',release_blocking:true,needed_by:'before iOS acceptance/release',evidence:'authority.json',decision_id:'TEST'}];
  try {
    writeFileSync(join(base,'authority.json'),JSON.stringify({kind:'direct_user_decisions',decisions:[{id:'TEST',choice:'LOCAL_PHASE0_WITH_IOS_PENDING',checks:['mobile-ios'],answer_verbatim:'Synthetic local-only deferral'}]}));
    assert.equal(deferredLocalCheck(data,'mobile-ios',base),true);
    assert.equal(data.phase.verification_results[0].result,'UNAVAILABLE');
    assert.equal(deferredLocalCheck(data,'backend-linux',base),false);
    data.phase.verification_results[0].result='FAILED';assert.equal(deferredLocalCheck(data,'mobile-ios',base),false);
    data.phase.verification_results[0].result='UNAVAILABLE';
    data.phase.deferred_local_checks[0].release_blocking=false;assert.equal(deferredLocalCheck(data,'mobile-ios',base),false);
    data.phase.deferred_local_checks[0].release_blocking=true;
    writeFileSync(join(base,'authority.json'),JSON.stringify({kind:'direct_user_decisions',decisions:[]}));
    assert.equal(deferredLocalCheck(data,'mobile-ios',base),false);
  } finally {unlinkSync(join(base,'authority.json'));rmdirSync(base);}
});
test('unreviewed financial resolutions cannot turn the phase green', () => {
  const data = fresh();
  data.decisions.decisions[0].status = 'RESOLVED';
  data.decisions.decisions[0].resolution = 'Assume a bearer';
  data.decisions.decisions[0].reviewed_by_roles = [];
  data.decisions.decisions[0].evidence = [];
  assert.equal(evaluateGate(data).result, 'INVALID');
});

test('configurability needs explicit user evidence and a current verified contract, not a mode flag', () => {
  const data = fresh();
  data.phase.financial_baseline = {mode:'USER_APPROVED_CONFIGURABLE_RULES',user_decision:'evidence/missing.json',contract:'financial-configuration.json',verification:'evidence/missing.json'};
  assert.equal(configurableFinancialBaseline(data),false);
  assert.equal(evaluateGate(data).criteria[2].result,'OPEN');
  data.phase.financial_baseline.user_decision='../outside.json';
  assert.equal(configurableFinancialBaseline(data),false);
});

test('a verified configurable design closes only finance; stale files or missing user authority invalidate it', () => {
  const data=loadRegister();
  assert.equal(configurableFinancialBaseline(data),true);
  delete data.phase.deferred_local_checks;
  data.phase.verification_results.find(result=>result.id==='mobile-ios').result='UNAVAILABLE';
  assert.equal(evaluateGate(data).criteria[2].result,'PASSED');
  assert.equal(evaluateGate(data).next_phase_allowed,false);
  const base=mkdtempSync(join(tmpdir(),'planext4u-finance-baseline-'));
  const files=[data.phase.financial_baseline.user_decision,data.phase.financial_baseline.contract,data.phase.financial_baseline.verification,'golden-cases.json'];
  mkdirSync(join(base,'evidence'));
  try {
    for(const path of files) writeFileSync(join(base,path),readFileSync(join(registerRoot,path)));
    assert.equal(configurableFinancialBaseline(data,base),true);
    const path=data.phase.financial_baseline.contract;
    const contract=JSON.parse(readFileSync(join(base,path),'utf8'));
    contract.reference_profiles[0].values['wallet.expiry_days']=31;
    writeFileSync(join(base,path),JSON.stringify(contract));
    assert.equal(configurableFinancialBaseline(data,base),false);
    writeFileSync(join(base,path),readFileSync(join(registerRoot,path)));
    const authority=data.phase.financial_baseline.user_decision;
    writeFileSync(join(base,authority),JSON.stringify({kind:'direct_user_decisions',decisions:[]}));
    assert.equal(configurableFinancialBaseline(data,base),false);
  } finally {
    for(const path of files) unlinkSync(join(base,path));
    rmdirSync(join(base,'evidence'));rmdirSync(base);
  }
});
test('calendar dates are valid dates and not invented strings', () => {
  const data = fresh();
  data.dependencies.dependencies[0].target_date = '2026-02-30';
  assert.ok(validateRegister(data).some(error => error.includes('invalid target date')));
});
test('the gate can pass once every criterion has reviewed execution evidence', () => {
  const base = mkdtempSync(join(tmpdir(), 'planext4u-phase0-test-'));
  const files = [];
  try {
    const data = fresh();
    mkdirSync(join(base, 'evidence'));
    const reviewPath = 'evidence/review.txt';
    writeFileSync(join(base, reviewPath), 'Synthetic test review. Not actual acceptance evidence.');
    files.push(reviewPath);
    const review = {by_roles: ['Product','Finance','QA'], date: '2026-10-24', summary: 'Synthetic test only', evidence: [reviewPath]};
    data.phase.scope_review = review;
    data.phase.readiness_review = review;
    data.catalogues.review = review;
    for (const source of data.sources.sources) {
      if (source.kind === 'audio') Object.assign(source, {review_status: 'VERIFIED', verified_by_role: 'Product', verified_date: '2026-10-24', segments: [{start_seconds: 0, end_seconds: 1, summary: 'Synthetic test scope', requirement_ids: ['P4U-ID-001']}]});
      if (source.kind === 'original_requirement') source.resolution = 'Synthetic replacement decision for gate test';
    }
    for (const decision of data.decisions.decisions) Object.assign(decision, {status: 'RESOLVED', resolution: 'Synthetic test decision', reviewed_by_roles: ['Product','Finance'], reviewed_date: '2026-10-24', evidence: [reviewPath]});
    for (const value of data.golden.cases) Object.assign(value, {policy_status: 'APPROVED', review});
    for (const dependency of data.dependencies.dependencies.filter(value => value.needed_before_phase === 0)) Object.assign(dependency, {status: 'VERIFIED', evidence: [reviewPath]});
    data.phase.verification_results = data.phase.required_local_checks.map(id => {
      const path = `evidence/${id}.json`;
      writeFileSync(join(base, path), JSON.stringify({check_id: id, result: 'PASSED', command: 'synthetic gate test', observed: 'Synthetic pass', environment: 'unit-test', executed_at: '2026-10-24T00:00:00Z', exit_code: 0, source_revisions: data.phase.source_revisions}));
      files.push(path);
      return {id, result: 'PASSED', evidence: path};
    });
    assert.equal(evaluateGate(data, base).next_phase_allowed, true);
    const ios=data.phase.verification_results.find(result=>result.id==='mobile-ios');
    ios.result='UNAVAILABLE';
    writeFileSync(join(base,'evidence/ios-deferral.json'),JSON.stringify({kind:'direct_user_decisions',decisions:[{id:'TEST-IOS',choice:'LOCAL_PHASE0_WITH_IOS_PENDING',checks:['mobile-ios'],answer_verbatim:'Synthetic test deferral, not actual user evidence'}]}));
    files.push('evidence/ios-deferral.json');
    data.phase.deferred_local_checks=[{id:'mobile-ios',status:'DEFERRED_BY_USER',scope:'LOCAL_PHASE_0_ONLY',release_blocking:true,needed_by:'before release',evidence:'evidence/ios-deferral.json',decision_id:'TEST-IOS'}];
    const localGate=evaluateGate(data,base);
    assert.equal(localGate.result,'PASSED_WITH_DEFERRALS');
    assert.equal(localGate.next_phase_allowed,true);
    assert.equal(localGate.release_ready,false);
    assert.equal(localGate.deferred_checks[0].result,'UNAVAILABLE');
    ios.result='FAILED';assert.equal(evaluateGate(data,base).result,'INVALID');
    ios.result='PASSED';delete data.phase.deferred_local_checks;
    data.phase.required_local_checks.pop();
    assert.equal(evaluateGate(data, base).result, 'INVALID');
    data.phase.required_local_checks.push('local-role-smoke');
    const receipt = data.phase.verification_results[0].evidence;
    writeFileSync(join(base, receipt), JSON.stringify({check_id: 'backend-linux', result: 'PASSED', command: 'old build', observed: 'old revision', environment: 'unit-test', executed_at: '2026-10-24T00:00:00Z', exit_code: 0, source_revisions: {'planext4u-backend': 'a'.repeat(40)}}));
    assert.equal(evaluateGate(data, base).result, 'INVALID');
  } finally {
    for (const path of files) unlinkSync(join(base, path));
    rmdirSync(join(base, 'evidence'));
    rmdirSync(base);
  }
});
test('supplied financial arithmetic preserves independent customer and supplier totals', () => {
  const sample = fresh().golden.cases.find(value => value.id === 'GOLDEN-SAMPLE');
  const {inputs: i, observed: o} = sample;
  assert.equal(i.mrp_minor - i.discount_minor, o.final_item_minor);
  assert.equal(i.mrp_minor * i.commission_basis_points / 10000, o.commission_minor);
  assert.equal(i.platform_fee_minor * i.platform_fee_gst_basis_points / 10000, o.platform_fee_gst_minor);
  assert.equal(i.mrp_minor - o.commission_minor, o.supplier_payable_minor);
  assert.equal(o.final_item_minor + i.platform_fee_minor + o.platform_fee_gst_minor, o.customer_total_minor);
  assert.equal(o.final_item_minor * i.product_tax_basis_points / 10000, o.labelled_product_tax_minor);
  assert.notEqual(o.customer_total_minor + o.labelled_product_tax_minor, o.customer_total_minor);
});
test('tax and discount alternatives remain distinguishable pending Finance review', () => {
  const cases = fresh().golden.cases;
  const discount = cases.find(value => value.id === 'GOLDEN-DISCOUNT-BEARER');
  assert.equal(discount.alternatives.platform_funded_supplier_minor - discount.inputs.discount_minor, discount.alternatives.supplier_funded_supplier_minor);
  const tax = cases.find(value => value.id === 'GOLDEN-PRODUCT-TAX');
  assert.equal(Math.floor(tax.inputs.item_minor * 1800 / 11800), tax.alternatives.embedded_tax_minor_if_rounded_down);
  assert.notEqual(tax.alternatives.additional_tax_minor, tax.alternatives.embedded_tax_minor_if_rounded_down);
  const wallet = cases.find(value => value.id === 'GOLDEN-WALLET');
  assert.equal(wallet.documented_conversion.minor_units, 100);
  const rider = cases.find(value => value.id === 'GOLDEN-RIDER');
  const i = rider.synthetic_tariff_inputs;
  assert.equal(i.base_minor + i.distance_meters / 1000 * i.per_km_minor + i.surge_minor + i.incentive_minor + i.tips_minor, rider.synthetic_expected.gross_before_agreed_deductions_minor);
  assert.equal(i.tips_minor, rider.synthetic_expected.tips_pass_through_minor);
});
