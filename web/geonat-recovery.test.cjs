const assert=require('node:assert/strict'),fs=require('node:fs'),vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/index.html','utf8'),a=html.indexOf('  // Repair a single incomplete'),b=html.indexOf('  // A separate LAN plan',a);assert(a>=0&&b>a);
function setup(){
 const els=Object.fromEntries(['geo-recover','geo-recovery-status'].map(id=>[id,{disabled:false,textContent:'',addEventListener(_,fn){this.handler=fn}}]));
 let pending=[],requests=[];const device={protocol:'api',routeros:'7.25.1'},geo={enabled:true,resource:'by',wan:'ether1',destination:'192.168.88.50',port:'3389',protocol:'tcp'};
 const ctx=vm.createContext({$:id=>els[id],chk:id=>id==='chk-geoNat'||id==='geo-ack',window:{confirm:()=>true},geoApplyIssue:()=>'',currentScript:'# MTI-GEONAT-GUARDED-APPLY',currentGeoPreview:{device,interfaces:[],geoNat:{...geo,acknowledged:true},generatedAt:'2026-01-01T00:00:00Z'},lastInfo:device,interfacesReady:true,connectedKey:'key',connectionKey:()=> 'key',previewSeq:1,currentGeoCheckParams:()=>({...geo}),connPayload:()=>({host:'192.0.2.1'}),applyInProgress:false,clearGeoCheck(){},setActions(){},setScriptState(){},fetch:(url,opts)=>{requests.push({url,body:JSON.parse(opts.body)});return new Promise(resolve=>pending.push(resolve));}});
 vm.runInContext(html.slice(a,b),ctx);return {ctx,els,requests,resolve:res=>pending.shift()({json:()=>Promise.resolve(res)})};
}
const settle=()=>new Promise(r=>setImmediate(r));
(async()=>{
 let h=setup();h.els['geo-recover'].handler.call(h.els['geo-recover']);assert.equal(h.requests[0].url,'/api/geonat/recover');assert.equal(h.requests[0].body.acknowledged,true);assert.equal(h.requests[0].body.geoNat.destination,'192.168.88.50');h.resolve({ok:true,count:106});await settle();await settle();assert.equal(h.requests[1].url,'/api/geonat/apply');h.resolve({ok:true,protocol:'api'});await settle();await settle();assert.match(h.els['geo-recovery-status'].textContent,/проверьте порядок/i);
 h=setup();h.els['geo-recover'].handler.call(h.els['geo-recover']);h.ctx.previewSeq++;h.resolve({ok:true,count:106});await settle();await settle();assert.equal(h.requests.length,1);assert.match(h.els['geo-recovery-status'].textContent,/не применён/);
 h=setup();h.els['geo-recover'].handler.call(h.els['geo-recover']);h.resolve({ok:true,count:106});await settle();await settle();h.resolve({ok:false,stage:'preflight',error:'no nat'});await settle();await settle();assert.match(h.els['geo-recovery-status'].textContent,/остановилось до записи/);
 h=setup();h.els['geo-recover'].handler.call(h.els['geo-recover']);h.resolve({ok:false,error:'lost'});await settle();await settle();assert.equal(h.requests.length,1);assert.match(h.els['geo-recovery-status'].textContent,/частичными/);
 console.log('GeoNAT recovery UI: OK');
})().catch(e=>{console.error(e);process.exitCode=1});
