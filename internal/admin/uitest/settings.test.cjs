const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');

// settingsHarness runs real navigation and preference handlers with independent browser storage.
function settingsHarness({ stored = '50', blocked = false, search = '?lang=en' } = {}) {
  const nodes = new Map(), storage = new Map([['engram.memoryPageSize', stored]]), resets = [];
  const location = { pathname: '/settings', search, hash: '' };
  const state = { settingsTab: 'backup', locationReady: true, locationRequest: 0 };
  const document = { activeElement: null };
  const $ = id => {
    if (!nodes.has(id)) {
      const classes = new Set(id.includes('panel-') && !id.endsWith('backup') ? ['hidden'] : []);
      nodes.set(id, { id, value: '', dataset: {}, events: {}, attributes: {},
        setAttribute(name, value) { this.attributes[name] = value; },
        addEventListener(name, fn) { this.events[name] = fn; },
        classList: { toggle(name, value) { if (value) classes.add(name); else classes.delete(name); }, contains: name => classes.has(name) },
        focus() { document.activeElement = this; }, click() { this.clicked = true; },
      });
    }
    return nodes.get(id);
  };
  const buttons = ['backup', 'memory', 'appearance', 'security'].map(name => {
    const button = $('settings-tab-' + name);
    button.dataset.settingsTab = name;
    return button;
  });
  document.querySelectorAll = () => buttons;
  const navigate = (_, __, url) => {
    const next = new URL(url, 'http://localhost');
    Object.assign(location, { pathname: next.pathname, search: next.search, hash: next.hash });
  };
  const context = vm.createContext({ $, state, document, location, history: { pushState: navigate, replaceState: navigate }, URL, URLSearchParams,
    activeTab: () => 'tab-settings', resetSecretField: node => resets.push(node.id),
    localStorage: { getItem(key) { if (blocked) throw new Error('Storage disabled'); return storage.get(key); }, setItem(key, value) { if (blocked) throw new Error('Storage disabled'); storage.set(key, value); } },
  });
  vm.runInContext(source.slice(source.indexOf('  // defaultMemoryPageSize'), source.indexOf('  // Secret fields')), context);
  vm.runInContext(source.slice(source.indexOf('  const TAB_PATHS ='), source.indexOf('  // restoreLocation')), context);
  vm.runInContext(source.slice(source.indexOf('  // Settings navigation supports'), source.indexOf('  // Event Listeners')), context);
  return { $, context, state, location, buttons, document, storage, resets };
}

test('settings tabs preserve language and drafts while keeping URL and accessible state aligned', () => {
  const h = settingsHarness();
  h.$('sk-base-url').value = 'https://unsaved.example.com';
  h.context.setSettingsTab('memory');
  assert.equal(h.location.search, '?lang=en&section=memory');
  assert.equal(h.$('settings-panel-memory').classList.contains('hidden'), false);
  assert.equal(h.buttons[1].attributes['aria-selected'], 'true');
  h.context.setSettingsTab('appearance');
  assert.equal(h.location.search, '?lang=en&section=appearance');
  assert.equal(h.context.parseRouteFromLocation().queryParams.get('section'), 'appearance');
  assert.equal(h.$('settings-panel-backup').classList.contains('hidden'), true);
  assert.equal(h.$('settings-panel-appearance').classList.contains('hidden'), false);
  assert.equal(h.buttons[2].attributes['aria-selected'], 'true');
  assert.equal(h.buttons[2].tabIndex, 0);
  assert.equal(h.buttons[0].tabIndex, -1);
  assert.equal(h.$('sk-base-url').value, 'https://unsaved.example.com');
  assert.deepEqual(h.resets, ['sk-api-key', 'sk-backup-passphrase']);
  h.context.setSettingsTab('invalid');
  assert.equal(h.state.settingsTab, 'backup');
  assert.equal(h.location.search, '?lang=en');
  // Deep-link section=memory must be accepted by the same route-allowed tab list as setSettingsTab.
  const restoreSnippet = source.slice(source.indexOf('    if (tab === "tab-settings") state.settingsTab ='), source.indexOf('    switchMainTab(', source.indexOf('    if (tab === "tab-settings") state.settingsTab =')));
  assert.match(restoreSnippet, /\["memory", "appearance", "security"\]/);
});

test('keyboard tab navigation wraps and supports Home and End', () => {
  const h = settingsHarness();
  const press = (index, key) => h.buttons[index].events.keydown({ key, preventDefault() {} });
  press(0, 'ArrowLeft');
  assert.equal(h.state.settingsTab, 'security');
  assert.equal(h.document.activeElement, h.buttons[3]);
  press(3, 'ArrowRight');
  assert.equal(h.state.settingsTab, 'backup');
  press(0, 'End');
  assert.equal(h.state.settingsTab, 'security');
  press(3, 'Home');
  assert.equal(h.state.settingsTab, 'backup');
});

