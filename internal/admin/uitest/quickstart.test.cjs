const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');

// Execute the production submission, modal, and repeat-access handlers with observable DOM state.
function harness({ response = {}, gate, language = 'zh-CN' } = {}) {
  const elements = new Map();
  const document = { activeElement: null, documentElement: { dataset: {} } };
  const node = () => {
    const classes = new Set();
    return {
      value: '', checked: false, dataset: {}, textContent: '', innerHTML: '', disabled: false, isConnected: true,
      classList: {
        contains: name => classes.has(name),
        add: name => classes.add(name),
        remove: name => classes.delete(name),
        toggle(name, enabled) { if (enabled ?? !classes.has(name)) classes.add(name); else classes.delete(name); },
      },
      focus() { document.activeElement = this; },
      setAttribute(name, value) { this[name] = value; },
      removeAttribute(name) { delete this[name]; },
      querySelector() { return this.child ||= node(); },
      addEventListener(type, callback) { this[type] = callback; },
    };
  };
  document.body = node();
  const $ = id => {
    if (!elements.has(id)) elements.set(id, node());
    return elements.get(id);
  };
  $('qs-result').classList.add('hidden');
  $('qs-marker-row').classList.add('hidden');
  $('client-config-modal').classList.add('hidden');
  Object.entries({ 'qs-tenant': 'default', 'qs-subject': 'owner', 'qs-display': 'Owner', 'qs-tenant-role': 'global_writer' }).forEach(([id, value]) => { $(id).value = value; });
  const state = { revealedTokens: {}, tokens: [] };
  const requests = [], errors = [];
  const context = vm.createContext({
    $, state, document, window: {}, navigator: { language }, location: { search: '' }, URLSearchParams,
    clearFormErrors() {}, showToast() {}, showFormError: (_, error) => errors.push(error),
    renderClientGuides() {}, refreshAllData: async () => {},
    api: async (url, options) => {
      requests.push({ url, method: options.method, body: JSON.parse(options.body) });
      if (gate) await gate;
      return { token_id: 'token-id', token: 'aim_test', token_name: 'Laptop', ...response };
    },
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../ui/i18n.js'), 'utf8'), context);
  context.t = context.window.EngramI18n.t;
  const load = (start, end) => {
    const from = source.indexOf(start), to = source.indexOf(end, from);
    assert.ok(from >= 0 && to > from, `Missing app block ${start}`);
    vm.runInContext(source.slice(from, to), context);
  };
  load('  function escapeHtml(', '  // onSubmit');
  load('  function identifier(', '  // statusLabel');
  load('  function clientTokenContext(', '  // activeTab');
  load('  function onSubmit(', '  // Keep wide');
  load('  $("btn-qs-again").addEventListener(', '  onSubmit("login-form"');
  load('  onSubmit("quickstart-form"', '  onSubmit("create-tenant-form"');
  return { $, state, document, context, requests, errors, submit: () => $('quickstart-form').submit({ preventDefault() {} }) };
}

test('quickstart is a direct form with exactly one optional section and unchanged access defaults', () => {
  const panel = html.match(/<section[^>]+id="quickstart-panel"[\s\S]*?<\/section>/)?.[0];
  assert.ok(panel);
  assert.match(panel, /aria-labelledby="quickstart-title"/);
  assert.equal((panel.match(/<details\b/g) || []).length, 1);
  assert.doesNotMatch(panel, /qs-result-box|查看初始化详情/);
  const optional = panel.match(/<details[^>]+id="qs-options"[\s\S]*?<\/details>/)?.[0];
  assert.ok(optional);
  assert.doesNotMatch(optional.split('>')[0], /\bopen\b/);
  for (const id of ['qs-project', 'qs-remote', 'qs-workspace', 'qs-tenant-role', 'qs-create-project']) assert.ok(optional.includes(`id="${id}"`));
  for (const id of ['qs-tenant', 'qs-subject', 'qs-display', 'qs-client']) {
    assert.ok(panel.includes(`id="${id}"`));
    assert.ok(!optional.includes(`id="${id}"`));
  }
  assert.match(optional, /value="global_writer" selected/);
  assert.doesNotMatch(optional.match(/<input[^>]+id="qs-create-project"[^>]*>/)[0], /\bchecked\b/);
  assert.match(panel, /\.engram-project/);
});

test('successful generation opens configuration immediately and closing returns focus to its visible action', async () => {
  const h = harness();
  await h.submit();
  assert.deepEqual(h.errors, []);
  assert.equal(h.requests.length, 1);
  assert.deepEqual(h.requests[0], { url: '/api/admin/quickstart', method: 'POST', body: { tenant_name: 'default', principal_subject: 'owner', principal_display_name: 'Owner', tenant_role: 'global_writer', project_name: '', remote_url: '', workspace_path: '', client_name: '' } });
  assert.equal(h.$('quickstart-form').classList.contains('hidden'), true);
  assert.equal(h.$('qs-result').classList.contains('hidden'), false);
  assert.equal(h.$('client-config-modal').classList.contains('hidden'), false);
  assert.equal(h.$('guide-token-input').value, 'aim_test');
  assert.equal(h.document.activeElement, h.$('btn-close-client-config'));
  h.context.closeClientGuides();
  assert.equal(h.document.activeElement, h.$('btn-go-client-guides'));
  h.$('btn-go-client-guides').click();
  assert.equal(h.$('client-config-modal').classList.contains('hidden'), false);
  assert.equal(h.requests.length, 1);
});

test('marker-only setup retains a safe copy action and translated instructions for the project file', async () => {
  const marker = 'b19ad68d-177a-4816-99a8-cc66a8b431a0';
  const h = harness({ response: { project_marker: marker }, language: 'en' });
  await h.submit();
  assert.deepEqual(h.errors, []);
  assert.equal(h.$('qs-marker-row').classList.contains('hidden'), false);
  assert.ok(h.$('qs-marker').innerHTML.includes(`data-copy-value="${marker}"`));
  assert.match(h.$('qs-result-summary').textContent, /Reopen its configuration/);
  const label = html.match(/<span>(将此标记保存[^<]+)<\/span>/)?.[1];
  assert.ok(label);
  assert.match(h.context.window.EngramI18n.t(label), /Save this marker in a \.engram-project file/);
});

test('continuing access restores the form without issuing a token or resetting permission choices', async () => {
  const h = harness();
  h.$('qs-client').value = 'Laptop';
  h.$('qs-create-project').checked = true;
  await h.submit();
  h.context.closeClientGuides();
  h.$('btn-qs-again').click();
  assert.equal(h.$('qs-result').classList.contains('hidden'), true);
  assert.equal(h.$('quickstart-form').classList.contains('hidden'), false);
  assert.equal(h.$('qs-client').value, '');
  assert.equal(h.$('qs-create-project').checked, true);
  assert.equal(h.document.activeElement, h.$('qs-client'));
  assert.equal(h.requests.length, 1);
});

test('concurrent generation clicks still issue one credential through the real submission guard', async () => {
  let release;
  const gate = new Promise(resolve => { release = resolve; });
  const h = harness({ gate });
  const first = h.submit();
  await h.submit();
  assert.equal(h.requests.length, 1);
  assert.equal(h.$('quickstart-form').querySelector('button[type="submit"]').disabled, true);
  release();
  await first;
  assert.deepEqual(h.errors, []);
  assert.equal(h.requests.length, 1);
  assert.equal(h.$('quickstart-form').dataset.submitting, 'false');
  assert.equal(h.$('client-config-modal').classList.contains('hidden'), false);
});
