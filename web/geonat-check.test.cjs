const assert=require('node:assert/strict'), fs=require('node:fs'), vm=require('node:vm');
const html=fs.readFileSync(__dirname+'/index.html','utf8');
const start=html.indexOf('  // A separate read-only check;'), end=html.indexOf('  // A separate LAN plan',start);
assert(start>=0 && end>start);
let resolve, requests=[];
const ids=['geo-check','geo-check-output','geo-id','geo-wan','geo-destination','geo-port','geo-protocol'];
const els=Object.fromEntries(ids.map(id=>[id,{value:'',textContent:'',disabled:false,addEventListener(_,fn){this.handler=fn}}]));
Object.assign(els['geo-id'],{value:'rdp'});els['geo-wan'].value='ether1';els['geo-destination'].value='192.168.88.50';els['geo-port'].value='3389';els['geo-protocol'].value='tcp';
const ctx=vm.createContext({$:id=>els[id], val:id=>els[id].value,chk:()=>true,geoCheckSeq:0,applyInProgress:false,lastInfo:{protocol:'rest'},connectedKey:'key',interfacesReady:true,connectionKey:()=> 'key',connPayload:()=>({host:'router'}),fetch:(url,opts)=>{requests.push({url,body:JSON.parse(opts.body)});return new Promise(r=>resolve=r)}});
vm.runInContext(html.slice(start,end),ctx);
const settle=()=>new Promise(r=>setImmediate(r));
(async()=>{
 els['geo-check'].handler.call(els['geo-check']);
 assert.equal(requests[0].url,'/api/geonat/check'); assert.equal(requests[0].body.geoNat.destination,'192.168.88.50'); assert(!('script' in requests[0].body));
 els['geo-destination'].value='192.168.88.51';ctx.clearGeoCheck();
 resolve({json:()=>Promise.resolve({ok:true,matched:true,message:'old'})});await settle();await settle();assert.equal(els['geo-check-output'].textContent,'');
 els['geo-check'].handler.call(els['geo-check']);resolve({json:()=>Promise.resolve({ok:true,matched:false,message:'disabled не получено'})});await settle();await settle();assert.match(els['geo-check-output'].textContent,/disabled не получено/);
 console.log('GeoNAT read-only UI check: OK');
})().catch(e=>{console.error(e);process.exitCode=1});
