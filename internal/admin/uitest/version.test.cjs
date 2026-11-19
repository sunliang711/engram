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

  const from = appSource.indexOf('  // renderView');
  const to = appSource.indexOf('  async function checkSession(', from);
  assert.ok(from >= 0 && to > from, 'Missing renderView block');
  vm.runInContext(appSource.slice(from, to), context);
  return { $, state, context };
}

test('topbar brand renders the version badge in the top-left corner', () => {
  const topbar = html.match(/<header class="topbar">[\s\S]*?<div class="brand">([\s\S]*?)<\/div>/)?.[1];
  assert.ok(topbar, 'Missing topbar brand markup');
  assert.match(topbar, /id="app-version"/);
  assert.match(topbar, /class="version-badge hidden"/);
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
