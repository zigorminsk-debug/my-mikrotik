// Regression tests for the apply UI and stale asynchronous responses.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
function between(start,end) {
  const a=html.indexOf(start), b=html.indexOf(end,a);
  assert(a>=0 && b>a, `missing ${start}`);
  return html.slice(a,b);
}
function setup() {
  const names=['btn-copy','btn-download','btn-apply','apply-reason','build-ok','build-error','host','geo-unknown-status'];
  const el=Object.fromEntries(names.map(id=>[id,{disabled:false,style:{display:'none'},textContent:'',innerHTML:'', value:'192.0.2.1', addEventListener(type,fn){this.handler=fn}}]));
  let resolveFetch, requests=0, geo=false, radiusOn=false, state='', lastRequest=null;
  const ctx=vm.createContext({
    $:id=>el[id], chk:id=>(id==='chk-geoNat'&&geo)||(id==='chk-pppRadius'&&radiusOn)||(id==='geo-unknown-status'&&!!el[id].checked), applyInProgress:false, Blob, currentGeoPreview:null, currentRadiusPreview:null,
    currentScript:':put 1', connectedKey:'key', lastInfo:{protocol:'api',routeros:'7.25.1'}, interfacesReady:true, previewSeq:1, previewTimer:null,
    connectionKey:()=> 'key', connPayload:()=>({host:'192.0.2.1'}),
    setScriptState:s=>{state=s}, protoLabel:p=>p, window:{confirm:()=>true},
    clearTimeout(){}, setTimeout(){return 1}, preview(){}, clearLiveLANPlan(){}, clearGeoCheck(){}, invalidateConnection(){throw Error('invalidated')},
    fetch:(url,opts)=>{ requests++; lastRequest={url,body:JSON.parse(opts.body)}; return new Promise(resolve=>{resolveFetch=resolve}); },
  });
  vm.runInContext(between('  function geoApplyIssue()', '  // ------------------------------------------------------------------\n  // предпросмотр'),ctx);
  vm.runInContext(between('  function schedulePreview()', '  function preview()'),ctx);
  vm.runInContext(between('  $("btn-apply").addEventListener("click"', '  // Desktop device list:'),ctx);
  return {ctx,el,resolve:res=>resolveFetch(res), selectGeo:()=>{geo=true}, selectRadius:()=>{radiusOn=true}, requests:()=>requests, request:()=>lastRequest, state:()=>state};
}
const settle=()=>new Promise(r=>setImmediate(r));
(async()=>{
  const h=setup(); h.ctx.setActions(true); h.el['btn-apply'].handler.call(h.el['btn-apply']);
  assert.equal(h.requests(),1); assert.equal(h.request().url,'/api/apply'); assert.equal(h.request().body.script,':put 1'); assert.equal(h.el['btn-apply'].disabled,true);
  h.el['btn-apply'].handler.call(h.el['btn-apply']); assert.equal(h.requests(),1);
  h.selectGeo(); h.ctx.schedulePreview(); h.ctx.currentScript='# MTI-GEONAT-IMPORT-ONLY'; h.ctx.setActions(true);
  assert.equal(h.el['btn-apply'].disabled,true);
  h.resolve({json:()=>Promise.resolve({ok:true,protocol:'api',script:'mti-apply'})});
  await settle(); await settle();
  assert.match(h.el['build-ok'].textContent,/прежним настройкам/);
  assert.notEqual(h.state(),'применено');
  assert.equal(h.el['btn-apply'].disabled,true); assert.match(h.el['apply-reason'].textContent,/обновите предпросмотр/);
  h.ctx.schedulePreview(); assert.equal(h.el['build-ok'].style.display,'none');

  const failure=setup(); failure.el['btn-apply'].handler.call(failure.el['btn-apply']);
  failure.ctx.schedulePreview(); failure.ctx.currentScript='new script'; failure.ctx.setActions(true);
  failure.resolve({json:()=>Promise.resolve({ok:false,error:'lost'})});
  await settle(); await settle();
  assert.match(failure.el['build-error'].textContent,/частично/);
  assert.match(failure.el['build-error'].textContent,/предыдущему запросу/);
  assert.notEqual(failure.state(),'ошибка применения');

  const guarded=setup(); guarded.selectGeo(); guarded.ctx.currentScript='# MTI-GEONAT-GUARDED-APPLY\n:put 1';
  guarded.ctx.currentGeoPreview={device:guarded.ctx.lastInfo,interfaces:[{name:'ether1'}],geoNat:{destination:'192.168.88.5',port:'3389',wan:'ether1'},generatedAt:'2026-11-13T00:00:00Z'};
  guarded.ctx.setActions(true); assert.equal(guarded.el['btn-apply'].disabled,false);
  guarded.el['btn-apply'].handler.call(guarded.el['btn-apply']); assert.equal(guarded.request().url,'/api/geonat/apply');
  assert.equal(guarded.request().body.geoNat.destination,'192.168.88.5'); assert.equal(guarded.request().body.script,guarded.ctx.currentScript);
  guarded.resolve({json:()=>Promise.resolve({ok:true,protocol:'api',script:'mti-apply'})});
  await settle(); await settle(); assert.equal(guarded.state(),'проверить правила'); assert.match(guarded.el['build-ok'].textContent,/не проверяет реальную защиту/);
  guarded.ctx.currentScript='# MTI-GEONAT-GUARDED-APPLY'+'x'.repeat(65537); guarded.ctx.setActions(true); assert.equal(guarded.el['btn-apply'].disabled,true);
  assert.match(guarded.el['apply-reason'].textContent,/64 КиБ/);
  guarded.ctx.currentScript='# MTI-GEONAT-GUARDED-APPLY'; guarded.ctx.lastInfo.protocol='mac'; guarded.ctx.setActions(true); assert.equal(guarded.el['btn-apply'].disabled,true);

  for (const [stage, safe] of [['preflight', true], ['unknown', false], [undefined, false]]) {
    const f=setup(); f.selectGeo(); f.ctx.currentScript='# MTI-GEONAT-GUARDED-APPLY';
    f.ctx.currentGeoPreview={device:f.ctx.lastInfo,interfaces:[],geoNat:{destination:'192.168.88.50',port:'3389',wan:'ether1'},generatedAt:'2026-11-13T00:00:00Z'};
    f.el['btn-apply'].handler.call(f.el['btn-apply']); f.resolve({json:()=>Promise.resolve({ok:false,error:'not found',stage})});
    await settle(); await settle();
    assert.equal(/настройки этим запросом не изменены/.test(f.el['build-error'].textContent),safe);
    assert.equal(/могли примениться частично/.test(f.el['build-error'].textContent),!safe);
  }

  const unknown=setup(); unknown.selectGeo(); unknown.ctx.currentScript='# MTI-GEONAT-GUARDED-APPLY';
  unknown.ctx.currentGeoPreview={device:unknown.ctx.lastInfo,interfaces:[],geoNat:{destination:'192.168.88.50',port:'3389',wan:'ether1'},generatedAt:'2026-11-13T00:00:00Z'};
  unknown.el['geo-unknown-status'].checked=true;
  let warning=''; unknown.ctx.window.confirm=msg=>{warning=msg;return true};
  unknown.el['btn-apply'].handler.call(unknown.el['btn-apply']);
  assert.equal(unknown.request().body.allowUnknownNatStatus,true); assert.match(warning,/без подтверждения включённого состояния/);
  unknown.resolve({json:()=>Promise.resolve({ok:true,protocol:'api'})}); await settle();await settle();
  assert.match(unknown.el['build-ok'].textContent,/могло не быть подтверждено/);
  unknown.ctx.schedulePreview();assert.equal(unknown.el['geo-unknown-status'].checked,false);

  const radius=setup(); radius.selectRadius(); radius.ctx.currentScript='# MTI-PPP-RADIUS-GUARDED-APPLY\n:put 1';
  radius.ctx.currentRadiusPreview={device:radius.ctx.lastInfo,interfaces:[],pppRadius:{address:'192.0.2.10',secret:'private'},generatedAt:'2026-11-13T00:00:00Z'};
  radius.ctx.setActions(true); assert.equal(radius.el['btn-apply'].disabled,false);
  radius.el['btn-apply'].handler.call(radius.el['btn-apply']); assert.equal(radius.requests(),1); assert.equal(radius.request().url,'/api/ppp-radius/apply');
  assert.equal(radius.request().body.pppRadius.address,'192.0.2.10'); radius.el['btn-apply'].handler.call(radius.el['btn-apply']);assert.equal(radius.requests(),1);
  radius.resolve({json:()=>Promise.resolve({ok:true,protocol:'api'})}); await settle(); await settle();
  assert.equal(radius.state(),'проверить авторизацию'); assert.match(radius.el['build-ok'].textContent,/не подтверждает работу FreeRADIUS/);
  radius.ctx.lastInfo.protocol='mac';radius.ctx.setActions(true);assert.equal(radius.el['btn-apply'].disabled,true);assert.match(radius.el['apply-reason'].textContent,/API\/REST/);
  const legacy=setup();legacy.ctx.currentScript='# MTI-PPP-RADIUS-PREVIEW-ONLY';legacy.ctx.setActions(true);assert.equal(legacy.el['btn-apply'].disabled,true);
  legacy.el['btn-apply'].handler.call(legacy.el['btn-apply']);assert.equal(legacy.requests(),0);

  const normal=setup(); normal.el['btn-apply'].handler.call(normal.el['btn-apply']);
  normal.resolve({json:()=>Promise.resolve({ok:true,protocol:'api',script:'mti-apply'})});
  await settle(); await settle(); assert.equal(normal.state(),'применено');
  normal.ctx.schedulePreview(); assert.equal(normal.el['build-ok'].style.display,'none');
  assert.match(html,/button\.ok:disabled/);
  console.log('Apply UI race and safety guards: OK');
})().catch(e=>{console.error(e);process.exitCode=1});

// Exact match of connection host and destination merits a warning, but a
// mismatch cannot prove that a destination is a different physical device.
const warningEls = {'geo-destination':{value:'192.168.88.1'},host:{value:'192.168.88.1'},'geo-target-warning':{style:{display:'none'}}};
const warningCtx = vm.createContext({$:id=>warningEls[id]});
vm.runInContext(between('  function updateGeoTargetWarning()', '  ["host", "geo-destination"].forEach'),warningCtx);
warningCtx.updateGeoTargetWarning(); assert.equal(warningEls['geo-target-warning'].style.display,'block');
warningEls['geo-destination'].value='192.168.88.10'; warningCtx.updateGeoTargetWarning(); assert.equal(warningEls['geo-target-warning'].style.display,'none');
