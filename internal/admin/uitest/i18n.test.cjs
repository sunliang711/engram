const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/i18n.js'), 'utf8');
const markdown = fs.readFileSync(path.join(__dirname, '../ui/markdown.js'), 'utf8');

function browser({ language = 'zh-CN', light = false, saved = {}, search = '', blocked = false } = {}) {
  const storage = new Map(Object.entries(saved));
  const controls = [{ value: '' }, { value: '' }];
  const location = { search, href: `http://localhost/${search}#tab=tab-memories&page=2`, assign(value) { this.assigned = value; } };
  const context = vm.createContext({
    window: { matchMedia: () => ({ matches: light }) },
    document: { documentElement: { dataset: {} }, querySelectorAll: () => controls },
    navigator: { language }, location, URL, URLSearchParams,
    localStorage: {
      getItem(key) { if (blocked) throw new Error('Storage unavailable'); return storage.get(key); },
      setItem(key, value) { if (blocked) throw new Error('Storage unavailable'); storage.set(key, value); },
    },
  });
  vm.runInContext(source, context);
  return { context, storage, controls, location, ui: context.window.EngramI18n };
}

test('browser defaults and saved preferences select valid languages and themes', () => {
  const defaults = browser({ language: 'en-US', light: true });
  assert.equal(defaults.ui.locale, 'en');
  assert.equal(defaults.context.document.documentElement.dataset.theme, 'light');
  const saved = browser({ language: 'en-US', light: true, saved: { 'engram.language': 'zh-CN', 'engram.theme': 'dark' } });
  assert.equal(saved.ui.locale, 'zh-CN');
  assert.equal(saved.context.document.documentElement.dataset.theme, 'dark');
  const invalid = browser({ saved: { 'engram.language': 'invalid', 'engram.theme': 'invalid' } });
  assert.equal(invalid.ui.locale, 'zh-CN');
  assert.equal(invalid.context.document.documentElement.dataset.theme, 'dark');
});

test('translations preserve interpolation values and unknown user content', () => {
  const { ui } = browser({ language: 'en' });
  assert.equal(ui.t('正常'), 'Active');
  assert.equal(ui.t('Custom category'), 'Custom category');
  const title = '正常 {1} <script>alert(1)</script>';
  assert.equal(ui.t`「${title}」${'pinned'}`, `“${title}” pinned`);
  assert.equal(ui.t`第 ${2} / ${5} 页`, 'Page 2 / 5');
  assert.equal(browser().ui.t`第 ${2} / ${5} 页`, '第 2 / 5 页');
});

test('theme changes update every control and persist without navigating', () => {
  const { ui, controls, storage, location, context } = browser();
  ui.setTheme('light');
  assert.equal(context.document.documentElement.dataset.theme, 'light');
  assert.ok(controls.every(control => control.value === 'light'));
  assert.equal(storage.get('engram.theme'), 'light');
  assert.equal(location.assigned, undefined);
  ui.setTheme('invalid');
  assert.equal(context.document.documentElement.dataset.theme, 'light');
});

test('language links preserve navigation and work when storage is unavailable', () => {
  const { ui, location } = browser({ blocked: true });
  ui.setLanguage('en');
  const target = new URL(location.assigned);
  assert.equal(target.searchParams.get('lang'), 'en');
  assert.equal(target.hash, '#tab=tab-memories&page=2');
  const restored = browser({ blocked: true, search: '?lang=en' });
  assert.equal(restored.ui.locale, 'en');
  assert.doesNotThrow(() => restored.ui.setTheme('light'));
  assert.equal(restored.context.document.documentElement.dataset.theme, 'light');
});

test('English Markdown controls leave Chinese memory content unchanged', () => {
  const { context } = browser({ language: 'en' });
  vm.runInContext(markdown, context);
  const renderer = context.window.EngramMarkdown;
  const result = renderer.render('正常\n\n```\n中文代码\n```');
  assert.match(result, /<p>正常<\/p>/);
  assert.match(result, /Copy code/);
  assert.match(result, /中文代码/);
  assert.match(renderer.render(''), /No content/);
  assert.match(renderer.diff('相同正文', '相同正文'), /No content changes/);
});
