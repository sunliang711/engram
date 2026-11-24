const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const html = fs.readFileSync(path.join(__dirname, '../ui/index.html'), 'utf8');
const css = fs.readFileSync(path.join(__dirname, '../ui/styles.css'), 'utf8');
const appSource = fs.readFileSync(path.join(__dirname, '../ui/app.js'), 'utf8');
const i18nSource = fs.readFileSync(path.join(__dirname, '../ui/i18n.js'), 'utf8');

function makeElement(id, initialHidden = false) {
  const classes = new Set(initialHidden ? ['hidden'] : []);
  const attrs = {};
  const listeners = {};
  const children = [];
  const el = {
    id,
    value: '',
    textContent: '',
    title: '',
    type: id.includes('password') ? 'password' : 'text',
    required: false,
    disabled: false,
    open: false,
    focused: false,
    isConnected: true,
    dataset: {},
    classList: {
      add: (...names) => names.forEach(n => classes.add(n)),
      remove: (...names) => names.forEach(n => classes.delete(n)),
      toggle(name, enabled) {
        if (enabled === undefined) {
          if (classes.has(name)) classes.delete(name);
          else classes.add(name);
        } else if (enabled) classes.add(name);
        else classes.delete(name);
      },
      contains: name => classes.has(name),
    },
    setAttribute(k, v) { attrs[k] = String(v); },
    getAttribute(k) { return Object.prototype.hasOwnProperty.call(attrs, k) ? attrs[k] : null; },
    removeAttribute(k) { delete attrs[k]; },
    hasAttribute(k) { return Object.prototype.hasOwnProperty.call(attrs, k); },
    addEventListener(type, fn) { (listeners[type] ||= []).push(fn); },
    dispatch(type, event = {}) {
      const list = listeners[type] || [];
      return Promise.all(list.map(fn => fn(event)));
    },
    click() { return el.dispatch('click', {}); },
    focus() { el.focused = true; documentRef.activeElement = el; },
    querySelector(sel) {
      if (sel === 'button[type="submit"]') return submitBtn;
      return null;
    },
    querySelectorAll() { return children; },
    append(node) { children.push(node); },
    insertAdjacentElement(_pos, node) { children.push(node); },
  };
  let submitBtn = null;
  if (id.endsWith('-form')) {
    submitBtn = makeElement(`${id}-submit`);
    submitBtn.before = (node) => children.push(node);
  }
  const documentRef = { activeElement: null };
  el._setDocument = (doc) => { documentRef.activeElement = doc.activeElement; Object.defineProperty(el, '_doc', { value: doc }); };
  el.focus = () => {
    el.focused = true;
    if (el._doc) el._doc.activeElement = el;
  };
  el._children = children;
  return el;
}

