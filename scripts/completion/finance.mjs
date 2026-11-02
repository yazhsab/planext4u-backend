import {readFileSync, writeFileSync} from 'node:fs';
import {createHash} from 'node:crypto';
import {resolve} from 'node:path';
import {fileURLToPath} from 'node:url';

const root = fileURLToPath(new URL('../../', import.meta.url));
const base = resolve(root, 'docs/completion/phase-0');
const nonempty = value => typeof value === 'string' && value.trim().length > 0;
export const checksum = value => createHash('sha256').update(value).digest('hex');

// Phase 0 contract validation only. This tool never publishes a runtime policy.
export function validateFinancialValues(contract, values) {
  const errors = [];
  const settings = contract.settings ?? [];
  const allowed = new Set(settings.map(field => field.key));
  if (!values || typeof values !== 'object' || Array.isArray(values)) return ['Financial values must be an object.'];
  for (const key of Object.keys(values)) if (!allowed.has(key)) errors.push(`Unknown financial setting ${key}.`);
  for (const field of settings) {
    const value = values[field.key];
    if (!Object.hasOwn(values, field.key)) {errors.push(`Missing financial setting ${field.key}.`); continue;}
    if (value === null && field.nullable === true) continue;
    let valid = false;
    if (field.type === 'integer') valid = Number.isSafeInteger(value) && value >= field.minimum && value <= field.maximum;
    if (field.type === 'enum') valid = field.values?.includes(value) === true;
    if (field.type === 'boolean') valid = typeof value === 'boolean';
    if (field.type === 'string') valid = nonempty(value) && value.length <= field.maximum_length;
    if (!valid) errors.push(`Invalid financial setting ${field.key}.`);
  }
  const bearer = values['discount.bearer'], share = values['discount.vendor_share_basis_points'];
  if ((bearer === 'PLATFORM' && share !== 0) || (bearer === 'VENDOR' && share !== 10000) ||
      (bearer === 'SPLIT' && !(share > 0 && share < 10000))) errors.push('Discount bearer and vendor share disagree.');
  if ((values['wallet.expiry_mode'] === 'NO_EXPIRY' && values['wallet.expiry_days'] !== null) ||
      (values['wallet.expiry_mode'] === 'DAYS_AFTER_EARN' && !Number.isSafeInteger(values['wallet.expiry_days']))) errors.push('Wallet expiry mode and days disagree.');
  for (const [mode, rate] of [['tax.product_mode','tax.product_rate_basis_points'],['tax.platform_fee_mode','tax.platform_fee_rate_basis_points'],['tax.commission_mode','tax.commission_rate_basis_points']]) {
    if (['EXEMPT','NONE'].includes(values[mode]) && values[rate] !== 0) errors.push(`${mode} cannot carry a nonzero tax rate.`);
  }
  if ((values['rider.surge_mode'] === 'FLAT' && values['rider.surge_basis_points'] !== 0) ||
      (values['rider.surge_mode'] === 'DISTANCE_PERCENT' && values['rider.surge_minor'] !== 0)) errors.push('Only one rider surge calculation may apply.');
  return errors;
}

