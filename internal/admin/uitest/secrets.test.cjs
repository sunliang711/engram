const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');

// secretHarness exercises the production mask and reveal state without storing real credentials.
function secretHarness(reveal = async () => ({ value: 'saved-test-key' })) {
  const nodes = new Map(), requests = [], errors = [];
  const makeNode = id => ({
    id, value: '', type: 'password', placeholder: 'Enter a key', dataset: {},
    setAttribute(name, value) { this[name] = value; },
    getAttribute(name) { return this[name]; },
    removeAttribute(name) { delete this[name]; },
    addEventListener(name, callback) { this[name] = callback; },
  });
  for (const id of ['sk-api-key', 'login-password']) {
    nodes.set(id, makeNode(id));
    nodes.set('btn-secret-' + id, makeNode('btn-secret-' + id));
  }
  nodes.get('sk-api-key').dataset.secretStored = 'api_key';
  const inputs = ['sk-api-key', 'login-password'].map(id => nodes.get(id));
  const context = vm.createContext({
    $: id => nodes.get(id), document: { querySelectorAll: () => inputs },
    t: value => value, showToast: message => errors.push(message),
    api: async (url, options) => { requests.push({ url, body: JSON.parse(options.body) }); return reveal(); },
  });
  vm.runInContext(source.slice(source.indexOf('  // Secret fields'), source.indexOf('  // showToast')), context);
  context.bindSecretFields();
  return { context, nodes, requests, errors, input: nodes.get('sk-api-key') };
}

test('every password, saved secret and client key input has an individual reveal button', () => {
  const fields = [...html.matchAll(/<input type="password" id="([^"]+)"[^>]*data-secret-input/g)];
  assert.equal(fields.length, 9);
  for (const [, id] of fields) assert.ok(html.includes(`data-secret-toggle="${id}"`), id);
  assert.doesNotMatch(html, /id="show-admin-passwords"/);
});

test('saved values show a mask, reveal only on click and never become replacement drafts', async () => {
  const h = secretHarness();
  h.context.renderStoredSecret('sk-api-key', true);
  assert.equal(h.input.placeholder, '********');
  assert.equal(h.input.value, '');
  assert.equal(h.requests.length, 0);
  await h.context.toggleSecretField(h.input);
  assert.equal(h.input.value, 'saved-test-key');
  assert.equal(h.input.type, 'text');
  assert.equal(h.context.secretDraft('sk-api-key'), '');
  assert.deepEqual(h.requests, [{ url: '/api/admin/integrations/safekeep/reveal', body: { field: 'api_key' } }]);
  await h.context.toggleSecretField(h.input);
  assert.equal(h.input.type, 'password');
  assert.equal(h.input.value, '');
  assert.equal(h.input.placeholder, '********');
  h.input.value = 'replacement';
  h.input.input();
  await h.context.toggleSecretField(h.input);
  assert.equal(h.input.type, 'text');
  assert.equal(h.context.secretDraft('sk-api-key'), 'replacement');
  assert.equal(h.requests.length, 1);
});

test('cancel, page reset, logout and user edits defeat late reveal responses', async () => {
  for (const action of ['cancel', 'page', 'logout', 'edit']) {
    let resolve;
    const h = secretHarness(() => new Promise(done => { resolve = done; }));
    h.context.renderStoredSecret('sk-api-key', true);
    const pending = h.context.toggleSecretField(h.input);
    if (action === 'cancel') await h.context.toggleSecretField(h.input);
    if (action === 'page') h.context.resetSecretVisibility();
    if (action === 'logout') h.context.resetSecretVisibility(true);
    if (action === 'edit') { h.input.value = 'my-draft'; h.input.input(); }
    resolve({ value: 'late-secret' });
    await pending;
    assert.equal(h.input.value, action === 'edit' ? 'my-draft' : '', action);
    assert.equal(h.input.type, 'password');
    assert.equal(h.context.secretDraft('sk-api-key'), action === 'edit' ? 'my-draft' : '');
  }
});

test('login passwords toggle locally, reset to hidden, and reveal errors keep saved masks', async () => {
  const h = secretHarness(async () => { throw new Error('cannot decrypt'); });
  const password = h.nodes.get('login-password');
  password.value = 'typed-password';
  await h.context.toggleSecretField(password);
  assert.equal(password.type, 'text');
  h.context.resetSecretVisibility();
  assert.equal(password.type, 'password');
  assert.equal(password.value, 'typed-password');
  h.context.renderStoredSecret('sk-api-key', true);
  await h.context.toggleSecretField(h.input);
  assert.equal(h.input.value, '');
  assert.equal(h.input.placeholder, '********');
  assert.deepEqual(h.errors, ['cannot decrypt']);
  h.context.resetSecretVisibility(true);
  assert.equal(password.value, '');
});
