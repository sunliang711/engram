const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');
const appSource = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const i18nSource = fs.readFileSync(path.join(__dirname, '../ui/i18n.js'), 'utf8');

test('navigation and panes include Audit Center and Scheduled Tasks tabs', () => {
  assert.match(html, /class="hidden app-shell"/);
  assert.match(html, /<aside id="app-navigation" class="app-sidebar"/);
  assert.match(html, /class="nav-tabs sidebar-nav"/);
  assert.match(html, /class="nav-group-label"/);
  assert.match(html, /class="sidebar-user"/);
  assert.match(html, /<div class="app-main">/);
  assert.match(html, /id="btn-mobile-menu"/);
  assert.match(html, /id="topbar-current-page"/);
  assert.match(html, /data-tab="tab-audit"/);
  assert.match(html, /data-tab="tab-scheduler"/);
  assert.match(html, /data-tab="tab-settings"/);
  assert.match(html, /<section id="tab-audit" class="tab-pane hidden">/);
  assert.match(html, /<section id="tab-scheduler" class="tab-pane hidden">/);
  assert.match(html, /<section id="tab-settings" class="tab-pane hidden">/);
  assert.match(html, /id="safekeep-settings-form"/);
  assert.match(html, /id="sk-base-url"/);
  assert.match(html, /id="sk-api-key"/);
  assert.match(html, /id="audit-cleanup-modal"/);
  assert.match(html, /id="scheduler-config-modal"/);
});

test('SPA route parser resolves /audit and /scheduler paths', () => {
  const from = appSource.indexOf('  const TAB_PATHS = {');
  const to = appSource.indexOf('  // restoreLocation', from);
  assert.ok(from >= 0 && to > from, 'Missing routing block');
  const block = appSource.slice(from, to);

  const runRoute = (pathname) => {
    const context = vm.createContext({
      location: { pathname, search: '', hash: '' },
      URLSearchParams,
      state: {},
    });
    vm.runInContext(block, context);
    return context.parseRouteFromLocation();
  };

  assert.equal(runRoute('/audit').tab, 'tab-audit');
  assert.equal(runRoute('/scheduler').tab, 'tab-scheduler');
  assert.equal(runRoute('/settings').tab, 'tab-settings');
});

test('audit and scheduler renderers escape dynamic strings and translate in English', () => {
  const elements = new Map();
  const makeEl = () => ({
    innerHTML: '',
    textContent: '',
    value: '',
    checked: false,
    disabled: false,
    classList: { toggle() {}, add() {}, remove() {}, contains() { return false; } },
    querySelector() { return makeEl(); },
  });
  const $ = (id) => {
    if (!elements.has(id)) elements.set(id, makeEl());
    return elements.get(id);
  };

  const context = vm.createContext({
    $,
    window: { matchMedia: () => ({ matches: false }) },
    document: { documentElement: { dataset: {} }, querySelectorAll: () => [] },
    navigator: { language: 'en-US' },
    location: { search: '?lang=en', href: 'http://localhost/audit?lang=en' },
    URL,
    URLSearchParams,
    localStorage: { getItem: () => null, setItem() {} },
    state: {
      auditLogs: [{
        id: '1',
        created_at: '2026-11-20T00:00:00Z',
        source: 'mcp',
        action: '<img src=x onerror=alert(1)>',
        actor_name: 'Owner',
        agent_name: '<script>evil()</script>',
        resource_type: 'memory',
        resource_id: 'abcdef123456',
        status: 'success',
        duration_ms: 8,
      }],
      auditTotal: 1,
      auditPage: 1,
      auditPageSize: 20,
      schedulerTasks: [{
        name: 'audit_log_cleanup',
        display_name: '定时清理过期审计日志',
        description: '按配置的保留时长（retention）和执行间隔分批清理过期的审计日志记录，防止数据库体积无限增长。',
        enabled: true,
        interval_seconds: 3600,
        config: { retention: '30d', batch_size: 1000 },
        last_status: 'success',
        run_count: 2,
      }, {
        name: 'safekeep_migration_backup',
        display_name: '定时导出迁移包到 SafeKeep',
        description: '按配置的执行间隔自动导出 Engram 系统或知识库迁移包，并通过 SafeKeep API 上传至远程存储。',
        enabled: false,
        interval_seconds: 86400,
        config: { migration_mode: 'system', upload_mode: 'sync', remote_path: 'engram' },
        last_status: 'idle',
        run_count: 0,
      }],
    },
    escapeHtml: (s) => String(s ?? '').replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'),
    formatDate: (s) => String(s),
    api: async () => ({}),
    showToast: () => {},
    onSubmit: () => {},
  });

  vm.runInContext(i18nSource, context);
  context.t = context.window.EngramI18n.t;
  context.locale = context.window.EngramI18n.locale;

  const start = appSource.indexOf('  // Audit Center & Scheduled Tasks');
  const end = appSource.indexOf('  document.querySelectorAll(".preset-chips")', start);
  assert.ok(start >= 0 && end > start, 'Missing Audit & Scheduler UI block');
  vm.runInContext(appSource.slice(start, end), context);

  const logsTbody = makeEl();
  elements.set('audit-logs-table', { querySelector: () => logsTbody });
  context.renderAuditLogsTable();
  assert.doesNotMatch(logsTbody.innerHTML, /<script>|<img /);
  assert.match(logsTbody.innerHTML, /&lt;script&gt;evil\(\)&lt;\/script&gt;/);
  assert.match(logsTbody.innerHTML, /MCP client/);
  assert.match(logsTbody.innerHTML, /Details/);

  const schedTbody = makeEl();
  elements.set('scheduler-tasks-table', { querySelector: () => schedTbody });
  context.renderSchedulerTasks();
  assert.match(schedTbody.innerHTML, /Scheduled expired audit log cleanup/);
  assert.match(schedTbody.innerHTML, /Keep 30d/);
  assert.match(schedTbody.innerHTML, /Scheduled migration backup to SafeKeep/);
  assert.match(schedTbody.innerHTML, /btn-sched-toggle/);
  assert.match(schedTbody.innerHTML, />Disable</);
  assert.match(schedTbody.innerHTML, />Enable</);
  assert.match(schedTbody.innerHTML, /Configure/);
  assert.match(schedTbody.innerHTML, /Run now/);
});
