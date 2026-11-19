const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');

// Exercise the overview empty state and its real existing-tenant-form navigation handler.
function harness() {
  const elements = new Map(), navigations = [];
  let focused;
  const $ = id => {
    if (!elements.has(id)) {
      const classes = new Set(['hidden']);
      elements.set(id, {
        value: '', textContent: '',
        classList: { toggle(name, enabled) { if (enabled) classes.add(name); else classes.delete(name); }, remove: name => classes.delete(name), contains: name => classes.has(name) },
        addEventListener(type, callback) { this[type] = callback; },
        focus() { focused = id; },
      });
    }
    return elements.get(id);
  };
  const state = { tenants: [] };
  const context = vm.createContext({ $, state, t: value => value, openTenant: value => navigations.push(value) });
  const load = (start, end) => {
    const from = source.indexOf(start), to = source.indexOf(end, from);
    assert.ok(from >= 0 && to > from, `Missing app block ${start}`);
    vm.runInContext(source.slice(from, to), context);
  };
  load('  function renderOverviewSetup(', '  // renderTenantsTable');
  load('  $("btn-overview-create-tenant").addEventListener(', '  $("btn-cancel-tenant").addEventListener(');
  return { $, state, context, navigations, focused: () => focused };
}

test('overview keeps statistics and recent knowledge without a client creation or configuration flow', () => {
  const overview = html.match(/<section[^>]+id="tab-overview"[\s\S]*?<\/section>\s*<div class="card" id="recent-memories-card">[\s\S]*?<\/section>/)?.[0];
  assert.ok(overview);
  assert.match(overview, /id="stats-grid"/);
  assert.match(overview, /id="recent-memories"/);
  assert.match(overview, /id="btn-overview-create-tenant"/);
  assert.doesNotMatch(overview, /quickstart|qs-|btn-view-client-config|guide-existing-token|<form/);
  assert.doesNotMatch(source, /\/api\/admin\/quickstart|quickstartToken|\$\("qs-/);
  assert.doesNotMatch(html, /guide-existing-token|quickstart-panel/);
});

test('only an empty tenant list shows the create-tenant overview guidance', () => {
  const h = harness();
  h.context.renderOverviewSetup();
  assert.equal(h.$('overview-setup').classList.contains('hidden'), false);
  assert.match(h.$('overview-title').textContent, /第一个租户/);
  h.state.tenants = [{ id: 'tenant' }];
  h.context.renderOverviewSetup();
  assert.equal(h.$('overview-setup').classList.contains('hidden'), true);
});

test('overview creation action only opens the existing tenant form and focuses its name input', () => {
  const h = harness();
  h.$('btn-overview-create-tenant').click();
  assert.deepEqual(h.navigations, ['']);
  assert.equal(h.$('create-tenant-card').classList.contains('hidden'), false);
  assert.equal(h.focused(), 'tenant-name');
});
