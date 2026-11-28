const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const i18n = fs.readFileSync(path.join(__dirname, '../ui/i18n.js'), 'utf8');

// 使用实际页面函数验证身份隔离、注销和账号操作，不复制业务实现。
function harness({ role = 'admin', api, confirm = true, language = 'zh-CN' } = {}) {
  const nodes = new Map(), handlers = new Map(), requests = [];
  const $ = id => {
    if (!nodes.has(id)) {
      const classes = new Set(['hidden']);
      nodes.set(id, {
        value: '', textContent: '', innerHTML: '', required: false, disabled: false, dataset: {}, isConnected: true,
        classList: { add: name => classes.add(name), remove: name => classes.delete(name), toggle: (name, value) => value ? classes.add(name) : classes.delete(name), contains: name => classes.has(name) },
        setAttribute() {}, removeAttribute() {}, focus() {},
        querySelector: () => $('tbody'),
        addEventListener(name, fn) { handlers.set(id + ':' + name, fn); },
      });
    }
    return nodes.get(id);
  };
  const state = { consoleRole: role, consoleUsername: 'admin', accountSecurityRequest: 0, activeSessions: [], consoleAccounts: [] };
  let signedOut = false;
  const context = vm.createContext({
    $, state, document: { activeElement: null, documentElement: { dataset: {} }, querySelectorAll: () => [] },
    window: { matchMedia: () => ({ matches: false }) }, navigator: { language },
    localStorage: { getItem: () => null }, location: { search: '' }, URLSearchParams,
    api: async (url, options) => { requests.push({ url, options }); return api ? api(url, options) : { sessions: [], accounts: [] }; },
    onSubmit: (id, handler) => handlers.set(id + ':submit', handler),
    resetSecretField: node => { node.value = ''; }, clearFormErrors() {}, showToast() {},
    resetSecretVisibility() {},
    confirmDialog: async () => confirm,
    dismissReauthToLogin: () => { signedOut = true; },
    formatDate: value => value,
  });
  vm.runInContext(i18n, context);
  context.t = context.window.EngramI18n.t;
  vm.runInContext(source.slice(source.indexOf('  function escapeHtml('), source.indexOf('  // onSubmit prevents')), context);
  vm.runInContext(source.slice(source.indexOf('  // 账号与会话仅按当前登录身份加载'), source.indexOf('  // Settings navigation supports')), context);
  vm.runInContext(source.slice(source.indexOf('  // renderView'), source.indexOf('  function renderView(')), context);
  return { $, state, context, handlers, requests, get signedOut() { return signedOut; } };
}

test('read-only accounts only load their sessions and cannot open account changes', async () => {
  const h = harness({ role: 'viewer' });
  await h.context.loadAccountSecurity();
  assert.deepEqual(h.requests.map(request => request.url), ['/api/admin/sessions']);
  h.context.openConsoleAccount('create');
  assert.equal(h.state.accountAction, undefined);
});

test('late session responses cannot restore a previous account after identity changes', async () => {
  const resolvers = [];
  const h = harness({ api: url => url.endsWith('/sessions') ? new Promise(done => { resolvers.push(done); }) : { accounts: [] } });
  h.state.settingsTab = 'security';
  const loading = h.context.loadAccountSecurity();
  h.context.syncSessionHeader({ authenticated: true, username: 'other', role: 'viewer' });
  assert.equal(resolvers.length, 2);
  resolvers[1]({ sessions: [{ id: 'other-session' }] });
  await new Promise(resolve => setImmediate(resolve));
  resolvers[0]({ sessions: [{ id: 'private-session' }] });
  await loading;
  assert.equal(h.state.activeSessions[0].id, 'other-session');
  assert.equal(h.$('btn-refresh-sessions').disabled, false);
  assert.doesNotMatch(h.$('active-sessions').innerHTML, /private-session/);
});

test('device information is escaped and session load failures stay visible', async () => {
  const h = harness({ api: async () => { throw new Error('load failed'); } });
  await h.context.loadAccountSecurity();
  assert.equal(h.$('account-security-status').textContent, 'load failed');
  assert.equal(h.$('btn-refresh-sessions').disabled, false);
  h.state.activeSessions = [{ id: 'session', client_ip: '<img>', user_agent: '<script>alert(1)</script>', current: true, created_at: 'now', last_seen_at: 'now', expires_at: 'later' }];
  h.context.renderAccountSecurity();
  assert.doesNotMatch(h.$('active-sessions').innerHTML, /<script>|<img>/);
  assert.match(h.$('active-sessions').innerHTML, /&lt;script&gt;/);
  assert.match(h.$('active-sessions').innerHTML, /当前会话/);
});

test('revoking current session requires confirmation and deliberately signs out', async () => {
  const canceled = harness({ confirm: false });
  const button = { disabled: false };
  await canceled.context.revokeConsoleSession({ id: 'current', current: true }, button);
  assert.equal(canceled.requests.length, 0);
  assert.equal(canceled.signedOut, false);
  assert.equal(button.disabled, false);
  const accepted = harness();
  await accepted.context.revokeConsoleSession({ id: 'current', current: true }, button);
  assert.equal(accepted.requests[0].options.method, 'DELETE');
  assert.equal(accepted.signedOut, true);
});

test('account creation sends second verification and clears both passwords after saving', async () => {
  const h = harness();
  h.context.openConsoleAccount('create');
  assert.equal(h.$('console-account-username').required, true);
  assert.equal(h.$('console-account-password').required, true);
  assert.equal(h.$('console-account-role').value, 'viewer');
  h.$('console-account-username').value = 'reader';
  h.$('console-account-password').value = 'reader-password';
  h.$('console-actor-password').value = 'actor-password';
  await h.handlers.get('console-account-form:submit')({ preventDefault() {} });
  assert.deepEqual(JSON.parse(h.requests[0].options.body), { username: 'reader', password: 'reader-password', role: 'viewer', current_password: 'actor-password' });
  assert.equal(h.$('console-account-password').value, '');
  assert.equal(h.$('console-actor-password').value, '');
  assert.equal(h.state.accountAction, null);
});

test('changing own role returns to login and translated labels cover the new interface', async () => {
  const h = harness({ language: 'en' });
  h.context.openConsoleAccount('role', { id: 'self', username: 'admin', role: 'admin' });
  h.$('console-actor-password').value = 'password';
  await h.handlers.get('console-account-form:submit')({ preventDefault() {} });
  assert.equal(h.requests[0].url, '/api/admin/accounts/self/role');
  assert.equal(h.signedOut, true);
  for (const text of ['账号与会话', '活跃会话', '用户名', '只读运维', '当前会话', '新增控制台账号', '你的当前密码（二次验证）']) {
    assert.notEqual(h.context.t(text), text);
  }
});
