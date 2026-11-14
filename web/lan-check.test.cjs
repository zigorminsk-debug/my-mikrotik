// Read-only offline LAN overlap advisory and stale-file UI tests.
const assert=require('node:assert/strict');const fs=require('node:fs');const vm=require('node:vm');const path=require('node:path');
const html=fs.readFileSync(path.join(__dirname,'index.html'),'utf8');
function part(start,end){const a=html.indexOf(start),b=html.indexOf(end,a);assert(a>=0&&b>a);return html.slice(a,b)}
const ids=['lan-check-file','lan-check-address','lan-check-interface','lan-check-output','lan-check-run','lan-check-copy','lan-check-download'];
const el=Object.fromEntries(ids.map(id=>[id,{files:[],value:'',disabled:false,textContent:'',addEventListener(type,fn){this['on'+type]=fn}}]));
const pending=[];let calls=0;
class Reader{readAsText(f){this.file=f;pending.push(this)}finish(){this.result=this.file.content;this.onload()}fail(){this.onerror()}}
const ctx=vm.createContext({$:id=>el[id],FileReader:Reader,Promise,Map,Set,Blob,URL,document:{},setTimeout,copyText:()=>Promise.resolve(),flash(){},showError(){},fetch(){calls++;throw Error('network forbidden')}});
vm.runInContext(part('  var snapshotCompareSeq = 0','  function compareSnapshotReports('),ctx);
vm.runInContext(part('  function readComparisonFile(', '  ["compare-before", "compare-after"]'),ctx);
vm.runInContext(part('  // Offline advisory only;', '  loadPrefs();'),ctx);
const form=vm.createContext({protoLabel:x=>x});vm.runInContext(part('  function formatNetworkSnapshot(', '  $("snapshot-run").addEventListener'),form);
const ok=rows=>({status:'ok',rows});
const snapshot={protocol:'api',sections:{
 addresses:ok([{address:'192.168.88.1/24',network:'192.168.88.0',interface:'bridge',dynamic:'false',disabled:'false'}, {address:'10.0.0.1/8',network:'10.0.0.0',interface:'ether1',disabled:'true'}]),
 dhcp:ok([{interface:'wan',address:'172.16.1.5/24',disabled:'false'}]),
 dhcpNetworks:ok([{address:'192.168.88.0/24',gateway:'192.168.88.1'}]),
 pools:ok([{name:'test',ranges:'192.168.88.20-192.168.88.50,192.0.2.10-192.0.2.11','next-pool':'none'}]),
 dhcpServers:ok([{name:'defconf',interface:'bridge','address-pool':'test',disabled:'false'}])
}};
const report=form.formatNetworkSnapshot(snapshot,'v0.2.18');
let out=ctx.inspectLanSnapshot(report,'192.168.88.5/24','bridge');
assert.match(out,/Пересечение адреса\/маски: 192\.168\.88\.1\/24/);assert.match(out,/Пересечение DHCP-сети/);assert.match(out,/Пересечение пула/);assert.match(out,/На указанном интерфейсе/);assert.match(out,/с неизвестным интерфейсом или состоянием/);assert.match(out,/Серверов с неизвестным состоянием/);
assert.match(out,/Найдено пересечений\/совпадений: 4/);assert.match(out,/Начало \(UTC\): —/);assert.match(out,/Автоматическое применение LAN остаётся заблокировано/);
assert.match(out,/отсутствие предупреждений не доказывает безопасности/);assert.match(out,/IPv6/);
out=ctx.inspectLanSnapshot(report,'172.16.1.1/24','');assert.match(out,/Пересечение адреса DHCP-клиента/);assert.match(out,/интерфейс не указан/);
out=ctx.inspectLanSnapshot(report,'10.1.2.3/32','bridge');assert.match(out,/10\.0\.0\.1\/8.*disabled=true/);
out=ctx.inspectLanSnapshot(report,'192.168.89.1/24','other');assert.doesNotMatch(out,/Пересечение адреса\/маски|Пересечение DHCP-сети|Пересечение пула/);assert.match(out,/не подтверждает их отсутствие/);
assert.equal(ctx.lanIPv4('255.255.255.255'),4294967295);assert.equal(ctx.lanIPv4('01.2.3.4'),null);assert.equal(ctx.lanIPv4('1.2.3.256'),null);assert.equal(ctx.lanIPv4('1.2.3.4.5'),null);
assert.equal(ctx.lanCIDR('0.0.0.0/0').high,4294967295);assert.equal(ctx.lanCIDR('255.255.255.255/32').low,4294967295);assert.equal(ctx.lanCIDR('1.2.3.4/33'),null);
assert.equal(ctx.lanOverlap(ctx.lanCIDR('192.168.88.0/24'),ctx.lanCIDR('192.168.89.0/24')),false);
assert.equal(ctx.lanPoolRanges('192.0.2.10-192.0.2.1'),null);assert.equal(ctx.lanPoolRanges('192.0.2.0/24')[0].high,3221226239);assert.equal(ctx.lanPoolRanges('192.0.2.1,broken'),null);
assert.throws(()=>ctx.inspectLanSnapshot(report,'192.168.1.1',''),/корректный IPv4/);assert.throws(()=>ctx.inspectLanSnapshot(report,'192.168.1.1/24','x\ny'),/имя интерфейса/);assert.throws(()=>ctx.inspectLanSnapshot('bad','192.168.1.1/24',''),/не похож/);
const incomplete=form.formatNetworkSnapshot({sections:{addresses:{status:'error'},dhcp:{status:'skipped'},dhcpNetworks:ok([{address:'—'}]),pools:{status:'ok',truncated:true,rows:[{name:'x',ranges:'unknown'}]},dhcpServers:ok([{name:'x'}])}},'v');
out=ctx.inspectLanSnapshot(incomplete,'192.168.88.1/24','bridge');assert.match(out,/не прочитан или неоднозначен/);assert.match(out,/усечён/);assert.match(out,/Не удалось оценить/);assert.match(out,/неразобранным\/неизвестным интерфейсом/);
const stripped=form.formatNetworkSnapshot({sections:{}},'v').replace(/\nПеременные скрипта[^\n]*/, '');out=ctx.inspectLanSnapshot(stripped,'192.168.1.1/24','');assert.match(out,/отсутствует завершающий текст/);
const ambiguous=form.formatNetworkSnapshot({sections:{addresses:ok([{address:'192.168.88.1/24',network:'192.168.88.0',interface:'bridge; dynamic=evil'}])}},'v');out=ctx.inspectLanSnapshot(ambiguous,'192.168.88.1/24','');assert.match(out,/Не удалось оценить/);
const p2p=form.formatNetworkSnapshot({sections:{addresses:ok([{address:'192.168.88.1/24',network:'192.168.90.1'}]),pools:ok([{name:'x',ranges:'192.168.88.10-192.168.88.20','next-pool':'other'}])}},'v');out=ctx.inspectLanSnapshot(p2p,'192.168.88.1/24','');assert.match(out,/отличается от сети адреса/);assert.match(out,/цепочки пулов не анализируются/);
const many=form.formatNetworkSnapshot({sections:{addresses:ok(Array.from({length:250},()=>({address:'192.168.88.1/24',network:'192.168.88.0'})))}},'v');
out=ctx.inspectLanSnapshot(many,'192.168.88.1/24','');assert.match(out,/Вывод ограничен 200 подробными строками/);assert.match(out,/Найдено пересечений\/совпадений: 250/);
const partial=form.formatNetworkSnapshot({sections:{addresses:{status:'ok',truncated:true,rows:[{address:'192.168.88.1/24',network:'192.168.88.0'}]}}},'v');
out=ctx.inspectLanSnapshot(partial,'192.168.88.1/24','');assert.match(out,/Раздел усечён/);assert.match(out,/Пересечение адреса\/маски/);
const evil=form.formatNetworkSnapshot({sections:{pools:ok([{name:'<img src=x onerror=alert(1)>',ranges:'192.168.88.1-192.168.88.2','next-pool':'none'}])}},'v');
(async()=>{
 el['lan-check-file'].files=[{size:evil.length,content:evil}];el['lan-check-file'].onchange();el['lan-check-address'].value='192.168.88.1/24';el['lan-check-address'].oninput();assert.equal(el['lan-check-run'].disabled,false);
 el['lan-check-run'].onclick.call(el['lan-check-run']);assert.equal(pending.length,1);el['lan-check-address'].value='10.0.0.1/24';el['lan-check-address'].oninput();pending.shift().finish();await new Promise(r=>setImmediate(r));assert.equal(el['lan-check-output'].textContent,'');assert.equal(el['lan-check-copy'].disabled,true);
 el['lan-check-address'].value='192.168.88.1/24';el['lan-check-address'].oninput();el['lan-check-run'].onclick.call(el['lan-check-run']);
 const old=pending.shift();el['lan-check-interface'].value='bridge';el['lan-check-interface'].oninput();el['lan-check-run'].onclick.call(el['lan-check-run']);pending.shift().finish();await new Promise(r=>setImmediate(r));old.finish();await new Promise(r=>setImmediate(r));assert.match(el['lan-check-output'].textContent,/интерфейс: bridge/);assert.match(el['lan-check-output'].textContent,/<img src=x/);assert.equal(el['lan-check-output'].innerHTML,undefined);assert.equal(el['lan-check-copy'].disabled,false);
 el['lan-check-file'].files=[{size:3*1024*1024,content:evil}];el['lan-check-file'].onchange();el['lan-check-run'].onclick.call(el['lan-check-run']);await new Promise(r=>setImmediate(r));assert.match(el['lan-check-output'].textContent,/2 МиБ/);assert.equal(el['lan-check-copy'].disabled,true);assert.equal(calls,0);
 console.log('Offline LAN snapshot advisory: OK');
})().catch(e=>{console.error(e);process.exitCode=1});