export function validateFinancialContract(contract) {
  const errors = [];
  if (contract.schema_version !== 1 || contract.decision !== 'USER_APPROVED_CONFIGURABLE_RULES' ||
      contract.status !== 'PHASE_0_CONTRACT_ONLY' || contract.runtime_connected !== false) errors.push('Invalid Phase 0 financial contract boundary.');
  if (!Array.isArray(contract.settings) || contract.settings.length === 0) return [...errors, 'Financial settings are missing.'];
  const keys = contract.settings.map(field => field.key);
  if (new Set(keys).size !== keys.length) errors.push('Duplicate financial settings.');
  for (const field of contract.settings) {
    if (!/^[a-z_]+\.[a-z_]+$/.test(field.key) || !nonempty(field.owner_service) ||
        !Number.isInteger(field.delivery_phase) || field.delivery_phase < 1 || field.delivery_phase > 10 ||
        field.required_before_publication !== true || Object.hasOwn(field,'default')) errors.push(`Invalid financial field definition ${field.key}.`);
    if (!['integer','enum','boolean','string'].includes(field.type)) errors.push(`Unsupported financial field type ${field.key}.`);
    if (field.type === 'integer' && !(Number.isSafeInteger(field.minimum) && Number.isSafeInteger(field.maximum) && field.minimum >= 0 && field.maximum >= field.minimum)) errors.push(`Invalid numeric bounds ${field.key}.`);
    if (field.type === 'enum' && !(field.values?.length > 0 && field.values.every(nonempty) && new Set(field.values).size === field.values.length)) errors.push(`Invalid enum ${field.key}.`);
    if (field.type === 'string' && !(Number.isSafeInteger(field.maximum_length) && field.maximum_length > 0)) errors.push(`Invalid string limit ${field.key}.`);
  }
  if (!Array.isArray(contract.publication_requirements) || contract.publication_requirements.length === 0) errors.push('Publication requirements are missing.');
  const resolution=contract.rule_resolution;
  if (JSON.stringify(resolution?.commission_first_match)!==JSON.stringify(['PRODUCT_OVERRIDE','VENDOR_OVERRIDE','VENDOR_PLAN','CATEGORY_RATE','PLATFORM_DEFAULT'])) errors.push('Preserve the source five-level commission cascade.');
  const redemptionScopes=resolution?.wallet_redemption?.supported_scopes;
  if (!Array.isArray(redemptionScopes) || new Set(redemptionScopes).size!==6 ||
      !['PLATFORM','CATEGORY','VENDOR_PLAN','VENDOR','PRODUCT','CAMPAIGN'].every(scope=>redemptionScopes.includes(scope)) ||
      resolution?.wallet_redemption?.precedence!=='EXPLICIT_PUBLISHED_ORDER') errors.push('Redemption requires category/plan/vendor/product/campaign scope and explicit published precedence.');
  if (!Array.isArray(contract.reference_profiles) || contract.reference_profiles.length === 0) errors.push('Reference profiles are missing.');
  for (const profile of contract.reference_profiles ?? []) {
    if (!nonempty(profile.id) || !nonempty(profile.version) || profile.purpose !== 'REFERENCE_FIXTURE' || profile.publication_state !== 'DRAFT' ||
        !nonempty(profile.scope?.tenant) || !/^[A-Z]{2}$/.test(profile.scope?.country) || !/^[A-Z]{3}$/.test(profile.scope?.currency)) errors.push('Reference profiles must remain scoped unpublished fixtures.');
    errors.push(...validateFinancialValues(contract, profile.values));
  }
  return errors;
}

