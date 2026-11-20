const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');

// calendarHarness restores production calendar controls and captures their enabled state.
function calendarHarness(tasks = []) {
  const nodes = new Map();
  const groups = {
    'sched-cfg-calendar-fields': ['sched-cfg-minute', 'sched-cfg-time', 'sched-cfg-weekday', 'sched-cfg-monthday'],
    'sched-cfg-minute-group': ['sched-cfg-minute'], 'sched-cfg-time-group': ['sched-cfg-time'],
    'sched-cfg-weekday-group': ['sched-cfg-weekday'], 'sched-cfg-monthday-group': ['sched-cfg-monthday'],
    'sched-cfg-cron-group': ['sched-cfg-cron'], 'sched-cfg-timezone-group': ['sched-cfg-timezone'],
    'sched-cfg-interval-group': ['sched-cfg-interval'],
  };
  const $ = id => {
    if (!nodes.has(id)) nodes.set(id, {
      value: '', textContent: '', classList: { toggle(name, hidden) { this[name] = hidden; }, remove() {} },
      querySelectorAll: () => (groups[id] || []).map($),
    });
    return nodes.get(id);
  };
  const context = vm.createContext({
    $, state: { schedulerTasks: tasks },
    t: value => value, formatDurationSeconds: value => `${value}s`,
  });
  const start = source.indexOf('  // Audit Center & Scheduled Tasks');
  const end = source.indexOf('  document.querySelectorAll(".preset-chips")', start);
  vm.runInContext(source.slice(start, end), context);
  return { $, context };
}

test('calendar presets produce different Cron rules and validate only visible controls', () => {
  const h = calendarHarness();
  h.$('sched-cfg-time').value = '09:35';
  h.$('sched-cfg-minute').value = '10';
  h.$('sched-cfg-weekday').value = '3';
  h.$('sched-cfg-monthday').value = '31';
  h.$('sched-cfg-timezone').value = 'Asia/Shanghai';
  h.$('sched-cfg-cron').value = '*/15 9-17 * * 1-5';
  h.$('sched-cfg-interval').value = '30m';
  for (const [kind, expected] of [['hourly', '10 * * * *'], ['daily', '35 9 * * *'], ['weekly', '35 9 * * 3'], ['monthly', '35 9 31 * *'], ['cron', '*/15 9-17 * * 1-5']]) {
    h.$('sched-cfg-schedule-type').value = kind;
    h.context.updateScheduleFields();
    const payload = h.context.calendarSchedulePayload();
    assert.equal(payload.schedule_type, kind);
    assert.equal(payload.cron_expression, expected);
    assert.equal(payload.timezone, 'Asia/Shanghai');
    assert.equal(Object.hasOwn(payload, 'interval'), false);
    assert.equal(h.$('sched-cfg-interval').disabled, true);
    assert.equal(h.$('sched-cfg-cron').required, kind === 'cron');
    assert.equal(h.$('sched-cfg-time').required, ['daily', 'weekly', 'monthly'].includes(kind));
  }
  h.$('sched-cfg-schedule-type').value = 'interval';
  h.context.updateScheduleFields();
  assert.equal(h.context.calendarSchedulePayload().interval, '30m');
  assert.equal(h.$('sched-cfg-timezone').required, false);
  assert.equal(h.$('sched-cfg-interval').disabled, false);
});

test('saved calendar and legacy schedules reopen without silently converting their rules', () => {
  const tasks = [
    { name: 'monthly', schedule_type: 'monthly', cron_expression: '5 8 31 * *', timezone: 'UTC', interval_seconds: 3600 },
    { name: 'custom', schedule_type: 'cron', cron_expression: '*/15 9-17 * * 1-5', timezone: 'America/New_York', interval_seconds: 3600 },
    { name: 'legacy', interval_seconds: 900 },
  ];
  const h = calendarHarness(tasks);
  h.context.openSchedulerConfigModal('monthly');
  assert.equal(h.$('sched-cfg-time').value, '08:05');
  assert.equal(h.$('sched-cfg-monthday').value, '31');
  assert.equal(h.context.calendarSchedulePayload().cron_expression, tasks[0].cron_expression);
  assert.match(h.context.formatTaskSchedule(tasks[0]), /每月.*5 8 31.*UTC/);
  h.context.openSchedulerConfigModal('custom');
  assert.equal(h.context.calendarSchedulePayload().cron_expression, tasks[1].cron_expression);
  assert.equal(h.context.calendarSchedulePayload().timezone, 'America/New_York');
  h.context.openSchedulerConfigModal('legacy');
  assert.equal(h.$('sched-cfg-schedule-type').value, 'interval');
  assert.equal(h.context.calendarSchedulePayload().interval, '15m');
  for (const kind of ['hourly', 'daily', 'weekly', 'monthly', 'cron']) assert.ok(html.includes(`value="${kind}"`));
});
