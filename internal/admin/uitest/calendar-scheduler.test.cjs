const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');
const times = ['2026-11-20T00:00:00Z', '2026-11-21T00:00:00Z', '2026-11-22T00:00:00Z', '2026-11-23T00:00:00Z', '2026-11-24T00:00:00Z'];
const deferred = () => { let resolve, reject; const promise = new Promise((yes, no) => { resolve = yes; reject = no; }); return { promise, resolve, reject }; };

// calendarHarness runs production drawer, preview and history functions with controllable responses.
function calendarHarness(tasks = [], preview = async () => ({ next_runs: times }), history = async () => ({ items: [], total: 0 })) {
  const nodes = new Map(), calls = [];
  const groups = { 'sched-cfg-cron-group': ['sched-cfg-cron'], 'sched-cfg-timezone-group': ['sched-cfg-timezone'], 'sched-cfg-interval-group': ['sched-cfg-interval'] };
  const document = { activeElement: null };
  const $ = id => {
    if (!nodes.has(id)) {
      const classes = new Set(id.endsWith('-modal') ? ['hidden'] : []);
      nodes.set(id, {
        value: '', textContent: '', innerHTML: '', isConnected: true,
        classList: { toggle(name, value) { if (value) classes.add(name); else classes.delete(name); }, add: name => classes.add(name), remove: name => classes.delete(name), contains: name => classes.has(name) },
        querySelectorAll: () => (groups[id] || []).map($),
        focus() { document.activeElement = this; },
      });
    }
    return nodes.get(id);
  };
  document.body = $('body');
  document.activeElement = $('opener');
  const context = vm.createContext({
    $, document, state: { schedulerTasks: tasks, schedulerRunsPageSize: 20 }, locale: 'zh-CN',
    t: (parts, ...values) => Array.isArray(parts) ? parts.map((part, i) => part + (values[i] ?? '')).join('') : parts,
    formatDurationSeconds: value => `${value}s`, formatDate: value => value,
    escapeHtml: value => String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;'),
    URLSearchParams, setTimeout, clearTimeout,
    api: async (url, options = {}) => {
      const payload = options.body ? JSON.parse(options.body) : null;
      calls.push({ url, payload });
      return url.includes('/preview') ? preview(payload) : history(url);
    },
  });
  const start = source.indexOf('  // Audit Center & Scheduled Tasks');
  const end = source.indexOf('  document.querySelectorAll(".preset-chips")', start);
  vm.runInContext(source.slice(start, end), context);
  return { $, context, calls, state: context.state, document };
}

const task = (name = 'backup', overrides = {}) => ({ name, display_name: name, schedule_type: 'daily', cron_expression: '0 4 * * *', timezone: 'Asia/Singapore', interval_seconds: 3600, timeout_seconds: 600, ...overrides });

test('presets match the reference project and only custom schedules expose Cron input', () => {
  const h = calendarHarness();
  h.$('sched-cfg-timezone').value = 'UTC';
  h.$('sched-cfg-interval').value = '30m';
  for (const [kind, expression] of [['hourly', '0 * * * *'], ['daily', '0 4 * * *'], ['weekly', '0 5 * * 1'], ['monthly', '0 4 1 * *']]) {
    h.$('sched-cfg-schedule-type').value = kind;
    h.context.updateScheduleFields();
    assert.equal(h.context.calendarSchedulePayload().cron_expression, expression);
    assert.equal(h.$('sched-cfg-cron').required, false);
    assert.equal(h.$('sched-cfg-interval').disabled, true);
  }
  h.$('sched-cfg-schedule-type').value = 'cron';
  h.context.updateScheduleFields();
  h.$('sched-cfg-cron').value = '*/15 9-17 * * 1-5';
  assert.equal(h.context.calendarSchedulePayload().cron_expression, '*/15 9-17 * * 1-5');
  assert.equal(h.$('sched-cfg-cron').required, true);
  h.$('sched-cfg-schedule-type').value = 'interval';
  h.context.updateScheduleFields();
  assert.equal(h.context.calendarSchedulePayload().interval, '30m');
  assert.equal(h.$('sched-cfg-timezone').required, false);
});