test('memory page size preferences validate persisted values and tolerate blocked storage', () => {
  const h = settingsHarness();
  assert.equal(h.$('settings-memory-page-size').value, 50);
  h.context.saveMemoryPageSize(100);
  assert.equal(h.storage.get('engram.memoryPageSize'), '100');
  assert.equal(h.$('mem-page-size').value, 100);
  assert.equal(h.state.memoryPageSize, 100);
  h.context.saveMemoryPageSize(999);
  assert.equal(h.storage.get('engram.memoryPageSize'), '100');
  assert.equal(settingsHarness({ stored: '999' }).context.defaultMemoryPageSize(), 20);
  const blocked = settingsHarness({ blocked: true });
  assert.equal(blocked.context.defaultMemoryPageSize(), 20);
  blocked.context.saveMemoryPageSize(50);
  assert.equal(blocked.state.memoryPageSize, 50);
});

test('account security invokes the existing password change flow', () => {
  const h = settingsHarness();
  h.$('btn-settings-change-password').events.click();
  assert.equal(h.$('btn-open-change-pwd').clicked, true);
});

test('memory index settings validate bounds, track dirty state, reset to defaults, and protect active drafts', async () => {
  const nodes = new Map(), requests = [], notices = [];
  const node = (id) => {
    const classes = new Set();
    return {
      id, value: '', textContent: '', className: '', disabled: false, events: {},
      classList: { toggle(n, v) { if (v) classes.add(n); else classes.delete(n); } },
      addEventListener(n, fn) { this.events[n] = fn; },
      querySelector: () => $('btn-mi-save'),
      contains: () => false,
      dataset: {},
      setAttribute(n, v) { this[n] = v; },
      removeAttribute(n) { delete this[n]; },
    };
  };
  const $ = id => {
    if (!nodes.has(id)) nodes.set(id, node(id));
    return nodes.get(id);
  };
  const state = { memoryIndexSettings: null };
  let serverState = {
    discover_project_recent_limit: 50,
    discover_global_recent_limit: 5,
    discover_pinned_limit: 10,
    index_summary_max_runes: 100,
  };
  const context = vm.createContext({
    $, state, document: { activeElement: null },
    t: (parts, ...vals) => typeof parts === 'string' ? parts : parts.reduce((r, p, i) => r + p + (vals[i] ?? ''), ''),
    formatDate: v => v,
    showToast: m => notices.push(m),
    clearFormErrors() {}, showFormError: (_, err) => notices.push(err.message),
    api: async (url, opts) => {
      const parsedBody = opts?.body ? JSON.parse(opts.body) : null;
      requests.push({ url, method: opts?.method || 'GET', body: parsedBody });
      if (opts?.method === 'PUT') {
        serverState = { ...parsedBody, updated_at: '2026-11-23T09:00:00Z' };
      }
      return serverState;
    },
  });
  vm.runInContext(source.slice(source.indexOf('  function onSubmit('), source.indexOf('  // Keep wide tables')), context);
  vm.runInContext(source.slice(source.indexOf('  const DEFAULT_MEMORY_INDEX_SETTINGS ='), source.indexOf('  async function runManualSafekeepBackup(')), context);

  await context.loadMemoryIndexSettings();
  assert.equal($('mi-project-recent-limit').value, 50);
  assert.equal($('btn-mi-save').disabled, true);
  assert.equal($('mi-status-badge').textContent, '默认配置');
  assert.equal(context.canSaveMemoryIndexSettings(), false);

  // Out-of-range value cannot save.
  $('mi-project-recent-limit').value = '999';
  assert.equal(context.canSaveMemoryIndexSettings(), false);

  // Valid change enables save and submits PUT payload.
  $('mi-project-recent-limit').value = '150';
  $('mi-summary-max-runes').value = '120';
  assert.equal(context.canSaveMemoryIndexSettings(), true);
  $('memory-index-settings-form').events.input();
  assert.equal($('btn-mi-save').disabled, false);

  await $('memory-index-settings-form').events.submit({ preventDefault() {} });
  assert.equal(requests.at(-1).method, 'PUT');
  assert.deepEqual(requests.at(-1).body, {
    discover_project_recent_limit: 150,
    discover_global_recent_limit: 5,
    discover_pinned_limit: 10,
    index_summary_max_runes: 120,
  });
  assert.equal($('mi-status-badge').textContent, '已自定义');
  assert.equal($('btn-mi-save').disabled, true);

  // Background refresh must not clobber an unsaved draft (even if currently invalid/out-of-range).
  $('mi-project-recent-limit').value = '999';
  serverState = { ...serverState, discover_project_recent_limit: 80 };
  await context.loadMemoryIndexSettings();
  assert.equal($('mi-project-recent-limit').value, '999');

  // Reset button clears errors, restores defaults, and marks dirty so user can save defaults.
  $('btn-mi-reset').events.click();
  assert.equal($('mi-project-recent-limit').value, 50);
  assert.equal($('mi-summary-max-runes').value, 100);
  assert.equal($('btn-mi-save').disabled, false);
});
