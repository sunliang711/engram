const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');
const appSource = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const i18nSource = fs.readFileSync(path.join(__dirname, '../ui/i18n.js'), 'utf8');

function createSelect(initialOptions = []) {
  const options = initialOptions.map(([value, text]) => ({ value, text }));
  const el = {
    tagName: 'SELECT',
    value: options[0]?.value ?? '',
    dataset: {},
    options,
    _innerHTML: '',
    get innerHTML() { return this._innerHTML; },
    set innerHTML(markup) {
      this._innerHTML = markup;
      options.length = 0;
      const re = /<option value="([^"]*)">([^<]*)<\/option>/g;
      let m;
      while ((m = re.exec(markup)) !== null) {
        options.push({ value: m[1], text: m[2] });
      }
    },
    insertAdjacentHTML(_pos, markup) {
      this._innerHTML += markup;
      const re = /<option value="([^"]*)">([^<]*)<\/option>/g;
      let m;
      while ((m = re.exec(markup)) !== null) {
        options.push({ value: m[1], text: m[2] });
      }
    },
  };
  return el;
}

function harness() {
  const elements = new Map();
  const makeNode = (id = '') => ({
    id,
    value: '',
    dataset: {},
    innerHTML: '',
    textContent: '',
    classList: { toggle() {}, add() {}, remove() {} },
    focus() {},
    reset() {
      if (id === 'filter-memories-form') {
        elements.get('mem-filter-q').value = '';
        elements.get('mem-filter-tenant').value = '';
        elements.get('mem-filter-scope').value = '';
        elements.get('mem-filter-project').value = '';
        elements.get('mem-filter-category').value = '';
        elements.get('mem-filter-tag').value = '';
        elements.get('mem-filter-deleted').value = 'active';
      }
    },
    querySelector() { return this.child ||= makeNode(); },
    querySelectorAll() { return []; },
    addEventListener(type, callback) { this[type] = callback; },
  });

  const tenants = [
    { id: 'tenant-a', name: 'Alpha', status: 'active' },
    { id: 'tenant-b', name: 'Beta', status: 'active' },
  ];
  const projects = [
    { id: 'proj-a', tenant_id: 'tenant-a', tenant_name: 'Alpha', name: 'service-a', status: 'active', repositories: [{}], members: [] },
    { id: 'proj-b', tenant_id: 'tenant-b', tenant_name: 'Beta', name: 'service-b', status: 'disabled', repositories: [], members: [] },
  ];

  elements.set('mem-filter-tenant', createSelect([['', '全部'], ['tenant-a', 'Alpha'], ['tenant-b', 'Beta']]));
  elements.set('mem-filter-scope', createSelect([['', '全部'], ['global', '全局记忆'], ['project', '项目记忆']]));
  elements.set('mem-filter-project', createSelect([['', '全部'], ['proj-a', 'service-a（Alpha）'], ['proj-b', 'service-b（Beta）']]));
  elements.set('mem-filter-category', createSelect([['', '全部'], ['architecture', '架构']]));
  elements.set('mem-filter-tag', createSelect([['', '全部'], ['urgent', 'urgent']]));
  elements.set('mem-filter-deleted', createSelect([['active', '正常'], ['deleted', '回收站'], ['all', '全部（含已删除）']]));
  elements.set('mem-filter-q', makeNode('mem-filter-q'));
  elements.set('filter-memories-form', makeNode('filter-memories-form'));

  const $ = id => {
    if (!elements.has(id)) elements.set(id, makeNode(id));
    return elements.get(id);
  };

  const state = {
    tenants,
    projects,
    principals: [],
    selectedProject: 'proj-b',
    memoryPage: 4,
  };
  const calls = [];
  const context = vm.createContext({
    $,
    state,
    window: { scrollTo(x, y) { calls.push(['scrollTo', x, y]); } },
    t: (parts, ...values) => typeof parts === 'string' ? parts : parts.reduce((result, part, i) => result + part + (values[i] ?? ''), ''),
    escapeHtml: value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;'),
    statusLabel: value => value,
    clearFormErrors() {},
    visiblePrincipals: () => [],
    tenantMembership: () => null,
    switchMainTab(tab, updateURL) { calls.push(['switchMainTab', tab, updateURL]); },
    syncLocation(push) { calls.push(['syncLocation', push]); },
    loadMemories() { calls.push(['loadMemories']); },
  });

  const load = (start, end) => {
    const from = appSource.indexOf(start);
    const to = appSource.indexOf(end, from);
    assert.ok(from >= 0 && to > from, `Missing app block ${start}`);
    vm.runInContext(appSource.slice(from, to), context);
  };

  load('  // fillSelect rebuilds', '  // tenantMembership resolves');
  load('  // projectPrincipals includes', '  // renderProjectDetail binds');
  load('  // openProject resets', '  // resolveTokenSecret reads');
  load('  $("projects-table").addEventListener(', '  $("btn-project-list").addEventListener(');

  return { $, state, calls, context };
}

