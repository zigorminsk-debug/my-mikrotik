// Browser-independent regression checks for critical constructor guards.
// Run with: node web/ui.test.cjs
const assert = require('node:assert/strict');
const fs = require('node:fs');
const vm = require('node:vm');
const html = fs.readFileSync(require('node:path').join(__dirname, 'index.html'), 'utf8');
function between(start, end) {
  const from = html.indexOf(start), to = html.indexOf(end, from);
  assert(from >= 0 && to > from, `missing ${start}`);
  return html.slice(from, to);
}

const ctx = vm.createContext({});
vm.runInContext(between('  function fmtAddr(str)', '  document.addEventListener("blur"'), ctx);
assert.equal(ctx.fmtAddr('192.168.088.001/24'), '192.168.88.1/24');
for (const invalid of ['10.0.0.300/24', '10.0.0.1/40', '1.2.3.4.5', 'abc192.168.1.1']) {
  assert.equal(ctx.fmtAddr(invalid), invalid);
}
vm.runInContext(between('  function isWifi(i)', '  function applyInterfaces(list)'), ctx);
assert.equal(ctx.preferLan([
  {name: 'ether1', type: 'ethernet'},
  {name: 'ether2', type: 'ethernet', bridge: 'bridge'},
  {name: 'bridge', type: 'bridge'},
]), 'bridge');
assert.equal(ctx.preferLan([{name:'b1', type:'bridge'}, {name:'b2', type:'bridge'}]), '');
assert.equal(ctx.preferLan([{name:'ether2', type:'ethernet', bridge:'bridge'}]), '');
assert.equal(ctx.preferEther([{name:'ether1', type:'ethernet', bridge:'bridge'}, {name:'ether2', type:'ethernet'}]), 'ether2');

vm.runInContext(between('  function protoLabel(p)', '  // ------------------------------------------------------------------\n  // автоматическое'), ctx);
vm.runInContext(between('  function identityDiagnostic(info, version, time)', '  function report(info)'), ctx);
const diagnostic = ctx.identityDiagnostic({protocol:'api', identity:'PRIVATE-IDENTITY', model:'hAP ac3', architecture:'arm', routeros:'7', diagnostics:{resourceBoard:'hAP ac3', routerboardModel:'RBD53iG', routerboardBoard:'legacy', routerboardStatus:'ok'}, password:'SECRET', serial:'SERIAL'}, 'Версия: v0.2.4', '2026-01-01T00:00:00Z');
assert.match(diagnostic, /board-name: hAP ac3/);
assert.match(diagnostic, /model: RBD53iG/);
assert.doesNotMatch(diagnostic, /PRIVATE-IDENTITY|SECRET|SERIAL/);
assert.match(ctx.identityDiagnostic({model:'CHR',diagnostics:{routerboardStatus:'error'}}, 'v', 't'), /не удалось прочитать/);
assert.match(ctx.identityDiagnostic({model:'RB',diagnostics:{routerboardModel:'RB',routerboardStatus:'ok'}}, 'v', 't'), /model \(резерв\)/);

vm.runInContext(between('  function formatNetworkSnapshot(snapshot, version)', '  $("snapshot-run").addEventListener'), ctx);
const snapshot = ctx.formatNetworkSnapshot({protocol:'api',startedAt:'start',finishedAt:'end',sections:{interfaces:{status:'ok',rows:[{name:'ether1', running:'true'}]},addresses:{status:'error',rows:[]},dhcp:{status:'skipped',rows:[]},routes:{status:'ok',rows:[]}}}, 'v');
assert.match(snapshot, /name=ether1; type=—; running=true/);
assert.match(snapshot, /Не удалось прочитать/);
assert.match(snapshot, /Не прочитано/);
assert.match(snapshot, /Записей не найдено/);

const els = Object.fromEntries(['build-body', 'build-need-conn', 'result', 'copy'].map(id =>
  [id, {style: {}, classList: {remove() {}}}]));
Object.assign(ctx, {
  lastInfo: {model: 'old'}, connectedKey: 'old', interfacesReady: true,
  lastIfaces: [{name:'ether1'}], currentScript: 'old script', previewTimer: null,
  previewSeq: 0, connectionKey: () => 'new', clearTimeout() {},
  clearNetworkSnapshot() {},
  $: id => els[id], setActions(enabled) { ctx.actions = enabled; }, showError(msg) { ctx.error = msg; },
});
vm.runInContext(between('  function invalidateConnection()', '  ["host", "port", "user"'), ctx);
ctx.invalidateConnection();
assert.equal(ctx.lastInfo, null);
assert.equal(ctx.connectedKey, null);
assert.equal(ctx.interfacesReady, false);
assert.equal(ctx.currentScript, '');
assert.equal(ctx.actions, false);
assert.match(ctx.error, /Подключитесь заново/);
console.log('UI guards: OK');

// Experimental USB scripts can be inspected/copied but not applied by the UI.
const usbEls = Object.fromEntries(['btn-copy', 'btn-download', 'btn-apply'].map(id => [id, {disabled:false}]));
const usbCtx = vm.createContext({$: id => usbEls[id], chk: () => false, currentScript: '# MTI-USB-PREVIEW-ONLY\n:put 1'});
vm.runInContext(between('  function setActions(enabled)', '  // ------------------------------------------------------------------\n  // предпросмотр'), usbCtx);
usbCtx.setActions(true);
assert.equal(usbEls['btn-copy'].disabled, false);
assert.equal(usbEls['btn-download'].disabled, false);
assert.equal(usbEls['btn-apply'].disabled, true);
usbCtx.currentScript = ':put 1'; usbCtx.setActions(true);
assert.equal(usbEls['btn-apply'].disabled, false);
assert.match(html, /usb-tether/);
assert.match(html, /скопированный скрипт остаётся исполняемым/);
