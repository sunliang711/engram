const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');

// Run production member-key actions, secret retrieval, creation, and configuration with observable DOM state.
function harness({ tokens = [], reveal = async id => ({ token: 'secret-' + id }), create, refresh, language = 'zh-CN' } = {}) {
  const elements = new Map(), requests = [], errors = [], notices = [];
  const document = { activeElement: null, documentElement: { dataset: {} }, querySelectorAll: () => [], querySelector: () => ({}) };
  const node = () => {
    const classes = new Set(), children = new Map();
    let previousHTML, buttons = [];
    return {
      value: '', type: 'password', dataset: {}, textContent: '', innerHTML: '', isConnected: true,
      classList: {
        add: name => classes.add(name), remove: name => classes.delete(name), contains: name => classes.has(name),
        toggle(name, enabled) { if (enabled) classes.add(name); else classes.delete(name); },
      },
      focus() { document.activeElement = this; },
      setAttribute(name, value) { this[name] = value; }, removeAttribute(name) { delete this[name]; },
      getAttribute(name) { return this[name]; },
      querySelector(selector) {
        const id = selector.match(/^\.btn-apply-token\[data-id="([^"]+)"\]$/)?.[1];
        if (id) return this.querySelector('tbody').querySelectorAll('.btn-apply-token').find(button => button.dataset.id === id) || null;
        if (!children.has(selector)) children.set(selector, node());
        return children.get(selector);
      },
      querySelectorAll(selector) {
        if (previousHTML !== this.innerHTML) {
          previousHTML = this.innerHTML;
          buttons = [...this.innerHTML.matchAll(/<button[^>]+class="([^"]+)"[^>]+data-id="([^"]+)"/g)].map(match => Object.assign(node(), { className: match[1], dataset: { id: match[2] } }));
        }
        return buttons.filter(button => button.className.split(' ').includes(selector.slice(1)));
      },
      setCustomValidity() {}, addEventListener(type, callback) { this[type] = callback; },
    };
  };
  document.body = node();
  const $ = id => {
    if (!elements.has(id)) elements.set(id, Object.assign(node(), { id }));
    return elements.get(id);
  };
  $('client-config-modal').classList.add('hidden');
  const principal = { id: 'member', display_name: 'Owner', identity_provider: 'api_token', tenant_roles: [{ tenant_id: 'tenant', role: 'none' }] };
  const state = { tokens, principals: [principal], memberTenant: 'tenant', selectedPrincipal: principal.id, locationRequest: 0, revealedTokens: {}, clientConfigTokenID: '', clientConfigRequest: 0 };
  let page = 'tab-tenants';
  const context = vm.createContext({
    $, state, document, window: { location: { origin: 'https://memory.example.com' } },
    navigator: { language }, location: { search: '' }, URLSearchParams, URL,
    activeTab: () => page, closeMemoryDetail() {}, renderView() {}, formatDate: value => value, relativeTime: () => 'now',
    showToast: message => notices.push(message), clearFormErrors() {}, showFormError: (_, error) => errors.push(error),
    api: async (url, options) => {
      const body = options.body ? JSON.parse(options.body) : {};
      requests.push({ url, method: options.method, body });
      if (url === '/api/admin/logout') return {};
      if (url === '/api/admin/tokens') return create ? create(body) : { id: 'new-key', token: 'created-secret' };
      assert.equal(url, '/api/admin/tokens/reveal');
      return reveal(body.id);
    },
    refreshAllData: async () => {
      if (refresh) await refresh(state);
      else state.tokens.push(token('new-key'));
      context.renderTokensTable();
      context.validateClientConfiguration();
    },
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../ui/i18n.js'), 'utf8'), context);
  context.t = context.window.EngramI18n.t;
  const load = (start, end) => {
    const from = source.indexOf(start), to = source.indexOf(end, from);
    assert.ok(from >= 0 && to > from, `Missing app block ${start}`);
    vm.runInContext(source.slice(from, to), context);
  };
  load('  // Secret fields', '  // showToast');
  load('  // isSystemPrincipal', '  // actorName');
  load('  function tenantMembership(', '  // renderTenantContext');
  load('  async function resolveTokenSecret(', '  // memoryFilters');
  load('  function getMemoryInstructionSnippet(', '  // activeTab');
  load('  function shellQuote(', '  // updatePrincipalMode');
  load('  function escapeHtml(', '  // Keep wide');
  load('  if ($("guide-token-input")) {\n    $("guide-token-input").addEventListener(', '  onSubmit("login-form"');
  load('  $("btn-logout").addEventListener(', '  onSubmit("create-tenant-form"');
  load('  onSubmit("member-token-form"', '  onSubmit("create-project-form"');
  context.renderTokensTable();
  return {
    $, state, requests, document, context, errors, notices,
    setPage(value) { page = value; state.locationRequest++; },
    row(id) { return $('member-tokens-table').querySelector(`.btn-apply-token[data-id="${id}"]`); },
    issue: () => $('member-token-form').submit({ preventDefault() {} }),
  };
}

const token = (id, overrides = {}) => ({ id, tenant_id: 'tenant', principal_id: 'member', tenant_name: 'Team', principal_name: 'Owner', name: 'Laptop', token_prefix: 'aim_' + id, can_reveal: true, created_at: 'today', ...overrides });
const defer = () => {
  let resolve, reject;
  const promise = new Promise((yes, no) => { resolve = yes; reject = no; });
  return { promise, resolve, reject };
};

// Every client template must use the fixed member-key input or a placeholder, never the previous key.
function assertGuides(h, expected) {
  const copied = h.context.clientGuideContents(true);
  for (const id of ['guide-codex-mcp', 'guide-claude-mcp', 'guide-claude-cli', 'guide-gemini-cli-mcp', 'guide-gemini-agy-mcp']) {
    assert.ok(copied[id].includes(expected), `${id} does not contain expected copied credential`);
    assert.ok(h.$(id).textContent.includes(expected === '<YOUR_API_TOKEN>' ? expected : '********'), `${id} does not mask the preview`);
  }
}

async function openRow(h, id) {
  const button = h.row(id);
  assert.ok(button, `Missing member configuration action ${id}`);
  await button.click();
}

test('configuration has no global selector and member rows expose every active key including unrecoverable ones', () => {
  assert.doesNotMatch(html, /guide-existing-token|btn-view-client-config/);
  const h = harness({ tokens: [token('A'), token('old', { can_reveal: false }), token('revoked', { revoked_at: 'yesterday' }), token('other', { principal_id: 'other-member' })] });
  assert.ok(h.row('A'));
  assert.ok(h.row('old'));
  const english = harness({ tokens: [token('A')], language: 'en' });
  assert.match(english.$('member-tokens-table').querySelector('tbody').innerHTML, />Copy<|>Copy<\/button>/);
  assert.equal(h.row('revoked'), null);
  assert.equal(h.row('other'), null);
});

test('member key row opens its exact secret and closing returns focus without creating data', async () => {
  const h = harness({ tokens: [token('A'), token('B')] });
  const opener = h.row('B');
  await openRow(h, 'B');
  assert.deepEqual(h.requests, [{ url: '/api/admin/tokens/reveal', method: 'POST', body: { id: 'B' } }]);
  assert.match(h.$('client-config-context').textContent, /Team \/ Owner \/ Laptop · aim_B/);
  assertGuides(h, 'secret-B');
  h.context.closeClientGuides();
  assert.equal(h.document.activeElement, opener);
  await openRow(h, 'B');
  assert.equal(h.requests.length, 1);
  assertGuides(h, 'secret-B');
});

test('client configuration previews mask secrets while copy contents retain the actual key', async () => {
  const h = harness({ tokens: [token('A')] });
  await openRow(h, 'A');
  assert.equal(h.$('guide-token-input').type, 'password');
  assertGuides(h, 'secret-A');
  await h.context.toggleSecretField(h.$('guide-token-input'));
  assert.equal(h.$('guide-token-input').type, 'text');
  assert.ok(h.$('guide-codex-mcp').textContent.includes('secret-A'));
  await h.context.toggleSecretField(h.$('guide-token-input'));
  assertGuides(h, 'secret-A');
  h.context.closeClientGuides();
  assert.equal(h.$('guide-token-input').value, '');
  assertGuides(h, '<YOUR_API_TOKEN>');
});

test('copy and configuration caches do not implicitly reveal member key rows', async () => {
  const h = harness({ tokens: [token('A')] });
  await h.context.resolveTokenSecret('A');
  h.context.renderTokensTable();
  const body = h.$('member-tokens-table').querySelector('tbody');
  assert.ok(body.innerHTML.includes('********'));
  assert.ok(!body.innerHTML.includes('secret-A'));
  await body.querySelectorAll('.btn-toggle-token')[0].click();
  assert.ok(body.innerHTML.includes('secret-A'));
  await body.querySelectorAll('.btn-toggle-token')[0].click();
  assert.ok(!body.innerHTML.includes('secret-A'));
});

test('configuration rejects another tenant, member, revoked key, missing token, or non-member page', async () => {
  const candidates = [token('other-member', { principal_id: 'other' }), token('other-tenant', { tenant_id: 'other' }), token('revoked', { revoked_at: 'now' })];
  for (const candidate of [...candidates, undefined]) {
    const h = harness({ tokens: candidates });
    await h.context.openClientGuides(candidate);
    assert.equal(h.requests.length, 0);
    assert.equal(h.$('client-config-modal').classList.contains('hidden'), true);
  }
  const h = harness({ tokens: [token('A')] });
  h.setPage('tab-overview');
  await h.context.openClientGuides(h.state.tokens[0]);
  assert.equal(h.requests.length, 0);
  h.setPage('tab-tenants');
  h.$('member-detail-card').classList.add('hidden');
  await h.context.openClientGuides(h.state.tokens[0]);
  assert.equal(h.requests.length, 0);
});

test('same-member keys cannot overwrite each other when reveals complete out of order', async () => {
  const a = defer(), b = defer();
  const h = harness({ tokens: [token('A'), token('B')], reveal: id => id === 'A' ? a.promise : b.promise });
  const first = openRow(h, 'A'), second = openRow(h, 'B');
  assertGuides(h, '<YOUR_API_TOKEN>');
  b.resolve({ token: 'current-B' });
  await second;
  a.resolve({ token: 'old-A' });
  await first;
  assert.equal(h.state.clientConfigTokenID, 'B');
  assertGuides(h, 'current-B');
});

test('unrecoverable active keys support manual paste within their fixed member context', async () => {
  const h = harness({ tokens: [token('old', { can_reveal: false })], language: 'en' });
  await openRow(h, 'old');
  assert.equal(h.requests.length, 0);
  assertGuides(h, '<YOUR_API_TOKEN>');
  assert.match(h.$('guide-token-hint').textContent, /Paste it to view configuration/);
  h.$('guide-token-input').value = 'manual-secret';
  h.$('guide-token-input').input();
  assert.equal(h.state.clientConfigTokenID, 'old');
  assertGuides(h, 'manual-secret');
});

test('manual input supersedes a pending reveal but remains subject to member invalidation', async () => {
  const pending = defer();
  const h = harness({ tokens: [token('A')], reveal: () => pending.promise });
  const opening = openRow(h, 'A');
  h.$('guide-token-input').value = 'manual-secret';
  h.$('guide-token-input').input();
  pending.resolve({ token: 'stale-secret' });
  await opening;
  assertGuides(h, 'manual-secret');
  h.state.selectedPrincipal = 'other';
  h.context.validateClientConfiguration();
  assert.equal(h.$('client-config-modal').classList.contains('hidden'), true);
  assert.equal(h.$('guide-token-input').value, '');
});

test('refreshing a revoked or missing key clears configuration and ignores pending reveal results', async () => {
  for (const disappeared of [false, true]) {
    const pending = defer();
    const h = harness({ tokens: [token('A')], reveal: () => pending.promise });
    const opening = openRow(h, 'A');
    h.$('mcp-check-result').textContent = 'Connected';
    if (disappeared) h.state.tokens = [];
    else h.state.tokens[0].revoked_at = 'now';
    h.context.validateClientConfiguration();
    pending.resolve({ token: 'invalid-A' });
    await opening;
    assert.equal(h.state.clientConfigTokenID, '');
    assert.equal(h.$('guide-token-input').value, '');
    assert.equal(h.$('mcp-check-result').textContent, '');
    assert.equal(h.$('client-config-modal').classList.contains('hidden'), true);
    assertGuides(h, '<YOUR_API_TOKEN>');
  }
});

test('failed retrieval clears a previous key without creating data', async () => {
  const h = harness({ tokens: [token('A'), token('B')], reveal: async id => {
    if (id === 'B') throw new Error('unavailable');
    return { token: 'secret-A' };
  } });
  await openRow(h, 'A');
  await openRow(h, 'B');
  assertGuides(h, '<YOUR_API_TOKEN>');
  assert.ok(h.requests.every(request => request.url === '/api/admin/tokens/reveal'));
});

test('close and logout cancel late secret responses without restoring hidden fields or logged-out cache', async () => {
  for (const logout of [false, true]) {
    const pending = defer();
    const h = harness({ tokens: [token('A')], reveal: () => pending.promise });
    const opening = openRow(h, 'A');
    if (logout) await h.$('btn-logout').click();
    else h.context.closeClientGuides();
    pending.resolve({ token: 'late-A' });
    await opening;
    assert.equal(h.$('client-config-modal').classList.contains('hidden'), true);
    assert.equal(h.$('guide-token-input').value, '');
    if (logout) assert.equal(h.state.revealedTokens.A, undefined);
  }
});

test('issuing a member key refreshes its row then opens configuration with the same secret and return focus', async () => {
  const h = harness();
  h.$('member-token-name').value = 'Laptop';
  await h.issue();
  assert.deepEqual(h.errors, []);
  assert.deepEqual(h.requests, [{ url: '/api/admin/tokens', method: 'POST', body: { tenant_id: 'tenant', principal_id: 'member', name: 'Laptop' } }]);
  assert.equal(h.$('client-config-modal').classList.contains('hidden'), false);
  assertGuides(h, 'created-secret');
  h.context.closeClientGuides();
  assert.equal(h.document.activeElement, h.row('new-key'));
});

test('an in-flight issuance cannot open configuration after changing member, tenant, page, or logging out', async () => {
  for (const change of ['member', 'tenant', 'page', 'hidden', 'logout']) {
    const pending = defer();
    const h = harness({ create: () => pending.promise });
    const issuing = h.issue();
    if (change === 'member') { h.state.selectedPrincipal = 'other'; h.state.locationRequest++; }
    if (change === 'tenant') { h.state.memberTenant = 'other'; h.state.locationRequest++; }
    if (change === 'page') h.setPage('tab-overview');
    if (change === 'hidden') h.$('app-view').classList.add('hidden');
    if (change === 'logout') await h.$('btn-logout').click();
    pending.resolve({ id: 'new-key', token: 'late-created-secret' });
    await issuing;
    assert.deepEqual(h.errors, []);
    assert.equal(h.$('client-config-modal').classList.contains('hidden'), true);
    assert.equal(h.state.revealedTokens['new-key'], undefined);
  }
});

test('changing context while issuance refreshes prevents an otherwise valid automatic open', async () => {
  const pending = defer();
  const h = harness({ refresh: async state => { await pending.promise; state.tokens.push(token('new-key')); } });
  const issuing = h.issue();
  await Promise.resolve();
  h.setPage('tab-projects');
  pending.resolve();
  await issuing;
  assert.equal(h.$('client-config-modal').classList.contains('hidden'), true);
});


test('an unfocused member key click restores focus after manual configuration of an unrecoverable key', async () => {
  const h = harness({ tokens: [token('old', { can_reveal: false })] });
  const opener = h.row('old');
  assert.equal(h.document.activeElement, null);
  await opener.click();
  h.$('guide-token-input').value = 'original-key';
  h.$('guide-token-input').input();
  h.context.closeClientGuides();
  assert.equal(h.document.activeElement, opener);
});

test('closing configuration finds the replacement key action when a refresh removed the original opener', async () => {
  const h = harness({ tokens: [token('A')] });
  const original = h.row('A');
  await original.click();
  original.isConnected = false;
  h.state.tokens[0].name = 'Updated client';
  h.context.renderTokensTable();
  const replacement = h.row('A');
  assert.notEqual(replacement, original);
  h.context.closeClientGuides();
  assert.equal(h.document.activeElement, replacement);
});