function createHarness({ fetchImpl, language = 'zh-CN', authenticated = true } = {}) {
  const nodes = new Map();
  const toasts = [];
  const doc = {
    activeElement: null,
    documentElement: { dataset: {} },
    querySelector(sel) {
      if (sel === '.account-menu') return $('account-menu');
      return null;
    },
    querySelectorAll(sel) {
      if (sel === '[data-secret-input]') return [$('reauth-password'), $('login-password')];
      return [];
    },
    createElement(tag) {
      const created = makeElement(`created-${tag}-${Math.random()}`);
      created.tagName = tag.toUpperCase();
      created.remove = () => {};
      return created;
    },
  };
  const $ = (id) => {
    if (!nodes.has(id)) {
      const initialHidden = [
        'login-view',
        'force-password-view',
        'reauth-modal',
        'toast',
        'reset-admin-banner',
        'confirm-modal',
        'memory-detail-card',
        'client-config-modal',
        'rename-modal',
        'remote-edit-modal',
        'memory-create-modal',
        'purge-modal',
        'glossary-modal',
        'audit-detail-modal',
        'audit-cleanup-modal',
        'create-tenant-card',
        'tenant-forms-panel',
        'project-forms-panel',
      ].includes(id);
      const node = makeElement(id, initialHidden);
      node._setDocument(doc);
      nodes.set(id, node);
    }
    return nodes.get(id);
  };

  if (authenticated) {
    $('app-view').classList.remove('hidden');
    $('login-view').classList.add('hidden');
    $('current-admin-user').textContent = 'admin';
  } else {
    $('app-view').classList.add('hidden');
    $('login-view').classList.remove('hidden');
  }

  const opener = $('btn-save-memory');
  opener.focus();

  const state = {
    appVersion: 'v0.0.1',
    visibleTokens: {},
    revealedTokens: {},
    latestToken: '',
    clientConfigTokenID: '',
    clientConfigRequest: 0,
    toastTimer: null,
    reauthPromise: null,
    reauthResolve: null,
    reauthReject: null,
    reauthOpener: null,
    reauthSeq: 0,
    locationReady: true,
    lastRefresh: 0,
  };

  let refreshCount = 0;
  const context = vm.createContext({
    $,
    state,
    document: doc,
    window: { matchMedia: () => ({ matches: false }) },
    navigator: { language },
    location: { search: '', href: 'http://localhost/' },
    URL,
    URLSearchParams,
    localStorage: { getItem: () => null, setItem() {} },
    fetch: fetchImpl,
    setTimeout,
    clearTimeout,
    FormData: class {},
    closeMemoryDetail() {},
    closeClientGuides() {},
    renderClientGuides() {},
    refreshAllData() { refreshCount++; },
    showToastSpy: toasts,
  });

  vm.runInContext(i18nSource, context);
  context.t = context.window.EngramI18n.t;

  const load = (start, end) => {
    const from = appSource.indexOf(start);
    const to = appSource.indexOf(end, from);
    assert.ok(from >= 0 && to > from, `Missing app block ${start} -> ${end}`);
    vm.runInContext(appSource.slice(from, to), context);
  };

  load('  // Secret fields', '  async function checkSession(');
  load('  // refreshIfStale', '  function detailIsOpen(');
  load('  // FormError attaches', '  // shellQuote protects');
  load('  // onSubmit prevents', '  // Keep wide tables');
  load('  onSubmit("login-form"', '  onSubmit("force-password-form"');

  const origShowToast = context.showToast;
  context.showToast = (msg, isError) => {
    origShowToast(msg, isError);
    if (!$('toast').classList.contains('hidden')) {
      toasts.push({ msg, isError: !!isError });
    }
  };

  return {
    $,
    state,
    doc,
    context,
    toasts,
    opener,
    getRefreshCount: () => refreshCount,
    submitReauth: (username, password) => {
      $('reauth-username').value = username;
      $('reauth-password').value = password;
      return $('reauth-form').dispatch('submit', { preventDefault() {} });
    },
  };
}

function jsonResp(status, body) {
  return {
    ok: status >= 200 && status < 300,
    status,
    clone() { return jsonResp(status, body); },
    async text() { return typeof body === 'string' ? body : JSON.stringify(body); },
    async json() { return body; },
  };
}

