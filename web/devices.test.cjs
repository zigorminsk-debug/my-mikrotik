// Browser-independent local device list tests. No router is contacted.
const assert = require('node:assert/strict');
const fs = require('node:fs'); const vm = require('node:vm'); const path = require('node:path');
const html=fs.readFileSync(path.join(__dirname,'index.html'),'utf8');
const start=html.indexOf('  // Desktop device list:'),end=html.indexOf('  // Offline advisory only;',start);assert(start>=0&&end>start);
assert(html.indexOf('id="devices-card"')<html.indexOf('<section id="result">'));
const ids=['devices-select','devices-name','devices-host','devices-note','devices-add','devices-update','devices-delete','devices-use','devices-refresh','devices-export','devices-file','devices-import','devices-status','host','password','go'];
const els=Object.fromEntries(ids.map(id=>[id,{value:'',files:[],disabled:false,textContent:'',children:[],addEventListener(type,fn){this['on'+type]=fn},appendChild(e){this.children.push(e)}}]));
let serverDevices=[], revision='a'.repeat(64), requests=[], force, synced=0, rejectWrite=false, pendingReader;
const fakeDoc={createElement(){return {value:'',textContent:'',click(){},remove(){}}},body:{appendChild(){},removeChild(){}}};
class Reader {readAsText(f){pendingReader=this;this.file=f} finish(){this.result=this.file.content;this.onload()} fail(){this.onerror()}}
const ctx=vm.createContext({$:id=>els[id], document:fakeDoc, FileReader:Reader, Blob, URL, setTimeout, confirm:()=>true,
  applyInProgress:false,syncProtocolFromHost(){synced++},invalidateConnection(f){force=f},
  fetch(url,opts={}) {requests.push({url,opts});assert.equal(url,'/api/devices');
    if(opts.method==='POST') {if(rejectWrite)return Promise.reject(Error('network offline'));const body=JSON.parse(opts.body);assert.equal(body.revision,revision);
      if(body.devices.some(d=>d.name==='duplicate'))return Promise.resolve({ok:false,status:400,json:()=>Promise.resolve({ok:false,error:'duplicate'})});
      serverDevices=body.devices; revision=String.fromCharCode(revision.charCodeAt(0)+1).repeat(64);}
    return Promise.resolve({ok:true,status:200,json:()=>Promise.resolve({ok:true,revision,devices:serverDevices})});
  },
});
vm.runInContext(html.slice(start,end),ctx);
const settle=()=>new Promise(r=>setImmediate(r));
(async()=>{
  await settle();assert.equal(els['devices-add'].disabled,false); assert.equal(requests.length,1);
  els['devices-name'].value='<b>Office</b>';els['devices-host'].value='192.168.88.5';els['devices-note'].value='test';els.password.value='SECRET';
  els['devices-add'].onclick();await settle();assert.equal(serverDevices.length,1);
  const body=requests[1].opts.body;assert.doesNotMatch(body,/SECRET|password/);assert.deepEqual(Object.keys(JSON.parse(body).devices[0]).sort(),['host','name','note']);
  assert.equal(els['devices-select'].children.at(-1).textContent,'<b>Office</b> — 192.168.88.5');
  els['devices-select'].value='0';els['devices-select'].onchange.call(els['devices-select']);assert.equal(els['devices-use'].disabled,false);
  els['devices-use'].onclick();assert.equal(els.host.value,'192.168.88.5');assert.equal(els.password.value,'');assert.equal(force,true);assert.equal(synced,1);
  els.password.value='another';els.go.disabled=true;els['devices-use'].onclick();assert.equal(els.password.value,'another');assert.match(els['devices-status'].textContent,/Дождитесь/);els.go.disabled=false;
  ctx.applyInProgress=true;els['devices-use'].onclick();assert.equal(els.password.value,'another');ctx.applyInProgress=false;
  els['devices-select'].value='0';els['devices-select'].onchange.call(els['devices-select']);els['devices-note'].value='changed';els['devices-update'].onclick();await settle();assert.equal(serverDevices[0].note,'changed');
  const bad={size:100,content:JSON.stringify({schema:1,devices:[{name:'x',host:'x',note:'',password:'DO-NOT-SEND'}]})};
  els['devices-file'].files=[bad];els['devices-file'].onchange();const before=requests.length;els['devices-import'].onclick();assert.equal(els['devices-file'].disabled,true);pendingReader.finish();await settle();assert.equal(requests.length,before);assert.match(els['devices-status'].textContent,/лишние поля/);
  const good={size:100,content:JSON.stringify({schema:1,devices:[{name:'second',host:'10.0.0.1',note:''}]})};els['devices-file'].files=[good];els['devices-file'].onchange();els['devices-import'].onclick();pendingReader.finish();await settle();assert.equal(serverDevices.length,2);
  const tooBig={size:512*1024+1,content:''};els['devices-file'].files=[tooBig];els['devices-file'].onchange();els['devices-import'].onclick();assert.match(els['devices-status'].textContent,/слишком большой/);
  els['devices-select'].value='1';els['devices-select'].onchange.call(els['devices-select']);els['devices-delete'].onclick();await settle();assert.equal(serverDevices.length,1);
  rejectWrite=true;els['devices-add'].onclick();await settle();assert.equal(els['devices-add'].disabled,true);assert.match(els['devices-status'].textContent,/Состояние сохранения неизвестно/);rejectWrite=false;
  els['devices-refresh'].onclick();await settle();assert.equal(els['devices-add'].disabled,false);assert.equal(serverDevices.length,1);
  console.log('Device list UI safety: OK');
})().catch(e=>{console.error(e);process.exitCode=1});
