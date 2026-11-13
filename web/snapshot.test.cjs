// Regression test for async snapshot invalidation and transport selection.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const html = fs.readFileSync(require('node:path').join(__dirname, 'index.html'), 'utf8');
const start = html.indexOf('  function clearNetworkSnapshot()');
const end = html.indexOf('  // Only explicitly selected fields', start);
assert(start >= 0 && end > start);
const names = ['snapshot-output','snapshot-run','snapshot-copy','snapshot-download','snapshot-history','snapshot-lte','appver'];
const el = Object.fromEntries(names.map(n => [n, {textContent:'', disabled:false, addEventListener(type, fn) {this.handler=fn;}}]));
el.appver.textContent='v0.2.6';
let resolveFetch, sent;
const ctx = vm.createContext({
  $: id => el[id], snapshotSeq:0, snapshotText:'', lastInfo:{protocol:'rest'}, connectedKey:'key',
  connectionKey:()=>'key', connPayload:()=>({host:'router',protocol:'auto'}), protoLabel:p=>p,
  fetch:(url, options)=>{sent = {url, body:JSON.parse(options.body)}; return new Promise(resolve => {resolveFetch=resolve;});},
  copyText:()=>Promise.resolve(), flash(){}, showError(){}, Blob, URL, document:{}, setTimeout,
});
vm.runInContext(html.slice(start,end),ctx);
const mk = (rows, status='ok') => ({protocol:'api',sections:{routes:{status,rows}}});
const lte = {'dst-address':'0.0.0.0/0','routing-table':'main',gateway:'172.26.48.63','immediate-gw':'172.26.48.63%lte1',distance:'2',active:'true',dynamic:'true'};
const ethernet = {'dst-address':'0.0.0.0/0','routing-table':'main',gateway:'10.254.100.254','immediate-gw':'10.254.100.254%ether1',distance:'2',active:'true',dynamic:'true'};
const controlled = {'dst-address':'0.0.0.0/0','routing-table':'main',gateway:'172.26.48.63%lte1',distance:'3',disabled:'false',comment:'mti-wan-0'};
const warns = rows => ctx.formatNetworkSnapshot(mk(rows),'v').includes('ВНИМАНИЕ: в main');
assert.match(ctx.formatNetworkSnapshot(mk([lte]), 'v'), /ручные ping и ICMP traceroute/);
const lanReport = ctx.formatNetworkSnapshot({sections:{dhcpServers:{status:'ok',rows:[{name:'defconf',interface:'bridge'}]},pools:{status:'error'},dhcpNetworks:{status:'skipped'}}},'v');
const lteReport=ctx.formatNetworkSnapshot({sections:{lte:{status:'ok',truncated:true,rows:[{name:'lte1','monitor-status':'ok',rsrp:'-97dBm'}]}}},'v');
assert.match(lteReport,/rsrp=-97dBm/); assert.match(lteReport,/первые 4/); assert.match(lteReport,/Пустые поля неизвестны/);
assert.match(ctx.formatNetworkSnapshot({sections:{lte:{status:'unsupported'}}},'v'),/Доступно только для RouterOS 7/);
const historyReport=ctx.formatNetworkSnapshot({sections:{failoverHistory:{status:'ok',truncated:true,rows:[{time:'11:57:00',event:'Контроллер сообщил о переключении',backup:'true',distance:'3'}]}}},'v');
assert.match(historyReport,/11:57:00/); assert.match(historyReport,/последние 100/); assert.match(historyReport,/не подтверждает путь трафика/);
assert.doesNotMatch(ctx.formatNetworkSnapshot({sections:{}},'v'),/История контроллера/);
assert.match(lanReport, /DHCP-серверы/); assert.match(lanReport, /defconf/); assert.match(lanReport, /IPv4-пулы/); assert.match(lanReport, /DHCP-сети/);

assert.equal(warns([lte, ethernet, controlled]), true);
assert.equal(warns([{...lte,distance:'1'}, ethernet]), false);
assert.equal(warns([lte, {...ethernet,active:undefined}]), false);
assert.equal(warns([lte, {...ethernet,'routing-table':'mti-probe'}]), false);
assert.equal(warns([lte, {...lte}]), false);
assert.equal(ctx.formatNetworkSnapshot(mk([lte,ethernet],'error'),'v').includes('ВНИМАНИЕ: в main'), false);
(async()=>{
  el['snapshot-history'].checked=true; el['snapshot-lte'].checked=true;
  el['snapshot-run'].handler.call(el['snapshot-run']);
  assert.equal(sent.body.includeHistory,true); assert.equal(sent.body.includeLTE,true);
  assert.equal(sent.url,'/api/network-snapshot');
  assert.equal(sent.body.conn.protocol,'rest');
  el['snapshot-lte'].handler();
  resolveFetch({ok:true,json:()=>Promise.resolve({ok:true,snapshot:{protocol:'rest',sections:{}}})});
  await new Promise(resolve=>setImmediate(resolve));
  assert.equal(el['snapshot-output'].textContent,'');
  assert.equal(el['snapshot-copy'].disabled,true);
  el['snapshot-run'].handler.call(el['snapshot-run']);
  resolveFetch({ok:true,json:()=>Promise.resolve({ok:true,snapshot:{protocol:'rest',sections:{interfaces:{status:'ok',rows:[{name:'ether1'}]}}}})});
  await new Promise(resolve=>setImmediate(resolve));
  assert.match(el['snapshot-output'].textContent,/ether1/);
  assert.equal(el['snapshot-copy'].disabled,false);
  ctx.connectionKey=()=> 'changed';
  el['snapshot-run'].handler.call(el['snapshot-run']);
  assert.equal(el['snapshot-output'].textContent.includes('ether1'),true);
  console.log('Snapshot UI async guards: OK');
})().catch(e=>{console.error(e);process.exitCode=1;});