test('HTML, CSS, and direct admin fetch callers wire #reauth-modal cleanly', () => {
  for (const id of [
    'reauth-modal',
    'reauth-form',
    'reauth-username',
    'reauth-password',
    'btn-secret-reauth-password',
    'btn-close-reauth',
    'btn-reauth-logout',
    'btn-reauth-submit',
  ]) {
    assert.ok(html.includes(`id="${id}"`), `Expected #${id} in index.html`);
  }
  assert.match(html, /id="reauth-modal"[^>]*class="modal-backdrop hidden"/);
  assert.match(css, /\.modal-backdrop#reauth-modal\s*\{\s*z-index:\s*1200;\s*\}/);

  const rawAdminFetchMatches = [...appSource.matchAll(/\bfetch\(["']\/api\/admin\//g)];
  assert.equal(rawAdminFetchMatches.length, 0, 'All /api/admin/* calls should go through adminFetch');
  assert.match(appSource, /adminFetch\("\/api\/admin\/export\/archive"/);
  assert.match(appSource, /adminFetch\("\/api\/admin\/memories\/purge\/backup"/);
  assert.match(appSource, /adminFetch\("\/api\/admin\/migration\/inspect"/);
  assert.match(appSource, /adminFetch\("\/api\/admin\/migration\/export"/);
  assert.match(appSource, /adminFetch\("\/api\/admin\/migration\/import"/);
  assert.match(appSource, /adminFetch\("\/api\/admin\/audit\/logs\/export\?"/);
});

test('reauth strings translate cleanly in English locale', () => {
  const ctx = vm.createContext({
    window: { matchMedia: () => ({ matches: false }) },
    document: { documentElement: { dataset: {} }, querySelectorAll: () => [] },
    navigator: { language: 'en-US' },
    location: { search: '?lang=en', href: 'http://localhost/?lang=en' },
    URL,
    URLSearchParams,
    localStorage: { getItem: () => null, setItem() {} },
  });
  vm.runInContext(i18nSource, ctx);
  const { t } = ctx.window.EngramI18n;
  assert.equal(t('登录已过期'), 'Session expired');
  assert.equal(t('返回登录页'), 'Back to sign-in');
  assert.equal(t('重新登录'), 'Sign in again');
  assert.equal(t('已重新登录'), 'Signed in again');
});

test('expired session opens temporary login modal once for concurrent 401s and retries after login', async () => {
  let authed = false;
  let calls = [];
  const h = createHarness({
    fetchImpl: async (url) => {
      calls.push(url);
      if (url === '/api/admin/login') {
        authed = true;
        return jsonResp(200, { authenticated: true, username: 'admin', must_change_password: false, version: 'v0.0.2' });
      }
      if (!authed) {
        return jsonResp(401, { code: 'UNAUTHENTICATED', message: 'admin authentication required' });
      }
      return jsonResp(200, { ok: true, url });
    },
  });

  const pending = Promise.all([
    h.context.api('/api/admin/overview'),
    h.context.api('/api/admin/tenants'),
    h.context.api('/api/admin/projects'),
  ]);
  await new Promise(r => setImmediate(r));

  assert.equal(h.$('reauth-modal').classList.contains('hidden'), false);
  assert.equal(h.$('app-view').classList.contains('hidden'), false);
  assert.equal(h.$('reauth-username').value, 'admin');
  assert.equal(h.doc.activeElement, h.$('reauth-password'));

  // Background refreshIfStale must not fire extra calls while reauth modal is open.
  h.context.refreshIfStale(0);
  assert.equal(h.getRefreshCount(), 0);

  await h.submitReauth('admin', 'secret-pass');
  const results = await pending;

  assert.equal(h.$('reauth-modal').classList.contains('hidden'), true);
  assert.equal(h.$('reauth-password').value, '');
  assert.equal(h.doc.activeElement, h.opener);
  assert.deepEqual(JSON.parse(JSON.stringify(results)), [
    { ok: true, url: '/api/admin/overview' },
    { ok: true, url: '/api/admin/tenants' },
    { ok: true, url: '/api/admin/projects' },
  ]);
  assert.equal(h.$('app-version').textContent, 'v0.0.2');
});

test('wrong password inside reauth modal shows inline error without re-triggering modal, then succeeds on retry', async () => {
  let authed = false;
  const h = createHarness({
    fetchImpl: async (url, opts) => {
      if (url === '/api/admin/login') {
        const body = JSON.parse(opts.body);
        if (body.password !== 'right-pass') {
          return jsonResp(401, { code: 'UNAUTHENTICATED', message: 'invalid username or password' });
        }
        authed = true;
        return jsonResp(200, { authenticated: true, username: 'admin', must_change_password: false });
      }
      if (!authed) {
        return jsonResp(401, { code: 'UNAUTHENTICATED', message: 'admin authentication required' });
      }
      return jsonResp(200, { saved: true });
    },
  });

  const pendingSave = h.context.api('/api/admin/memories', { method: 'PATCH', body: '{}' });
  await new Promise(r => setImmediate(r));
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), false);

  await h.submitReauth('admin', 'wrong-pass');
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), false);
  assert.ok(h.$('reauth-form')._children.some(node => /登录已过期或账号密码不正确/.test(node.textContent)));

  await h.submitReauth('admin', 'right-pass');
  const res = await pendingSave;
  assert.deepEqual(JSON.parse(JSON.stringify(res)), { saved: true });
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), true);
});

test('wrong current password on change-password and /mcp 401 do not open reauth modal', async () => {
  let mode = 'wrong-current';
  const h = createHarness({
    fetchImpl: async (url) => {
      if (url === '/api/admin/change-password' && mode === 'wrong-current') {
        return jsonResp(401, { code: 'UNAUTHENTICATED', message: 'current password is incorrect' });
      }
      if (url === '/api/admin/change-password' && mode === 'expired') {
        return jsonResp(401, { code: 'UNAUTHENTICATED', message: 'admin authentication required' });
      }
      if (url === '/api/admin/login') {
        return jsonResp(200, { authenticated: true, username: 'admin', must_change_password: false });
      }
      return jsonResp(200, { updated: true });
    },
  });

  assert.equal(h.context.isSessionExpiredFailure('/mcp', { status: 401 }, { code: 'UNAUTHENTICATED' }), false);

  await assert.rejects(
    () => h.context.api('/api/admin/change-password', { method: 'POST', body: '{}' }),
    /登录已过期或账号密码不正确/,
  );
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), true);

  mode = 'expired';
  const p = h.context.api('/api/admin/change-password', { method: 'POST', body: '{}' });
  await new Promise(r => setImmediate(r));
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), false);
  mode = 'ok';
  await h.submitReauth('admin', 'secret');
  assert.deepEqual(JSON.parse(JSON.stringify(await p)), { updated: true });
});

test('cancelling or returning to login view resolves cleanly without leaving stale modal or error toast', async () => {
  const h = createHarness({
    fetchImpl: async () => jsonResp(401, { code: 'UNAUTHENTICATED', message: 'admin authentication required' }),
  });

  const p1 = h.context.api('/api/admin/overview');
  await new Promise(r => setImmediate(r));
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), false);
  await h.$('btn-close-reauth').click();
  await assert.rejects(() => p1, /登录已过期或账号密码不正确/);
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), true);
  assert.equal(h.doc.activeElement, h.opener);

  h.$('memory-create-modal').classList.remove('hidden');
  const p2 = h.context.api('/api/admin/overview');
  await new Promise(r => setImmediate(r));
  await h.$('btn-reauth-logout').click();
  await assert.rejects(() => p2, err => err.reauthDismissed === true);
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), true);
  assert.equal(h.$('memory-create-modal').classList.contains('hidden'), true);
  assert.equal(h.$('app-view').classList.contains('hidden'), true);
  assert.equal(h.$('login-view').classList.contains('hidden'), false);
  h.context.showToast('should be suppressed on login view', true);
  assert.equal(h.toasts.length, 0);
});