test('project list and detail views expose knowledge base jump buttons', () => {
  assert.match(html, /id="project-detail-view"[\s\S]*id="btn-open-project-memories"[^>]*>查看知识库<\/button>/);

  const h = harness();
  h.context.renderProjectsTable();
  const tbody = h.$('projects-table').querySelector('tbody').innerHTML;
  assert.match(tbody, /class="secondary small btn-open-project-memories" data-id="proj-a">知识库<\/button>/);
  assert.match(tbody, /class="secondary small btn-open-project" data-id="proj-a">管理<\/button>/);
  assert.match(tbody, /class="secondary small btn-open-project-memories" data-id="proj-b">知识库<\/button>/);
});

test('clicking a project knowledge button resets unrelated filters and navigates with one history entry', () => {
  const h = harness();
  h.$('mem-filter-tenant').value = 'tenant-a';
  h.$('mem-filter-project').value = 'proj-a';
  h.$('mem-filter-scope').value = 'global';
  h.$('mem-filter-category').value = 'architecture';
  h.$('mem-filter-tag').value = 'urgent';
  h.$('mem-filter-deleted').value = 'deleted';
  h.$('mem-filter-q').value = 'stale query';

  const memoriesBtn = { dataset: { id: 'proj-b' } };
  h.$('projects-table').click({
    target: { closest: selector => (selector === '.btn-open-project-memories' ? memoriesBtn : null) },
  });

  assert.equal(h.$('mem-filter-tenant').value, 'tenant-b');
  assert.equal(h.$('mem-filter-scope').value, 'project');
  assert.equal(h.$('mem-filter-project').value, 'proj-b');
  assert.deepEqual(h.$('mem-filter-project').options.map(o => o.value), ['', 'proj-b']);
  assert.equal(h.$('mem-filter-category').value, '');
  assert.equal(h.$('mem-filter-tag').value, '');
  assert.equal(h.$('mem-filter-deleted').value, 'active');
  assert.equal(h.$('mem-filter-q').value, '');
  assert.equal(h.state.memoryPage, 1);
  assert.deepEqual(h.calls, [
    ['switchMainTab', 'tab-memories', false],
    ['syncLocation', true],
    ['loadMemories'],
    ['scrollTo', 0, 0],
  ]);
});

test('project detail knowledge button jumps using selectedProject and ignores unknown ids', () => {
  const h = harness();
  h.state.selectedProject = 'proj-a';
  h.$('btn-open-project-memories').click();

  assert.equal(h.$('mem-filter-tenant').value, 'tenant-a');
  assert.equal(h.$('mem-filter-scope').value, 'project');
  assert.equal(h.$('mem-filter-project').value, 'proj-a');
  assert.equal(h.calls.length, 4);

  h.calls.length = 0;
  h.context.openProjectMemories('missing-project');
  assert.equal(h.calls.length, 0);
});

test('project knowledge jump button labels translate in English locale', () => {
  const context = vm.createContext({
    window: { matchMedia: () => ({ matches: false }) },
    document: { documentElement: { dataset: {} }, querySelectorAll: () => [] },
    navigator: { language: 'en-US' },
    location: { search: '', href: 'http://localhost/' },
    URL,
    URLSearchParams,
    localStorage: { getItem: () => null, setItem() {} },
  });
  vm.runInContext(i18nSource, context);
  assert.equal(context.window.EngramI18n.t('知识库'), 'Knowledge');
  assert.equal(context.window.EngramI18n.t('查看知识库'), 'View knowledge');
});
