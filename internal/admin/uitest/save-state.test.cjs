const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const source = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');

// Exercise the real submit lifecycle, including keyboard submission and pending writes.
function harness(handler, canSubmit) {
  const events = {}, errors = [];
  const button = { disabled: false, setAttribute(name, value) { this[name] = value; }, removeAttribute(name) { delete this[name]; } };
  const form = { dataset: {}, addEventListener(name, callback) { events[name] = callback; }, querySelector: () => button };
  const context = vm.createContext({ $: () => form, clearFormErrors() {}, showFormError: (_, err) => errors.push(err.message) });
  vm.runInContext(source.slice(source.indexOf('  function onSubmit('), source.indexOf('  // Keep wide tables')), context);
  if (canSubmit) context.onSubmit('form', handler, canSubmit);
  else context.onSubmit('form', handler);
  return { button, form, events, errors, submit: () => events.submit({ preventDefault() {} }) };
}

test('unchanged drafts reject keyboard submission and reverting disables save', async () => {
  let draft = 'saved', writes = 0;
  const h = harness(async () => { writes++; }, () => draft !== 'saved');
  assert.equal(h.button.disabled, true);
  await h.submit();
  assert.equal(writes, 0);
  draft = 'edited';
  h.events.input();
  assert.equal(h.button.disabled, false);
  draft = 'saved';
  h.events.change();
  assert.equal(h.button.disabled, true);
  await h.submit();
  assert.equal(writes, 0);
});

test('pending writes remain disabled during edits and success adopts the saved baseline', async () => {
  let finish, writes = 0, draft = 'edited', saved = 'saved';
  const pending = new Promise(resolve => { finish = resolve; });
  const h = harness(async () => { writes++; await pending; saved = draft; }, () => draft !== saved);
  const submitted = h.submit();
  assert.equal(h.button.disabled, true);
  assert.equal(h.button['aria-busy'], 'true');
  h.events.input();
  assert.equal(h.button.disabled, true);
  await h.submit();
  assert.equal(writes, 1);
  finish();
  await submitted;
  assert.equal(h.button.disabled, true);
  assert.equal(h.button['aria-busy'], undefined);
});

test('failed saves keep the draft retryable and release the busy state', async () => {
  const h = harness(async () => { throw new Error('Write failed'); }, () => true);
  await h.submit();
  assert.deepEqual(h.errors, ['Write failed']);
  assert.equal(h.button.disabled, false);
  assert.equal(h.button['aria-busy'], undefined);
  assert.equal(h.form.dataset.submitting, 'false');
});


test('creation and action forms retain availability controlled by their own renderers', () => {
  const h = harness(async () => {});
  h.button.disabled = true;
  h.events.input();
  h.events.change();
  assert.equal(h.button.disabled, true);
});
