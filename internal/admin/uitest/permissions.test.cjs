const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');

// Run the real UI functions with small DOM doubles; no application logic is copied into the tests.
function harness() {
  const elements = new Map();
  const node = () => ({
    value: '', dataset: {}, disabled: false, innerHTML: '', textContent: '',
    classList: { toggle() {} },
    querySelector() { return this.child ||= node(); },
    querySelectorAll() { return []; },
    addEventListener(type, callback) { this[type] = callback; },
  });
  const $ = id => {
    if (!elements.has(id)) elements.set(id, node());
    return elements.get(id);
  };
  const principal = { id: 'member', status: 'active', display_name: 'Member', subject: 'member', identity_provider: 'api_token', tenant_roles: [{ tenant_id: 'tenant', role: 'none' }] };
  const project = { id: 'project', tenant_id: 'tenant', status: 'active', name: 'Project', members: [] };
  const state = { memberTenant: 'tenant', selectedPrincipal: principal.id, selectedProject: project.id, principals: [principal], projects: [project], tenants: [{ id: 'tenant', name: 'Tenant', status: 'active' }] };
  const requests = [];
  const submissions = {};
  const context = vm.createContext({
    $, state,
    t: (parts, ...values) => typeof parts === 'string' ? parts : parts.reduce((result, part, i) => result + part + (values[i] ?? ''), ''),
    identifier: () => '', formatDate: () => '', statusLabel: value => value,
    escapeHtml: value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;'),
    clearFormErrors() {}, showToast() {},
    api: async (url, options) => { requests.push({ url, method: options.method, body: JSON.parse(options.body) }); },
    refreshAllData: async () => {}, onSubmit: (id, callback) => { submissions[id] = callback; },
  });
  const load = (start, end) => {
    const from = source.indexOf(start);
    const to = source.indexOf(end, from);
    assert.ok(from >= 0 && to > from, `Missing app block ${start}`);
    vm.runInContext(source.slice(from, to), context);
  };
  load('  const TENANT_ROLES =', '  // actorName');
  load('  function tenantMembership(', '  // renderTenantContext');
  load('  const PROVIDERS =', '  // renderPrincipalsTable');
  load('  function renderMemberDetail()', '  // renderManagementViews');
  load('  function renderProjectRole()', '  // openProject');
  load('  onSubmit("member-role-form"', '  onSubmit("member-token-form"');
  return { $, state, principal, project, context, requests, submissions };
}

test('member details render all three project grants independently of global access', () => {
  for (const global of ['none', 'member', 'global_writer']) {
    for (const grant of ['none', 'reader', 'writer']) {
      const h = harness();
      h.principal.tenant_roles[0].role = global;
      h.project.members = grant === 'none' ? [] : [{ principal_id: h.principal.id, role: grant }];
      h.context.renderMemberDetail();
      assert.equal(h.$('member-role').value, global);
      const html = h.$('member-projects-table').querySelector('tbody').innerHTML;
      assert.match(html, new RegExp(`<option value="${grant}" selected>`));
      assert.match(html, /value="none"/);
      assert.match(html, /value="reader"/);
      assert.match(html, /value="writer"/);
      assert.doesNotMatch(html, /maintainer|value="admin"|无需单独授权/);
    }
  }
});

test('project grant picker supports explicit grants and revocation for a global-only member', () => {
  const h = harness();
  h.principal.tenant_roles[0].role = 'global_writer';
  h.$('pm-principal').value = h.principal.id;
  for (const grant of ['none', 'reader', 'writer']) {
    h.project.members = grant === 'none' ? [] : [{ principal_id: h.principal.id, role: grant }];
    h.context.renderProjectRole();
    assert.equal(h.$('pm-role').value, grant);
    assert.equal(h.$('pm-role').disabled, false);
  }
});

test('member grant actions submit none to revoke a project and remove global access', async () => {
  const h = harness();
  const attributes = {};
  const button = {
    disabled: false, dataset: { project: h.project.id },
    setAttribute(key, value) { attributes[key] = value; },
    removeAttribute(key) { delete attributes[key]; },
    closest: () => ({ querySelector: () => ({ value: 'none' }) }),
  };
  await h.$('member-projects-table').click({ target: { closest: () => button } });
  assert.deepEqual(h.requests[0], { url: '/api/admin/projects/members', method: 'POST', body: { project_id: 'project', principal_id: 'member', role: 'none' } });
  assert.equal(button.disabled, false);
  assert.equal(attributes['aria-busy'], undefined);
  h.$('member-role').value = 'none';
  await h.submissions['member-role-form']({ preventDefault() {} });
  assert.deepEqual(h.requests[1].body, { mode: 'update_role', tenant_id: 'tenant', principal_id: 'member', tenant_role: 'none' });
});