test('editing preserves existing custom dates, time zones, timeouts and fixed intervals', async () => {
  const tasks = [task('monthly', { schedule_type: 'monthly', cron_expression: '5 8 31 * *', timezone: 'UTC', timeout_seconds: 120 }), task('legacy', { schedule_type: 'interval', interval_seconds: 900 })];
  const h = calendarHarness(tasks);
  await h.context.openSchedulerConfigModal('monthly');
  assert.equal(h.$('sched-cfg-schedule-type').value, 'cron');
  assert.equal(h.$('sched-cfg-timeout').value, 120);
  assert.equal(h.context.calendarSchedulePayload().cron_expression, tasks[0].cron_expression);
  assert.equal(h.context.calendarSchedulePayload().timezone, 'UTC');
  assert.equal((h.$('sched-cfg-next-runs').innerHTML.match(/<li>/g) || []).length, 5);
  assert.equal(h.$('btn-save-scheduler-config').disabled, false);
  await h.context.openSchedulerConfigModal('legacy');
  assert.equal(h.context.calendarSchedulePayload().interval, '15m');
  assert.match(html, /class="scheduler-drawer"/);
  assert.match(html, /id="scheduler-history-modal"/);
});

test('invalid preview blocks saving and late previews cannot replace a newer draft', async () => {
  const a = deferred(), b = deferred();
  let count = 0;
  const h = calendarHarness([task()], () => (++count === 1 ? a.promise : b.promise));
  const first = h.context.openSchedulerConfigModal('backup');
  h.$('sched-cfg-schedule-type').value = 'cron';
  h.$('sched-cfg-cron').value = 'bad cron';
  const second = h.context.requestSchedulePreview();
  b.reject(new Error('invalid cron'));
  await second;
  a.resolve({ next_runs: times });
  await first;
  assert.equal(h.$('btn-save-scheduler-config').disabled, true);
  assert.equal(h.$('sched-cfg-preview-status').textContent, 'invalid cron');
  assert.equal(h.$('sched-cfg-next-runs').innerHTML, '');
});

test('closing the drawer cancels pending preview and restores the invoking control', async () => {
  const pending = deferred();
  const h = calendarHarness([task()], () => pending.promise);
  const opener = h.document.activeElement;
  const opened = h.context.openSchedulerConfigModal('backup');
  h.context.closeSchedulerConfig();
  pending.resolve({ next_runs: times });
  await opened;
  assert.equal(h.$('scheduler-config-modal').classList.contains('hidden'), true);
  assert.equal(h.state.schedulerPreviewValid, false);
  assert.equal(h.$('sched-cfg-next-runs').innerHTML, '');
  assert.equal(h.document.activeElement, opener);
});

test('history responses stay scoped to their task and escape result details', async () => {
  const a = deferred();
  const h = calendarHarness([task('A'), task('B')], undefined, url => url.includes('task_name=A') ? a.promise : Promise.resolve({ items: [{ status: 'timeout', trigger_type: 'manual', started_at: times[0], summary: '<img src=x>', error_message: '<script>x</script>', details: { output: '<b>unsafe</b>' } }], total: 1 }));
  // Use the same escaping helper as production renderers.
  h.context.escapeHtml = value => String(value).replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;');
  const first = h.context.openSchedulerHistory('A');
  await h.context.openSchedulerHistory('B');
  a.resolve({ items: [{ summary: 'wrong task' }], total: 1 });
  await first;
  assert.ok(h.$('scheduler-history-list').innerHTML.includes('&lt;img'));
  assert.ok(!h.$('scheduler-history-list').innerHTML.includes('<script>'));
  assert.ok(!h.$('scheduler-history-list').innerHTML.includes('wrong task'));
  assert.ok(h.$('scheduler-history-list').innerHTML.includes('超时'));
  h.context.closeSchedulerHistory();
  assert.equal(h.state.schedulerHistoryTask, '');
});

test('common Cron expressions get readable descriptions and complex rules keep their raw expression', () => {
  const h = calendarHarness();
  for (const [expression, description] of Object.entries({ '* * * * *': '每分钟', '*/5 * * * *': '每 5 分钟', '0 * * * *': '每小时整点', '0 4 * * *': '每天 04:00', '0 5 * * 1': '每周一 05:00', '0 9 * * 1-5': '每周一至周五 09:00', '30 4 1,15 * *': '每月 1 日、15 日 04:30' })) assert.equal(h.context.describeTaskCron(expression), description);
  for (const expression of ['0 4 1 * 1', '0 4 * 6 *', '*/5 1-3 * * *']) assert.equal(h.context.describeTaskCron(expression), null);
});
