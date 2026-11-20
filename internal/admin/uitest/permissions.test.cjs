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
    value: '', dataset: {}, checked: false, disabled: false, innerHTML: '', textContent: '',
    classList: { toggle() {}, add() {}, remove() {} },
    scrollIntoView() {}, focus() {},
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
  const state = { memberTenant: 'tenant', selectedPrincipal: principal.id, selectedProject: project.id, principals: [principal], projects: [project], tenants: [{ id: 'tenant', name: 'Tenant', status: 'active' }], tokens: [], revealedTokens: {} };
  const requests = [];
  const submissions = {};
  const context = vm.createContext({
    $, state,
    t: (parts, ...values) => typeof parts === 'string' ? parts : parts.reduce((result, part, i) => result + part + (values[i] ?? ''), ''),
    identifier: () => '', formatDate: () => '', statusLabel: value => value,
    escapeHtml: value => String(value).replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;'),
    clearFormErrors() {}, showToast() {}, openTenant() {}, openClientGuides() {}, renderClientGuides() {}, formatMCPConfig: () => '{"mcpServers":{}}',
    api: async (url, options) => { requests.push({ url, method: options.method, body: JSON.parse(options.body) }); return { principal_id: principal.id, token: 'test-token' }; },
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
  load('  function renderPrincipalsTable()', '  // renderMemberDetail');
  load('  function renderMemberDetail()', '  // renderManagementViews');
  load('  function renderProjectRole()', '  // openProject');
  load('  onSubmit("member-role-form"', '  onSubmit("member-token-form"');
  load('  onSubmit("create-principal-form"', '  onSubmit("member-role-form"');
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
  const select = { value: 'none', dataset: { saved: 'writer' }, disabled: false };
  const button = {
    disabled: false, dataset: { project: h.project.id },
    setAttribute(key, value) { attributes[key] = value; },
    removeAttribute(key) { delete attributes[key]; },
    closest: () => ({ querySelector: () => select }),
  };
  await h.$('member-projects-table').click({ target: { closest: () => button } });
  assert.deepEqual(h.requests[0], { url: '/api/admin/projects/members', method: 'POST', body: { project_id: 'project', principal_id: 'member', role: 'none' } });
  assert.equal(button.disabled, true);
  assert.equal(attributes['aria-busy'], undefined);
  h.$('member-role').value = 'none';
  await h.submissions['member-role-form']({ preventDefault() {} });
  assert.deepEqual(h.requests[1].body, { mode: 'update_role', tenant_id: 'tenant', principal_id: 'member', tenant_role: 'none', can_create_project: false });
});


test('member list displays project creation permission independently of global access', () => {
  for (const role of ['none', 'member', 'global_writer']) {
    for (const allowed of [false, true]) {
      const h = harness();
      Object.assign(h.principal.tenant_roles[0], { role, can_create_project: allowed });
      h.context.renderPrincipalsTable();
      const html = h.$('principals-table').querySelector('tbody').innerHTML;
      assert.match(html, new RegExp(`<td>${allowed ? '允许' : '不允许'}</td>`));
    }
  }
});

test('creation permission picker reflects saved grants and preserves unsaved drafts', () => {
  for (const allowed of [false, true]) {
    const h = harness();
    const membership = h.principal.tenant_roles[0];
    membership.can_create_project = allowed;
    h.context.renderMemberDetail();
    assert.equal(h.$('member-create-project').checked, allowed);
    assert.equal(h.$('member-create-project').disabled, false);
    h.$('member-create-project').checked = !allowed;
    h.context.renderMemberDetail();
    assert.equal(h.$('member-create-project').checked, !allowed);
    membership.can_create_project = !allowed;
    h.context.renderMemberDetail();
    assert.equal(h.$('member-create-project').checked, !allowed);
    // Switching identities must replace the previous member's unsaved checkbox value.
    h.$('member-create-project').checked = allowed;
    h.principal.id = 'other-member';
    h.state.selectedPrincipal = h.principal.id;
    h.context.renderMemberDetail();
    assert.equal(h.$('member-create-project').checked, !allowed);
  }
});

test('inactive tenants and members disable project creation changes', () => {
  for (const inactive of ['tenant', 'member']) {
    const h = harness();
    h.principal.tenant_roles[0].can_create_project = true;
    if (inactive === 'tenant') h.state.tenants[0].status = 'disabled';
    else h.principal.status = 'disabled';
    h.context.renderMemberDetail();
    assert.equal(h.$('member-create-project').checked, true);
    assert.equal(h.$('member-create-project').disabled, true);
    assert.equal(h.$('member-role-form').querySelector('button[type="submit"]').disabled, true);
  }
});

test('member permission saves submit explicit creation grants and revocations', async () => {
  for (const allowed of [false, true]) {
    const h = harness();
    h.$('member-role').value = 'member';
    h.$('member-create-project').checked = allowed;
    await h.submissions['member-role-form']({ preventDefault() {} });
    assert.deepEqual(h.requests[0].body, { mode: 'update_role', tenant_id: 'tenant', principal_id: 'member', tenant_role: 'member', can_create_project: allowed });
  }
});

test('creating or binding a member explicitly configures project creation permission', async () => {
  for (const mode of ['create', 'bind']) {
    for (const allowed of [false, true]) {
      const h = harness();
      h.$('prin-mode').value = mode;
      h.$('prin-existing').value = h.principal.id;
      h.$('prin-create-project').checked = allowed;
      await h.submissions['create-principal-form']({ preventDefault() {} });
      assert.equal(h.requests[0].body.mode, mode);
      assert.equal(h.requests[0].body.can_create_project, allowed);
    }
  }
});


test('permission save predicates require changes and preserve inactive access guards', () => {
  const h = harness();
  h.context.renderMemberDetail();
  assert.equal(h.context.canSaveMemberRole(), false);
  h.$('member-create-project').checked = true;
  assert.equal(h.context.canSaveMemberRole(), true);
  h.$('member-create-project').checked = false;
  assert.equal(h.context.canSaveMemberRole(), false);
  h.$('member-role').value = 'global_writer';
  assert.equal(h.context.canSaveMemberRole(), true);
  h.$('member-role').disabled = true;
  assert.equal(h.context.canSaveMemberRole(), false);
  h.$('pm-principal').value = h.principal.id;
  h.context.renderProjectRole();
  assert.equal(h.context.canSaveProjectRole(), false);
  h.$('pm-role').value = 'writer';
  assert.equal(h.context.canSaveProjectRole(), true);
  h.$('pm-role').value = 'none';
  assert.equal(h.context.canSaveProjectRole(), false);
});
