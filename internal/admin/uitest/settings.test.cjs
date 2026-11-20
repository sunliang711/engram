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
  const buttons = ['backup', 'appearance', 'security'].map(name => {
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
  h.context.setSettingsTab('appearance');
  assert.equal(h.location.search, '?lang=en&section=appearance');
  assert.equal(h.context.parseRouteFromLocation().queryParams.get('section'), 'appearance');
  assert.equal(h.$('settings-panel-backup').classList.contains('hidden'), true);
  assert.equal(h.$('settings-panel-appearance').classList.contains('hidden'), false);
  assert.equal(h.buttons[1].attributes['aria-selected'], 'true');
  assert.equal(h.buttons[1].tabIndex, 0);
  assert.equal(h.buttons[0].tabIndex, -1);
  assert.equal(h.$('sk-base-url').value, 'https://unsaved.example.com');
  assert.deepEqual(h.resets, ['sk-api-key', 'sk-backup-passphrase']);
  h.context.setSettingsTab('invalid');
  assert.equal(h.state.settingsTab, 'backup');
  assert.equal(h.location.search, '?lang=en');
});

test('keyboard tab navigation wraps and supports Home and End', () => {
  const h = settingsHarness();
  const press = (index, key) => h.buttons[index].events.keydown({ key, preventDefault() {} });
  press(0, 'ArrowLeft');
  assert.equal(h.state.settingsTab, 'security');
  assert.equal(h.document.activeElement, h.buttons[2]);
  press(2, 'ArrowRight');
  assert.equal(h.state.settingsTab, 'backup');
  press(0, 'End');
  assert.equal(h.state.settingsTab, 'security');
  press(2, 'Home');
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
