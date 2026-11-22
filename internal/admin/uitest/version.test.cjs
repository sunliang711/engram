const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');
const appSource = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const i18nSource = fs.readFileSync(path.join(__dirname, '../ui/i18n.js'), 'utf8');

function renderHarness(language = 'zh-CN') {
  const elements = new Map();
  const makeNode = () => {
    const classes = new Set(['hidden']);
    return {
      textContent: '',
      title: '',
      value: '',
      required: false,
      classList: {
        add: name => classes.add(name),
        remove: name => classes.delete(name),
        toggle(name, enabled) {
          if (enabled) classes.add(name);
          else classes.delete(name);
        },
        contains: name => classes.has(name),
      },
    };
  };
  const $ = id => {
    if (!elements.has(id)) elements.set(id, makeNode());
    return elements.get(id);
  };
  const state = { appVersion: '' };
  const context = vm.createContext({
    $,
    state,
    window: { matchMedia: () => ({ matches: false }) },
    document: { documentElement: { dataset: {} }, querySelectorAll: () => [] },
    navigator: { language },
    location: { search: '', href: 'http://localhost/' },
    URL,
    URLSearchParams,
    localStorage: { getItem: () => null, setItem() {} },
    refreshAllData() {},
  });
  vm.runInContext(i18nSource, context);
  context.t = context.window.EngramI18n.t;
  vm.runInContext(appSource.slice(appSource.indexOf('  // Secret fields'), appSource.indexOf('  // showToast')), context);

  const from = appSource.indexOf('  // renderView');
  const to = appSource.indexOf('  async function checkSession(', from);
  assert.ok(from >= 0 && to > from, 'Missing renderView block');
  vm.runInContext(appSource.slice(from, to), context);
  return { $, state, context };
}

test('sidebar header renders the version badge in the top-left corner and topbar actions stay icon-only', () => {
  const sidebarHeader = html.match(/<div class="sidebar-header">([\s\S]*?)<\/div>\s*<nav/)?.[1];
  assert.ok(sidebarHeader, 'Missing sidebar header markup');
  assert.match(sidebarHeader, /id="app-version"/);
  assert.match(sidebarHeader, /class="version-badge hidden"/);

  const topbar = html.match(/<header class="topbar">([\s\S]*?)<\/header>/)?.[1];
  assert.ok(topbar, 'Missing topbar markup');
  assert.doesNotMatch(topbar, /id="app-version"/);

  const refreshBtn = topbar.match(/<button[^>]*id="btn-refresh"[^>]*>([\s\S]*?)<\/button>/)?.[1];
  assert.ok(refreshBtn, 'Missing refresh button markup');
  assert.match(refreshBtn.trim(), /^<svg[\s\S]*<\/svg>$/);

  const accountSummary = topbar.match(/<details class="account-menu">[\s\S]*?<summary[^>]*>([\s\S]*?)<\/summary>/)?.[1];
  assert.ok(accountSummary, 'Missing account menu summary markup');
  assert.match(accountSummary.trim(), /^<svg[\s\S]*<\/svg><span id="current-admin-user" class="sr-only">admin<\/span>$/);
});

test('renderView displays server version and preserves it across logout', () => {
  const h = renderHarness('en');
  h.context.renderView({ authenticated: false });
  assert.equal(h.$('app-version').textContent, '');
  assert.equal(h.$('app-version').classList.contains('hidden'), true);

  h.context.renderView({ authenticated: true, username: 'admin', must_change_password: false, version: 'v0.0.2' });
  assert.equal(h.$('app-version').textContent, 'v0.0.2');
  assert.equal(h.$('app-version').title, 'Server version: v0.0.2');
  assert.equal(h.$('app-version').classList.contains('hidden'), false);

  h.context.renderView({ authenticated: false });
  assert.equal(h.$('app-version').textContent, 'v0.0.2');
  assert.equal(h.$('app-version').classList.contains('hidden'), false);
});
