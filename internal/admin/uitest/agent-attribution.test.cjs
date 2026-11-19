const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');

// Execute the production detail renderer and escaping functions with a minimal DOM fixture.
function harness(memory, revisions, language = 'zh-CN') {
  const elements = new Map();
  const node = () => ({
    innerHTML: '', textContent: '',
    classList: { contains: () => true, remove() {}, add() {}, toggle() {} },
    click() {}, focus() {},
  });
  const $ = id => {
    if (!elements.has(id)) elements.set(id, node());
    return elements.get(id);
  };
  const state = { detailRequest: 0, principals: [{ id: 'member', display_name: 'Member', identity_provider: 'api_token' }] };
  const errors = [];
  const context = vm.createContext({
    $, state,
    t: (parts, ...values) => typeof parts === 'string' ? parts : parts.reduce((result, part, i) => result + part + (values[i] ?? ''), ''),
    document: { activeElement: {}, body: node(), documentElement: { dataset: {} } },
    navigator: { language }, location: { search: '' }, URLSearchParams,
    window: { EngramMarkdown: { render: () => '', diff: () => '' } },
    formatDate: value => value, categoryLabel: value => value, identifier: () => '',
    showToast: message => errors.push(message),
    api: async url => ({ items: url.includes('/revisions?') ? revisions : [memory] }),
  });
  vm.runInContext(fs.readFileSync(path.join(__dirname, '../ui/i18n.js'), 'utf8'), context);
  context.t = context.window.EngramI18n.t;
  const load = (start, end) => {
    const from = source.indexOf(start);
    const to = source.indexOf(end, from);
    assert.ok(from >= 0 && to > from, `Missing app block ${start}`);
    vm.runInContext(source.slice(from, to), context);
  };
  load('  // isSystemPrincipal', '  // renderView');
  load('  function splitFrontmatter(', '  // memorySnippet');
  load('  const REVISION_FIELDS =', '  // closeMemoryDetail');
  load('  function escapeHtml(', '  // onSubmit');
  return { $, context, errors };
}

const memory = { id: 'memory', title: 'Knowledge', scope: 'global', category: 'note', version: 2, updated_at: 'now', content_md: 'body' };
const revision = (version, agent) => ({ version, created_at: 'then', operation: version === 1 ? 'create' : 'update', actor_id: 'member', snapshot: agent === undefined ? {} : { updated_agent: agent } });

test('detail shows creating and updating agents while history preserves each revision agent and member', async () => {
  const h = harness({ ...memory, created_agent: 'Codex', updated_agent: 'review-agent' }, [revision(2, 'review-agent'), revision(1, 'Codex')]);
  await h.context.viewMemoryDetail('memory', false);
  assert.deepEqual(h.errors, []);
  assert.match(h.$('detail-meta').innerHTML, /创建 Agent：Codex/);
  assert.match(h.$('detail-meta').innerHTML, /最后修改 Agent：review-agent/);
  const summaries = [...h.$('detail-revisions').innerHTML.matchAll(/<summary><strong>v(\d)[\s\S]*?<span>(.*?)<\/span><\/summary>/g)];
  assert.equal(summaries.length, 2);
  assert.match(summaries[0][2], /Member · review-agent/);
  assert.match(summaries[1][2], /Member · Codex/);
});

test('missing and empty attribution stays unknown without inheriting another revision or member name', async () => {
  const h = harness({ ...memory, created_agent: '', updated_agent: '' }, [revision(3, ''), revision(2), revision(1, 'Claude Code')]);
  await h.context.viewMemoryDetail('memory', false);
  assert.deepEqual(h.errors, []);
  assert.equal((h.$('detail-meta').innerHTML.match(/未知 Agent/g) || []).length, 2);
  const summaries = [...h.$('detail-revisions').innerHTML.matchAll(/<summary><strong>v\d[\s\S]*?<span>(.*?)<\/span><\/summary>/g)];
  assert.match(summaries[0][1], /Member · 未知 Agent/);
  assert.match(summaries[1][1], /Member · 未知 Agent/);
  assert.match(summaries[2][1], /Member · Claude Code/);
  assert.equal(h.context.agentName(undefined), '未知 Agent');
  assert.equal(h.context.agentName('   '), '未知 Agent');
});

test('self-reported current and historical agent strings are escaped before entering markup', async () => {
  const payload = '<img src=x onerror="alert(1)"><script>alert(2)</script>';
  const h = harness({ ...memory, created_agent: payload, updated_agent: payload }, [revision(1, payload)]);
  await h.context.viewMemoryDetail('memory', false);
  assert.deepEqual(h.errors, []);
  for (const html of [h.$('detail-meta').innerHTML, h.$('detail-revisions').innerHTML]) {
    assert.doesNotMatch(html, /<img|<script|onerror="/);
    assert.match(html, /&lt;img/);
    assert.match(html, /&lt;script&gt;/);
    assert.match(html, /&quot;alert\(1\)&quot;/);
  }
});


test('English detail and history retain translations after adding agent attribution', async () => {
  const h = harness({ ...memory, created_agent: 'Codex', updated_agent: '' }, [revision(1)], 'en');
  await h.context.viewMemoryDetail('memory', false);
  assert.deepEqual(h.errors, []);
  assert.match(h.$('detail-meta').innerHTML, /Created by agent: Codex/);
  assert.match(h.$('detail-meta').innerHTML, /Last modified by agent: Unknown agent/);
  assert.match(h.$('detail-revisions').innerHTML, /Member · Unknown agent/);
  assert.match(h.$('detail-revisions').innerHTML, /Raw snapshot/);
  assert.doesNotMatch(h.$('detail-revisions').innerHTML, /原始快照/);
});