// Independent source arithmetic, not a replacement checkout/settlement engine.
export function validateFinancialReferences(contract, golden) {
  const errors = [];
  const byID = new Map(golden.cases.map(value => [value.id, value]));
  if (contract.golden_case_ids?.length !== 6 || new Set(contract.golden_case_ids).size !== 6 || contract.golden_case_ids.some(id => !byID.has(id))) return ['Map all six financial reference cases.'];
  const check = (condition, description) => {if (!condition) errors.push(description);};
  const n = value => {if (!Number.isSafeInteger(value) || value < 0) throw new Error('Reference amounts must be nonnegative safe integers.'); return BigInt(value);};
  try {
    const sample = byID.get('GOLDEN-SAMPLE'), i = sample.inputs, o = sample.observed;
    check(n(i.mrp_minor)-n(i.discount_minor) === n(o.final_item_minor), 'Workbook discounted item mismatch.');
    check(n(i.mrp_minor)*n(i.commission_basis_points)/10000n === n(o.commission_minor), 'Workbook commission mismatch.');
    check(n(i.platform_fee_minor)*n(i.platform_fee_gst_basis_points)/10000n === n(o.platform_fee_gst_minor), 'Workbook fee tax mismatch.');
    check(n(i.mrp_minor)-n(o.commission_minor) === n(o.supplier_payable_minor), 'Workbook supplier payable mismatch.');
    check(n(o.final_item_minor)+n(i.platform_fee_minor)+n(o.platform_fee_gst_minor) === n(o.customer_total_minor), 'Workbook customer total mismatch.');
    check(n(o.final_item_minor)*n(i.product_tax_basis_points)/10000n === n(o.labelled_product_tax_minor), 'Workbook informational tax mismatch.');
    const values = contract.reference_profiles[0].values;
    check(values['commission.base'] === 'MRP' && values['commission.rate_basis_points'] === i.commission_basis_points &&
      values['discount.bearer'] === 'PLATFORM' && values['tax.product_mode'] === 'INFORMATIONAL' && values['tax.product_base'] === 'NET_AFTER_DISCOUNT' && values['tax.product_rate_basis_points'] === i.product_tax_basis_points &&
      values['tax.platform_fee_mode'] === 'EXCLUSIVE' && values['tax.platform_fee_rate_basis_points'] === i.platform_fee_gst_basis_points && values['tax.commission_mode'] === 'NONE', 'Workbook reference profile must preserve supplied arithmetic.');
    const discount = byID.get('GOLDEN-DISCOUNT-BEARER');
    check(n(discount.alternatives.platform_funded_supplier_minor)-n(discount.inputs.discount_minor) === n(discount.alternatives.supplier_funded_supplier_minor), 'Discount alternatives mismatch.');
    const tax = byID.get('GOLDEN-PRODUCT-TAX');
    check(n(tax.inputs.item_minor)*n(tax.inputs.rate_basis_points)/10000n === n(tax.alternatives.additional_tax_minor) &&
      n(tax.inputs.item_minor)*n(tax.inputs.rate_basis_points)/(10000n+n(tax.inputs.rate_basis_points)) === n(tax.alternatives.embedded_tax_minor_if_rounded_down), 'Tax alternatives mismatch.');
    const wallet = byID.get('GOLDEN-WALLET').characterization;
    check(values['wallet.point_value_minor'] === wallet.point_value_minor && n(wallet.capped_points)*n(wallet.point_value_minor) === n(wallet.wallet_applied_minor) && wallet.capped_points <= wallet.requested_points, 'Wallet conversion/cap reference mismatch.');
    const supplier = byID.get('GOLDEN-SUPPLIER-SETTLEMENT').characterization;
    check(n(supplier.gross_minor)-n(supplier.commission_minor)-n(supplier.commission_tax_minor) === n(supplier.net_minor), 'Captured settlement reference mismatch.');
    const rider = byID.get('GOLDEN-RIDER'), r = rider.synthetic_tariff_inputs;
    check(n(r.base_minor)+n(r.distance_meters)*n(r.per_km_minor)/1000n+n(r.surge_minor)+n(r.incentive_minor)+n(r.tips_minor) === n(rider.synthetic_expected.gross_before_agreed_deductions_minor), 'Synthetic rider reference mismatch.');
    check(r.tips_minor === rider.synthetic_expected.tips_pass_through_minor && values['rider.tips_policy'] === 'FULL_PASS_THROUGH_EXCLUDED_FROM_DEDUCTIONS', 'Tips must pass through in full.');
  } catch (error) {errors.push(`Invalid financial reference: ${error.message}`);}
  return errors;
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  try {
    const command = process.argv[2] ?? 'validate';
    if (!['validate','verify'].includes(command)) throw new Error('Usage: node scripts/completion/finance.mjs validate|verify');
    const contractBytes = readFileSync(resolve(base,'financial-configuration.json'));
    const goldenBytes = readFileSync(resolve(base,'golden-cases.json'));
    const contract = JSON.parse(contractBytes), golden = JSON.parse(goldenBytes);
    const errors = [...validateFinancialContract(contract),...validateFinancialReferences(contract,golden)];
    if (errors.length) throw new Error(errors.join('\n'));
    if (command === 'verify') {
      const phase = JSON.parse(readFileSync(resolve(base,'phase-0.json'),'utf8'));
      writeFileSync(resolve(base,'evidence/financial-configuration-2026-10-24.json'),JSON.stringify({
        check_id:'financial-policy-contract', result:'PASSED', command:'node scripts/completion/finance.mjs verify',
        observed:`Validated ${contract.settings.length} required configurable fields, unpublished reference profile and all six independent financial arithmetic cases. No runtime policy activated.`,
        environment:`Node ${process.version}; local Phase 0 design contract`, executed_at:new Date().toISOString(), exit_code:0,
        source_revisions:phase.source_revisions, contract_sha256:checksum(contractBytes), references_sha256:checksum(goldenBytes), tool_sha256:checksum(readFileSync(fileURLToPath(import.meta.url))),
        case_ids:contract.golden_case_ids, runtime_connected:false,
        limitations:['Configuration design/arithmetic verification only; runtime owner-service/admin integration remains Phases 3–5. No statutory classification, live tariff or external Finance sign-off claimed.'],
      },null,2)+'\n');
    }
    console.log(`Financial contract valid: ${contract.settings.length} settings, six reference cases; runtime integration remains planned.`);
  } catch (error) {console.error(error.message); process.exitCode=1;}
}
