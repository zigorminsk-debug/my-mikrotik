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
  const names=['btn-copy','btn-download','btn-apply','apply-reason','build-ok','build-error','host'];
  const el=Object.fromEntries(names.map(id=>[id,{disabled:false,style:{display:'none'},textContent:'',innerHTML:'', value:'192.0.2.1', addEventListener(type,fn){this.handler=fn}}]));
  let resolveFetch, requests=0, geo=false, state='';
  const ctx=vm.createContext({
    $:id=>el[id], chk:id=>id==='chk-geoNat'&&geo, applyInProgress:false,
    currentScript:':put 1', connectedKey:'key', lastInfo:{protocol:'api'}, interfacesReady:true, previewSeq:1, previewTimer:null,
    connectionKey:()=> 'key', connPayload:()=>({host:'192.0.2.1'}),
    setScriptState:s=>{state=s}, protoLabel:p=>p, window:{confirm:()=>true},
    clearTimeout(){}, setTimeout(){return 1}, preview(){}, invalidateConnection(){throw Error('invalidated')},
    fetch:(url,opts)=>{ requests++; assert.equal(url,'/api/apply'); assert.equal(JSON.parse(opts.body).script,':put 1'); return new Promise(resolve=>{resolveFetch=resolve}); },
  });
  vm.runInContext(between('  function setActions(enabled)', '  // ------------------------------------------------------------------\n  // предпросмотр'),ctx);
  vm.runInContext(between('  function schedulePreview()', '  function preview()'),ctx);
  vm.runInContext(between('  $("btn-apply").addEventListener("click"', '  loadPrefs();'),ctx);
  return {ctx,el,resolve:res=>resolveFetch(res), selectGeo:()=>{geo=true}, requests:()=>requests, state:()=>state};
}
const settle=()=>new Promise(r=>setImmediate(r));
(async()=>{
  const h=setup(); h.ctx.setActions(true); h.el['btn-apply'].handler.call(h.el['btn-apply']);
  assert.equal(h.requests(),1); assert.equal(h.el['btn-apply'].disabled,true);
  h.el['btn-apply'].handler.call(h.el['btn-apply']); assert.equal(h.requests(),1);
  h.selectGeo(); h.ctx.schedulePreview(); h.ctx.currentScript='# MTI-GEONAT-IMPORT-ONLY'; h.ctx.setActions(true);
  assert.equal(h.el['btn-apply'].disabled,true);
  h.resolve({json:()=>Promise.resolve({ok:true,protocol:'api',script:'mti-apply'})});
  await settle(); await settle();
  assert.match(h.el['build-ok'].textContent,/прежним настройкам/);
  assert.notEqual(h.state(),'применено');
  assert.equal(h.el['btn-apply'].disabled,true); assert.match(h.el['apply-reason'].textContent,/автоматическое применение отключено/);
  h.ctx.schedulePreview(); assert.equal(h.el['build-ok'].style.display,'none');

  const failure=setup(); failure.el['btn-apply'].handler.call(failure.el['btn-apply']);
  failure.ctx.schedulePreview(); failure.ctx.currentScript='new script'; failure.ctx.setActions(true);
  failure.resolve({json:()=>Promise.resolve({ok:false,error:'lost'})});
  await settle(); await settle();
  assert.match(failure.el['build-error'].textContent,/частично/);
  assert.match(failure.el['build-error'].textContent,/предыдущему запросу/);
  assert.notEqual(failure.state(),'ошибка применения');

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