test('direct adminFetch download calls and must_change_password re-login work seamlessly', async () => {
  let authed = false;
  let forceReset = false;
  const h = createHarness({
    fetchImpl: async (url) => {
      if (url === '/api/admin/login') {
        authed = true;
        return jsonResp(200, { authenticated: true, username: 'admin', must_change_password: forceReset });
      }
      if (!authed) {
        return jsonResp(401, { code: 'UNAUTHENTICATED', message: 'admin authentication required' });
      }
      return { ok: true, status: 200, blob: async () => 'zip-bytes' };
    },
  });

  const respPromise = h.context.adminFetch('/api/admin/export/archive');
  await new Promise(r => setImmediate(r));
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), false);
  await h.submitReauth('admin', 'secret');
  const resp = await respPromise;
  assert.equal(resp.ok, true);
  assert.equal(await resp.blob(), 'zip-bytes');

  authed = false;
  forceReset = true;
  const p2 = h.context.api('/api/admin/overview');
  await new Promise(r => setImmediate(r));
  await h.submitReauth('admin', 'temp-pass');
  await assert.rejects(() => p2, err => err.reauthDismissed === true);
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), true);
  assert.equal(h.$('force-password-view').classList.contains('hidden'), false);
});

test('401 while already on login view does not open reauth modal', async () => {
  const h = createHarness({
    authenticated: false,
    fetchImpl: async () => jsonResp(401, { code: 'UNAUTHENTICATED', message: 'admin authentication required' }),
  });
  await assert.rejects(() => h.context.api('/api/admin/overview'), /登录已过期或账号密码不正确/);
  assert.equal(h.$('reauth-modal').classList.contains('hidden'), true);
});
