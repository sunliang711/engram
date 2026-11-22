const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');
const css = fs.readFileSync(path.join(__dirname, '../ui/styles.css'), 'utf8');
const appSource = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');

test('CSS preserves sticky topbar, aligned sidebar buttons, and unwrapped audit detail button', () => {
  assert.match(css, /\.topbar\s*\{[\s\S]*?position:\s*sticky;/);
  assert.doesNotMatch(css, /\.topbar\s*\{\s*position:\s*relative;/);
  assert.doesNotMatch(css, /\.tab-btn\s*\{\s*padding:\s*9px 20px;/);
  assert.match(css, /#audit-logs-table \.btn-audit-detail\s*\{[^}]*white-space:\s*nowrap;/);
  assert.match(css, /\.scheduler-plans-table \.btn-sched-run\[aria-busy="true"\]::after\s*\{\s*content:\s*none;\s*\}/);
});

test('HTML orders member client keys before project permissions and exposes P0-P2 controls', () => {
  const keysIdx = html.indexOf('id="member-tokens-table"');
  const permsIdx = html.indexOf('id="member-projects-table"');
  assert.ok(keysIdx > 0 && permsIdx > 0 && keysIdx < permsIdx, 'Client keys should appear above project permissions');
  for (const id of [
    'overview-health',
    'btn-overview-health-audit',
    'btn-overview-health-backup',
    'btn-member-projects-all-writer',
    'btn-save-all-member-projects',
    'project-filter-q',
    'btn-rename-project',
    'btn-delete-project',
    'remote-edit-modal',
    'btn-new-memory',
    'mem-filter-sort',
    'mem-filter-agent',
    'memory-bulk-bar',
    'btn-detail-prev',
    'btn-detail-next',
    'btn-detail-revisions',
    'btn-edit-memory',
    'memory-edit-form',
    'memory-create-modal',
    'btn-audit-stat-failed',
    'audit-filter-range',
    'sched-history-failed-only',
    'mig-safekeep-hint',
  ]) {
    assert.ok(html.includes(`id="${id}"`), `Expected element id="${id}" in index.html`);
  }
  assert.match(html, /id="create-tenant-card"[^>]*class="modal-backdrop hidden"/);
  assert.match(html, /id="tenant-forms-panel"[^>]*class="modal-backdrop hidden"/);
  assert.match(html, /id="project-forms-panel"[^>]*class="modal-backdrop hidden"/);
  assert.match(html, /id="btn-mig-import-submit"\s+disabled/);
});

test('audit duration formatter, timestamp localizer, and range filter params behave cleanly', () => {
  const nodes = new Map();
  const $ = (id) => {
    if (!nodes.has(id)) {
      const classes = new Set();
      nodes.set(id, {
        value: '',
        textContent: '',
        innerHTML: '',
        checked: false,
        classList: {
          toggle(n, v) { if (v) classes.add(n); else classes.delete(n); },
          add: (n) => classes.add(n),
          remove: (n) => classes.delete(n),
          contains: (n) => classes.has(n),
        },
        setAttribute() {},
      });
    }
    return nodes.get(id);
  };
  const ctx = vm.createContext({
    $,
    state: { auditPage: 1, auditPageSize: 20, schedulerRuns: [], schedulerRunsPage: 1, schedulerRunsPageSize: 20, schedulerRunsTotal: 0 },
    t: (parts, ...vals) => Array.isArray(parts) ? parts.map((p, i) => p + (vals[i] ?? '')).join('') : parts,
    formatDate: (v) => `LOCAL(${v})`,
    escapeHtml: (v) => String(v),
    URLSearchParams,
    Date,
  });
  const start = appSource.indexOf('  function formatDurationSeconds(sec)');
  const end = appSource.indexOf('  async function loadAuditStats()', start);
  vm.runInContext(appSource.slice(start, end), ctx);

  assert.equal(ctx.formatRetentionLabel('720h0m0s'), '30 天');
  assert.equal(ctx.formatRetentionLabel('24h0m0s'), '1 天');
  assert.equal(ctx.formatRetentionLabel('7d'), '7 天');
  assert.equal(ctx.formatRetentionLabel('30m0s'), '30 分钟');
  assert.equal(ctx.localizeEmbeddedTimestamps('截止时间 2026-10-22T13:10:32Z'), '截止时间 LOCAL(2026-10-22T13:10:32Z)');

  $('audit-filter-status').value = 'abnormal';
  $('audit-filter-range').value = '24h';
  const params = ctx.buildAuditFilterParams(true);
  assert.equal(params.get('status'), 'abnormal');
  assert.ok(params.get('from'), 'Expected RFC3339 from timestamp for 24h range');

  ctx.switchAuditSubTab('tasks');
  assert.equal($('audit-header-actions').classList.contains('hidden'), true);
  ctx.switchAuditSubTab('logs');
  assert.equal($('audit-header-actions').classList.contains('hidden'), false);
});
