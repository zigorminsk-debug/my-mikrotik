// Offline snapshot comparison: accuracy, privacy and stale asynchronous reads.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const path = require('node:path');
const html = fs.readFileSync(path.join(__dirname, 'index.html'), 'utf8');
function between(a,b) {const start=html.indexOf(a),end=html.indexOf(b,start);assert(start>=0&&end>start);return html.slice(start,end);}
const names=['compare-before','compare-after','compare-output','compare-run','compare-copy','compare-download'];
const el=Object.fromEntries(names.map(n=>[n,{files:[],disabled:false,textContent:'',addEventListener(type,fn){this.handler=fn}}]));
const pending=[];let networkCalls=0;
class FileReaderMock {readAsText(file){this.file=file;pending.push(this)} finish(){this.result=this.file.content;this.onload();}}
const ctx=vm.createContext({$:id=>el[id],FileReader:FileReaderMock, Promise,Map,Set,Blob,URL,document:{},setTimeout,
  copyText:()=>Promise.resolve(),flash(){},showError(){},fetch(){networkCalls++;throw Error('Network not allowed')}});
vm.runInContext(between('  var snapshotCompareSeq = 0', '  // Only explicitly selected fields'),ctx);
const header='MikroTik Inspector — снимок каналов и маршрутов\nВерсия: v0.2.9\nНачало (UTC): 2026-01-01\n';
const sec=(name,rows,tail='')=>'\n'+name+':\n'+(rows.length ? rows.map((r,i)=>'  '+(i+1)+'. '+r).join('\n'):'  Записей не найдено.')+(tail?'\n  '+tail:'')+'\n';
const i1='name=ether1; type=ether; running=true; disabled=false',i2='name=lte1; type=lte; running=false; disabled=false';
const one=header+sec('Интерфейсы',[i1,i2])+sec('IPv4-адреса',['address=192.168.88.1/24; network=—']);
const reordered=header.replace('0.2.9','0.2.15')+sec('Интерфейсы',[i2,i1])+sec('IPv4-адреса',['address=192.168.88.1/24; network=—']);
let out=ctx.compareSnapshotReports(one,reordered);
assert.match(out,/Интерфейсы:\n  Распознанные строки совпадают/); assert.match(out,/несовпавших строк: 0/);
const changed=header+sec('Интерфейсы',[i1,i2.replace('running=false','running=true')])+sec('IPv4-адреса',['address=192.168.88.1/24; network=—']);
out=ctx.compareSnapshotReports(one,changed);assert.match(out,/Только в первом/);assert.match(out,/Только в втором/);assert.match(out,/несовпавших строк: 2/);
assert.match(out,/не доказывает добавление или удаление/);assert.match(out,/одному устройству/);
const duplicate=header+sec('Интерфейсы',[i1,i1,i2]);out=ctx.compareSnapshotReports(duplicate,header+sec('Интерфейсы',[i1,i2]));assert.match(out,/Только в первом \(1\)/);
const errors=header+'\nИнтерфейсы:\n  Не удалось прочитать (права, версия RouterOS или ошибка связи).\n';
out=ctx.compareSnapshotReports(one,errors);assert.match(out,/не прочитан, усечён или разобран неоднозначно/);assert.match(out,/Нет раздела во втором/);assert.doesNotMatch(out,/Только в первом/);
const truncated=header+sec('Интерфейсы',[i1],'Показаны не все записи (лимит 500).');out=ctx.compareSnapshotReports(truncated,one);assert.match(out,/усечён/);
const optional=header+sec('Интерфейсы',[i1])+sec('LTE: разовое чтение показателей',['name=lte1; rsrp=—']);out=ctx.compareSnapshotReports(optional,header+sec('Интерфейсы',[i1]));assert.match(out,/Нет раздела во втором/);
assert.throws(()=>ctx.parseSnapshotReport('wrong'),/не похож/);
assert.throws(()=>ctx.parseSnapshotReport(header+sec('Интерфейсы',[])+sec('Интерфейсы',[])),/повторяется/);
assert.throws(()=>ctx.parseSnapshotReport(header+'\nИнтерфейсы:\n  1. name='+('x'.repeat(8193))+'\n'),/длинная строка/);
assert.doesNotThrow(()=>ctx.parseSnapshotReport('\ufeff'+one.replace(/\n/g,'\r\n')));
assert.match(ctx.compareSnapshotReports(one,one),/не найден завершающий текст/);
out=ctx.compareSnapshotReports(header+'\nИнтерфейсы:\n  1. name=partial',one); assert.match(out,/неоднозначно/);
assert.match(ctx.compareSnapshotReports(one,one),/Нет раздела в обоих снимках/);
const formatter=vm.createContext({protoLabel:x=>x});vm.runInContext(between('  function formatNetworkSnapshot(', '  $("snapshot-run").addEventListener'),formatter);
const actual=formatter.formatNetworkSnapshot({protocol:'api',sections:{interfaces:{status:'ok',rows:[{name:'ether1',type:'ether',running:'true',disabled:'false'}]}}},'v0.2.16');
out=ctx.compareSnapshotReports(actual,actual);assert.match(out,/Интерфейсы:\n  Распознанные строки совпадают/);assert.doesNotMatch(out,/не найден завершающий текст/);
const ambiguous=header+'\nИнтерфейсы:\n  странная строка\n';out=ctx.compareSnapshotReports(ambiguous,one);assert.match(out,/неоднозначно/);
const many=header+sec('Интерфейсы',Array.from({length:401},(_,i)=>'name=ether'+i));out=ctx.compareSnapshotReports(many,header+sec('Интерфейсы',[]));assert.match(out,/ограничен 400/);
const repeats=header+sec('Интерфейсы',Array.from({length:500},()=>i1));out=ctx.compareSnapshotReports(repeats,header+sec('Интерфейсы',[]));assert.doesNotMatch(out,/ограничен 400/);
const oversizedRows=header+sec('Интерфейсы',Array.from({length:2001},(_,i)=>'name='+i));out=ctx.compareSnapshotReports(oversizedRows,one);assert.match(out,/неоднозначно/);
(async()=>{
  const before={size:one.length,content:one},after={size:changed.length,content:changed};
  el['compare-before'].files=[before];el['compare-after'].files=[after];el['compare-before'].handler();assert.equal(el['compare-run'].disabled,false);
  el['compare-run'].handler.call(el['compare-run']);assert.equal(pending.length,2);
  el['compare-after'].files=[before];el['compare-after'].handler();pending.shift().finish();pending.shift().finish();await new Promise(r=>setImmediate(r));
  assert.equal(el['compare-output'].textContent,'');assert.equal(el['compare-copy'].disabled,true);
  el['compare-run'].handler.call(el['compare-run']);pending.shift().finish();pending.shift().finish();await new Promise(r=>setImmediate(r));
  assert.match(el['compare-output'].textContent,/Распознанные строки совпадают/);assert.equal(el['compare-copy'].disabled,false);
  const evil={size:one.length+100,content:header+sec('Интерфейсы',['name=<img src=x onerror=alert(1)>'])};
  el['compare-after'].files=[evil];el['compare-after'].handler();el['compare-run'].handler.call(el['compare-run']);pending.shift().finish();pending.shift().finish();await new Promise(r=>setImmediate(r));
  assert.match(el['compare-output'].textContent,/<img src=x onerror=alert\(1\)>/);assert.equal(el['compare-output'].innerHTML,undefined);
  el['compare-before'].files=[{size:2*1024*1024+1,content:one}];el['compare-before'].handler();el['compare-run'].handler.call(el['compare-run']);
  // The other file can still be read, but the request must fail without comparing.
  pending.shift().finish();await new Promise(r=>setImmediate(r));assert.match(el['compare-output'].textContent,/2 МиБ/);assert.equal(el['compare-copy'].disabled,true);
  assert.equal(networkCalls,0);console.log('Offline snapshot comparison: OK');
})().catch(e=>{console.error(e);process.exitCode=1});
