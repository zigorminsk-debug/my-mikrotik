const assert=require('node:assert/strict');const fs=require('node:fs');const vm=require('node:vm');const html=fs.readFileSync(require('node:path').join(__dirname,'index.html'),'utf8');
const start=html.indexOf('  // A separate LAN plan'),end=html.indexOf('  loadPrefs();',start);assert(start>=0&&end>start);
const ids=['lan-live-output','lan-live-ack','lan-live-apply','lan-live-plan','lan-live-status','host'];
const el=Object.fromEntries(ids.map(id=>[id,{textContent:'',value:'',checked:false,disabled:false,addEventListener(type,fn){this['on'+type]=fn}}]));el.host.value='192.168.88.1';
const selected={'chk-lan':true,'chk-dhcp':true},values={'lan-iface':'bridge','lan-addr':'192.168.50.1/24','dhcp-iface':'bridge','dhcp-start':'','dhcp-end':'','dhcp-lease':'1d'};
let queue=[],calls=[],ok=true;
const ctx=vm.createContext({$:id=>el[id],chk:id=>!!selected[id],val:id=>values[id],lanLiveSeq:0,lanLivePlan:null,lanLiveBusy:false,applyInProgress:false,previewSeq:1,previewTimer:null,currentScript:'old',
 lastInfo:{protocol:'rest'},lastIfaces:[{name:'bridge'}],interfacesReady:true,connectedKey:'key',connectionKey:()=> 'key',connPayload:()=>({host:'192.168.88.1',password:'secret',protocol:'rest'}),setActions(){},window:{confirm:()=>ok},
 fetch:(url,opts)=>{calls.push({url,body:JSON.parse(opts.body)});return new Promise((resolve,reject)=>queue.push({resolve,reject}))}});
vm.runInContext(html.slice(start,end),ctx);const settle=()=>new Promise(r=>setImmediate(r));const response=plan=>({json:()=>Promise.resolve({ok:true,plan})});const plan={summary:'<img src=x onerror=alert(1)>',script:'# MTI-LAN-GUARDED-APPLY\n:put 1',fingerprint:'a'.repeat(64)};
(async()=>{
 el['lan-live-plan'].onclick.call(el['lan-live-plan']);assert.equal(calls[0].url,'/api/lan/plan');assert.equal(calls[0].body.dhcp.enabled,true);assert.equal(calls[0].body.conn.password,'secret');
 values['lan-addr']='192.168.51.1/24';ctx.previewSeq++;ctx.clearLiveLANPlan();queue.shift().resolve(response(plan));await settle();assert.equal(el['lan-live-apply'].disabled,true);assert.match(el['lan-live-status'].textContent,/заново/);
 el['lan-live-plan'].onclick.call(el['lan-live-plan']);queue.shift().resolve(response(plan));await settle();assert.match(el['lan-live-output'].textContent,/<img/);assert.equal(el['lan-live-output'].innerHTML,undefined);
 el['lan-live-ack'].checked=true;el['lan-live-ack'].onchange.call(el['lan-live-ack']);assert.equal(el['lan-live-apply'].disabled,false);
 ok=false;el['lan-live-apply'].onclick.call(el['lan-live-apply']);assert.equal(calls.length,2);ok=true;
 el['lan-live-apply'].onclick.call(el['lan-live-apply']);assert.equal(calls[2].url,'/api/lan/apply');assert.equal(calls[2].body.acknowledged,true);assert.equal(calls[2].body.fingerprint,plan.fingerprint);assert.equal(calls[2].body.script,plan.script);assert.equal(ctx.applyInProgress,true);
 el['lan-live-apply'].onclick.call(el['lan-live-apply']);assert.equal(calls.length,3);
 ctx.previewSeq++;queue.shift().resolve(response(plan));await settle();assert.match(el['lan-live-status'].textContent,/ранее выбранному/);assert.equal(ctx.applyInProgress,false);assert.equal(el['lan-live-apply'].disabled,true);
 el['lan-live-plan'].onclick.call(el['lan-live-plan']);queue.shift().resolve(response(plan));await settle();el['lan-live-ack'].checked=true;el['lan-live-ack'].onchange.call(el['lan-live-ack']);el['lan-live-apply'].onclick.call(el['lan-live-apply']);queue.shift().reject(Error('connection lost'));await settle();assert.match(el['lan-live-status'].textContent,/частично/);assert.equal(el['lan-live-apply'].disabled,true);
 ctx.lastInfo.protocol='mac';el['lan-live-plan'].onclick.call(el['lan-live-plan']);assert.match(el['lan-live-status'].textContent,/не по MAC/);
 console.log('LAN live plan and apply UI: OK');
})().catch(e=>{console.error(e);process.exitCode=1});
