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

const els = Object.fromEntries(['build-body', 'build-need-conn', 'result', 'copy'].map(id =>
  [id, {style: {}, classList: {remove() {}}}]));
Object.assign(ctx, {
  lastInfo: {model: 'old'}, connectedKey: 'old', interfacesReady: true,
  lastIfaces: [{name:'ether1'}], currentScript: 'old script', previewTimer: null,
  previewSeq: 0, connectionKey: () => 'new', clearTimeout() {},
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
