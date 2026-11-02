import {test} from 'node:test';
import assert from 'node:assert/strict';
import {readFileSync} from 'node:fs';
import {validateFinancialValues,validateFinancialContract,validateFinancialReferences} from './finance.mjs';

const fixture = () => JSON.parse(readFileSync(new URL('../../docs/completion/phase-0/financial-configuration.json',import.meta.url),'utf8'));
const golden = () => JSON.parse(readFileSync(new URL('../../docs/completion/phase-0/golden-cases.json',import.meta.url),'utf8'));

test('financial configuration preserves the workbook and six independently checked references',()=>{
  const contract=fixture();
  assert.deepEqual(validateFinancialContract(contract),[]);
  assert.deepEqual(validateFinancialReferences(contract,golden()),[]);
  const changed=golden();
  changed.cases.find(value=>value.id==='GOLDEN-SAMPLE').observed.supplier_payable_minor-=700;
  assert.ok(validateFinancialReferences(contract,changed).some(error=>error.includes('supplier payable')));
});

test('missing, misspelled, unsafe and out-of-range financial fields cannot fall back silently',()=>{
  const contract=fixture();
  for(const change of [v=>delete v['wallet.mode'],v=>{v['wallet.mode']='HYBRID';},v=>{v['rider.base_minor']=-1;},v=>{v['rider.base_minor']=Number.MAX_SAFE_INTEGER+1;},v=>{v['commission.rate_basis_points']=10001;},v=>{v['wallet.maximum_points_per_order']=0.5;},v=>{v['tax.rate']=1800;}]) {
    const values=structuredClone(contract.reference_profiles[0].values);change(values);
    assert.ok(validateFinancialValues(contract,values).length>0);
  }
});

test('wallet expiry, split discount and tax/surge choices require consistent explicit fields',()=>{
  const contract=fixture();
  for(const change of [v=>{v['wallet.expiry_mode']='NO_EXPIRY';},v=>{v['discount.bearer']='SPLIT';},v=>{v['tax.commission_rate_basis_points']=1800;},v=>{v['rider.surge_mode']='DISTANCE_PERCENT';}]) {
    const values=structuredClone(contract.reference_profiles[0].values);change(values);
    assert.ok(validateFinancialValues(contract,values).length>0);
  }
  const values=structuredClone(contract.reference_profiles[0].values);
  Object.assign(values,{'wallet.mode':'POINTS_ONLY','wallet.expiry_mode':'NO_EXPIRY','wallet.expiry_days':null,
    'discount.bearer':'SPLIT','discount.vendor_share_basis_points':4000,'tax.product_mode':'INCLUSIVE',
    'tax.commission_mode':'DEDUCT_FROM_PAYABLE','tax.commission_rate_basis_points':1800,
    'rider.surge_mode':'DISTANCE_PERCENT','rider.surge_minor':0,'rider.surge_basis_points':2500});
  assert.deepEqual(validateFinancialValues(contract,values),[]);
});

test('reference fixtures cannot be relabelled as published or connected runtime policies',()=>{
  for(const change of [c=>{c.runtime_connected=true;},c=>{c.reference_profiles[0].publication_state='PUBLISHED';},c=>{c.reference_profiles[0].purpose='LAUNCH_CONFIGURATION';},c=>{c.settings[0].default='PLATFORM';}]) {
    const contract=fixture();change(contract);assert.ok(validateFinancialContract(contract).length>0);
  }
});

test('a partial commission cascade or missing category redemption scope cannot pass the financial contract',()=>{
  const contract=fixture();
  contract.rule_resolution.commission_first_match.splice(3,1);
  assert.ok(validateFinancialContract(contract).some(error=>error.includes('five-level')));
  const redemption=fixture();
  redemption.rule_resolution.wallet_redemption.supported_scopes=redemption.rule_resolution.wallet_redemption.supported_scopes.filter(scope=>scope!=='CATEGORY');
  assert.ok(validateFinancialContract(redemption).some(error=>error.includes('Redemption requires')));
});

test('franchise reference allocations divide one percentage pool and conserve remainder paise',()=>{
  const reference=JSON.parse(readFileSync(new URL('../../docs/completion/phase-0/franchise-profit-reference.json',import.meta.url),'utf8'));
  assert.equal(reference.purpose,'REFERENCE_FIXTURE');assert.equal(reference.publication_state,'DRAFT');
  for(const value of reference.cases) {
    const pool=BigInt(value.net_profit_minor)*BigInt(value.share_basis_points)/10000n;
    assert.equal(pool,BigInt(value.pool_minor));
    assert.equal(new Set(value.eligible_recipients).size,value.eligible_recipients.length);
    assert.equal(value.allocations_minor.length,value.eligible_recipients.length);
    const divisor=BigInt(value.eligible_recipients.length), base=pool/divisor, remainder=pool%divisor;
    value.allocations_minor.forEach((amount,index)=>assert.equal(BigInt(amount),base+(BigInt(index)<remainder?1n:0n)));
    assert.equal(value.allocations_minor.reduce((sum,amount)=>sum+BigInt(amount),0n)+BigInt(value.retained_minor),pool);
    assert.notEqual(pool*divisor,pool); // A full percentage grant per member over-allocates this fixture.
  }
});
