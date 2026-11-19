const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');

// Run production credential selection, reveal, input, and guide renderers without creating fixtures on a server.
function harness({ tokens = [], reveal = async id => ({ token: 'secret-' + id }), language = 'zh-CN' } = {}) {
  const elements = new Map(), requests = [];
  const accountMenu = {};
  const document = { activeElement: null, documentElement: { dataset: {} }, querySelectorAll: () => [], querySelector: () => accountMenu };
  const node = () => {
    const classes = new Set();
    return {
      value: '', dataset: {}, textContent: '', innerHTML: '', isConnected: true,
      classList: { add: name => classes.add(name), remove: name => classes.delete(name), contains: name => classes.has(name) },
      focus() { document.activeElement = this; },
      querySelector() { return this.child ||= node(); },
      querySelectorAll() { return []; },
      setCustomValidity() {},
      addEventListener(type, callback) { this[type] = callback; },
    };
  };
  document.body = node();
  const $ = id => {
    if (!elements.has(id)) elements.set(id, node());
    return elements.get(id);
  };
  const state = { tokens, revealedTokens: {}, clientConfigTokenID: '', clientConfigRequest: 0 };
  const context = vm.createContext({
    $, state, document, window: { location: { origin: 'https://memory.example.com' } },
    navigator: { language }, location: { search: '' }, URLSearchParams, URL,
    closeMemoryDetail() {}, renderView() {},
    api: async (url, options) => {
      const body = options.body ? JSON.parse(options.body) : {};
      requests.push({ url, method: options.method, body });
      return url === '/api/admin/logout' ? {} : reveal(body.id);
    },
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../ui/i18n.js'), 'utf8'), context);
  context.t = context.window.EngramI18n.t;
  const load = (start, end) => {
    const from = source.indexOf(start), to = source.indexOf(end, from);
    assert.ok(from >= 0 && to > from, `Missing app block ${start}`);
    vm.runInContext(source.slice(from, to), context);
  };
  load('  async function resolveTokenSecret(', '  // renderTokensTable');
  load('  function getMemoryInstructionSnippet(', '  // activeTab');
  load('  function shellQuote(', '  // updatePrincipalMode');
  load('  function escapeHtml(', '  // onSubmit');
  load('  $("guide-existing-token").addEventListener(', '  $("btn-qs-again").addEventListener(');
  load('  $("btn-logout").addEventListener(', '  onSubmit("quickstart-form"');
  return { $, state, requests, document, context };
}

const token = (id, overrides = {}) => ({ id, tenant_name: 'Team', principal_name: 'Owner', name: 'quickstart-owner', token_prefix: 'aim_' + id, can_reveal: true, ...overrides });
const defer = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};
const select = (h, id) => { h.$('guide-existing-token').value = id; return h.$('guide-existing-token').change(); };

// All three client templates must show the selected input or a placeholder, never a prior credential.
function assertGuides(h, expected) {
  for (const id of ['guide-codex-mcp', 'guide-claude-mcp', 'guide-claude-cli', 'guide-gemini-cli-mcp', 'guide-gemini-agy-mcp']) assert.ok(h.$(id).textContent.includes(expected), `${id} does not contain expected credential`);
}

test('overview configuration entry is independent of creation and works with no credentials', () => {
  assert.match(html, /type="button"[^>]+id="btn-view-client-config">查看客户端配置/);
  assert.match(html, /type="submit">创建密钥并配置/);
  const h = harness();
  h.$('btn-view-client-config').focus();
  h.$('btn-view-client-config').click();
  assert.deepEqual(h.requests, []);
  assertGuides(h, '<YOUR_API_TOKEN>');
  assert.equal(h.$('client-config-modal').classList.contains('hidden'), false);
  h.context.closeClientGuides();
  assert.equal(h.document.activeElement, h.$('btn-view-client-config'));
  h.$('btn-view-client-config').click();
  assert.deepEqual(h.requests, []);
});

test('same-name clients are distinguished by prefix and selection only reveals an existing key', async () => {
  const h = harness({ tokens: [token('A'), token('B'), token('revoked', { revoked_at: 'yesterday' })] });
  h.context.openClientGuides();
  assert.match(h.$('guide-existing-token').innerHTML, /Team \/ Owner \/ quickstart-owner · aim_A/);
  assert.match(h.$('guide-existing-token').innerHTML, /aim_B/);
  assert.doesNotMatch(h.$('guide-existing-token').innerHTML, /revoked|secret-/);
  await select(h, 'B');
  assert.deepEqual(h.requests, [{ url: '/api/admin/tokens/reveal', method: 'POST', body: { id: 'B' } }]);
  assertGuides(h, 'secret-B');
  h.context.closeClientGuides();
  h.context.openClientGuides();
  assertGuides(h, 'secret-B');
  assert.equal(h.requests.length, 1);
});

test('a member token action opens its own credential instead of the previous selection', async () => {
  const h = harness({ tokens: [token('A'), token('B')] });
  await h.context.openClientGuides(h.state.tokens[0]);
  await h.context.openClientGuides(h.state.tokens[1]);
  assert.equal(h.$('guide-existing-token').value, 'B');
  assertGuides(h, 'secret-B');
  assert.ok(h.requests.every(request => request.url === '/api/admin/tokens/reveal'));
});

test('out-of-order reveals cannot replace the later selection', async () => {
  const a = defer(), b = defer();
  const h = harness({ tokens: [token('A'), token('B')], reveal: id => id === 'A' ? a.promise : b.promise });
  h.context.openClientGuides();
  const first = select(h, 'A'), second = select(h, 'B');
  assertGuides(h, '<YOUR_API_TOKEN>');
  b.resolve({ token: 'new-B' });
  await second;
  a.resolve({ token: 'old-A' });
  await first;
  assert.equal(h.$('guide-existing-token').value, 'B');
  assertGuides(h, 'new-B');
  assert.doesNotMatch(h.$('guide-codex-mcp').textContent, /old-A/);
});

test('failure clears the previous key and explains manual recovery without issuing credentials', async () => {
  const h = harness({ tokens: [token('A'), token('B')], reveal: async id => {
    if (id === 'B') throw new Error('unavailable');
    return { token: 'secret-A' };
  }, language: 'en' });
  h.context.openClientGuides();
  await select(h, 'A');
  await select(h, 'B');
  assert.equal(h.$('guide-token-input').value, '');
  assertGuides(h, '<YOUR_API_TOKEN>');
  assert.match(h.$('guide-token-hint').textContent, /Paste it to view configuration/);
  assert.ok(h.requests.every(request => request.url === '/api/admin/tokens/reveal'));
});

test('unrecoverable keys allow manual input and manual edits supersede pending selection', async () => {
  const pending = defer();
  const h = harness({ tokens: [token('old', { can_reveal: false }), token('pending')], reveal: () => pending.promise });
  h.context.openClientGuides();
  await select(h, 'old');
  assert.deepEqual(h.requests, []);
  assert.match(h.$('guide-token-hint').textContent, /粘贴原密钥/);
  const selecting = select(h, 'pending');
  h.$('guide-token-input').value = 'manual-secret';
  h.$('guide-token-input').input();
  pending.resolve({ token: 'stale-secret' });
  await selecting;
  assert.equal(h.$('guide-existing-token').value, '');
  assert.equal(h.$('guide-token-hint').textContent, '');
  assertGuides(h, 'manual-secret');
  h.context.closeClientGuides();
  h.context.openClientGuides();
  assertGuides(h, 'manual-secret');
});

test('refreshing a revoked or missing candidate clears selection and ignores any pending reveal', async () => {
  for (const disappeared of [false, true]) {
    const pending = defer();
    const h = harness({ tokens: [token('A')], reveal: () => pending.promise });
    h.context.openClientGuides();
    const selecting = select(h, 'A');
    if (disappeared) h.state.tokens = [];
    else h.state.tokens[0].revoked_at = 'now';
    h.context.renderClientTokenPicker();
    pending.resolve({ token: 'invalid-A' });
    await selecting;
    assert.equal(h.$('guide-existing-token').value, '');
    assert.equal(h.$('guide-token-input').value, '');
    assert.match(h.$('guide-token-hint').textContent, /已失效或已吊销/);
    assertGuides(h, '<YOUR_API_TOKEN>');
  }
});

test('client names in selector labels are escaped and never contain secret values', () => {
  const h = harness({ tokens: [token('A', { name: '<img src=x onerror="alert(1)">' })] });
  h.context.openClientGuides();
  assert.match(h.$('guide-existing-token').innerHTML, /&lt;img/);
  assert.doesNotMatch(h.$('guide-existing-token').innerHTML, /<img|secret-A/);
});


test('a superseded failure cannot clear the later successful credential', async () => {
  const pending = defer();
  const h = harness({ tokens: [token('A'), token('B')], reveal: id => id === 'A' ? pending.promise : { token: 'current-B' } });
  h.context.openClientGuides();
  const first = select(h, 'A');
  await select(h, 'B');
  pending.reject(new Error('late failure'));
  await first;
  assertGuides(h, 'current-B');
  assert.equal(h.$('guide-token-hint').textContent, '');
});

test('refreshing a credential after its secret was loaded clears every guide when it is revoked', async () => {
  const h = harness({ tokens: [token('A')] });
  h.context.openClientGuides();
  await select(h, 'A');
  assertGuides(h, 'secret-A');
  h.$('mcp-check-result').textContent = 'Connected';
  h.state.tokens[0].revoked_at = 'now';
  h.context.renderClientTokenPicker();
  assert.equal(h.$('mcp-check-result').textContent, '');
  assert.equal(h.$('guide-existing-token').value, '');
  assert.equal(h.$('guide-token-input').value, '');
  assertGuides(h, '<YOUR_API_TOKEN>');
});

test('closing configuration ignores a pending reveal without reopening or updating its fields', async () => {
  const pending = defer();
  const h = harness({ tokens: [token('A')], reveal: () => pending.promise });
  h.context.openClientGuides();
  const selecting = select(h, 'A');
  h.context.closeClientGuides();
  pending.resolve({ token: 'late-A' });
  await selecting;
  assert.equal(h.$('client-config-modal').classList.contains('hidden'), true);
  assert.equal(h.$('guide-token-input').value, '');
  assertGuides(h, '<YOUR_API_TOKEN>');
});

test('logout cancels a pending reveal and prevents it from restoring cached secret material', async () => {
  const pending = defer();
  const h = harness({ tokens: [token('A')], reveal: () => pending.promise });
  h.context.openClientGuides();
  const selecting = select(h, 'A');
  await h.$('btn-logout').click();
  pending.resolve({ token: 'late-A' });
  await selecting;
  assert.equal(h.$('client-config-modal').classList.contains('hidden'), true);
  assert.equal(h.$('guide-token-input').value, '');
  assert.equal(h.state.clientConfigTokenID, '');
  assert.equal(h.state.revealedTokens.A, undefined);
  assertGuides(h, '<YOUR_API_TOKEN>');
});
