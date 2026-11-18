(function () {
  const { t, locale } = window.EngramI18n;
  window.EngramI18n.initialize();
  const state = {
    tenants: [],
    principals: [],
    projects: [],
    tokens: [],
    memories: [],
    facets: { categories: [], tags: [] },
    lastRefresh: 0,
    latestToken: "",
    revealedTokens: {},
    memoryPage: 1,
    memoryPageSize: 20,
    memoryTotal: 0,
    memoryRequest: 0,
    detailRequest: 0,
    selectedMemory: null,
    detailTrigger: null,
    locationReady: false,
    locationRequest: 0,
    initialized: null,
    toastTimer: null,
  };

  function $(id) {
    return document.getElementById(id);
  }

  // showToast keeps the latest message visible for its complete display interval.
  function showToast(msg, isError) {
    const el = $("toast");
    el.textContent = msg;
    el.className = isError ? "toast error" : "toast";
    clearTimeout(state.toastTimer);
    state.toastTimer = setTimeout(() => {
      el.classList.add("hidden");
    }, isError ? 7000 : 3800);
  }

  // api decodes management responses and translates common failures for the console.
  async function api(path, options = {}) {
    const headers = Object.assign({ "Content-Type": "application/json" }, options.headers || {});
    const resp = await fetch(path, Object.assign({}, options, { headers, credentials: "same-origin" }));
    let data = null;
    const text = await resp.text();
    if (text) {
      try {
        data = JSON.parse(text);
      } catch (_) {
        data = { message: text };
      }
    }
    if (!resp.ok) {
      const raw = (data && (data.message || data.error)) || "";
      const msg = ERROR_CODES[data && data.code] || translateError(raw) || raw || t`请求失败（${resp.status}），请重试。`;
      throw new Error(msg);
    }
    return data;
  }

  const ERROR_CODES = {
    UNAUTHENTICATED: t("登录已过期或账号密码不正确，请重新登录。"),
    UNAUTHORIZED: t("权限不足，无法完成这个操作。"),
    INTERNAL_ERROR: t("暂时无法完成操作，请稍后重试。"),
    RATE_LIMITED: t("操作过于频繁，请稍后重试。"),
    VERSION_CONFLICT: t("这条知识刚被其他客户端修改，请刷新后重试。"),
    MEMORY_NOT_FOUND: t("这条知识不存在或已被删除，请刷新后重试。"),
  };

  // ERROR_MESSAGES translates validation messages the management API returns in English.
  const ERROR_MESSAGES = [
    [/^(invalid remote url|remote host cannot be empty|invalid repository path)/i, t("仓库地址格式不正确，请填写 SSH 或 HTTPS 形式的 Git 地址。")],
    [/^(path must be relative|relative path must not contain)/i, t("子目录请填写相对仓库根目录的路径，不能以 / 开头，也不能包含 . 或 ..。")],
    [/^principal is not a member of the specified tenant/i, t("该成员还不属于所选租户，请先在「租户与成员」页把成员绑定到这个租户。")],
    [/^principal must be a member of the project's tenant/i, t("该成员还不属于这个项目所在的租户，请先绑定租户。")],
    [/^project name is required/i, t("请填写项目名称。")],
    [/^tenant name is required/i, t("请填写租户名称。")],
    [/^tenant_name, principal_subject, and principal_display_name are required/i, t("请填写租户名称、成员标识和显示名称。")],
    [/^project not found/i, t("项目不存在，请刷新后重新选择。")],
    [/^memory (is not deleted|is already deleted|is deleted|pinned state is unchanged)/i, t("这条知识的状态已经变化，请刷新后重试。")],
    [/^invalid (tenant_id|principal_id|project_id)/i, t("请先选择租户、成员或项目。")],
    [/^invalid (json body|tenant role|principal mode|identity provider)/i, t("提交的内容不正确，请刷新页面后重试。")],
    [/admin password change required/i, t("请先修改管理员密码。")],
  ];

  function translateError(message) {
    const hit = ERROR_MESSAGES.find(([pattern]) => pattern.test(message));
    return hit ? hit[1] : "";
  }

  const TENANT_ROLES = { member: t("全局只读成员"), global_writer: t("全局读写者"), admin: t("租户管理员") };
  const PROJECT_ROLES = { reader: t("只读成员"), writer: t("编辑者"), maintainer: t("维护者") };

  // isSystemPrincipal marks the internal identity the console uses to act on a tenant's behalf.
  function isSystemPrincipal(principal) { return principal && principal.identity_provider === "system_webui"; }

  // visiblePrincipals hides the console's internal identity from member lists and pickers.
  function visiblePrincipals() { return state.principals.filter((principal) => !isSystemPrincipal(principal)); }

  // actorName names the author of a revision, describing console actions as such.
  function actorName(actorID) {
    const actor = state.principals.find((person) => person.id === actorID);
    if (!actor) return actorID.slice(0, 8);
    return isSystemPrincipal(actor) ? t("管理控制台") : actor.display_name;
  }

  // renderView separates first activation from an authenticated administration session.
  function renderView(sessionState) {
    const banner = $("reset-admin-banner");
    if (sessionState && sessionState.reset_admin_env_active) {
      banner.classList.remove("hidden");
    } else {
      banner.classList.add("hidden");
    }

    $("login-view").classList.add("hidden");
    $("force-password-view").classList.add("hidden");
    $("app-view").classList.add("hidden");

    if (!sessionState || !sessionState.authenticated) {
      $("login-view").classList.remove("hidden");
      return;
    }

    if (sessionState.must_change_password) {
      $("pwd-view-title").textContent = t("设置管理员密码");
      $("pwd-view-subtitle").textContent = t("你正在使用临时密码。请设置新的管理员密码以完成首次激活。");
      $("current-admin-password-group").classList.add("hidden");
      $("current-admin-password").required = false;
      $("btn-cancel-change-pwd").classList.add("hidden");
      $("force-password-view").classList.remove("hidden");
      return;
    }

    $("current-admin-user").textContent = sessionState.username || "admin";
    $("app-view").classList.remove("hidden");
    refreshAllData();
  }

  async function checkSession() {
    try {
      const s = await api("/api/admin/session");
      renderView(s);
    } catch (_) {
      renderView({ authenticated: false });
    }
  }

  // refreshAllData refreshes shared lists and restores the initial navigation state once.
  // Quiet refreshes keep the current list visible and leave an open detail drawer untouched.
  async function refreshAllData(options = {}) {
    state.lastRefresh = Date.now();
    try {
      const [overview, tenants, principals, projects, tokens, facets] = await Promise.all([
        api("/api/admin/overview"),
        api("/api/admin/tenants"),
        api("/api/admin/principals"),
        api("/api/admin/projects"),
        api("/api/admin/tokens"),
        api("/api/admin/memories/facets"),
      ]);
      state.tenants = tenants.items || [];
      state.principals = principals.items || [];
      state.projects = projects.items || [];
      state.tokens = tokens.items || [];
      state.facets = { categories: facets.categories || [], tags: facets.tags || [] };
      state.deletedCount = overview.deleted_memories_count || 0;

      $("stat-tenants").textContent = overview.tenants_count || 0;
      $("stat-principals").textContent = overview.principals_count || 0;
      $("stat-projects").textContent = overview.projects_count || 0;
      $("stat-memories").textContent = overview.active_memories_count || 0;
      $("stat-deleted").textContent = overview.deleted_memories_count || 0;
      $("stat-tokens").textContent = overview.active_tokens_count || 0;
      $("stat-events").textContent = overview.events_count || 0;

      const initialized = state.tenants.length > 0;
      $("tab-overview").classList.toggle("needs-setup", !initialized);
      $("overview-title").textContent = initialized ? t("让知识在项目与客户端之间流动") : t("连接你的第一个 AI 客户端");
      $("overview-description").textContent = initialized ? t("浏览最近沉淀的知识，管理项目和客户端接入。") : t("创建空间、关联项目，再复制客户端配置，即可开始共享记忆。");
      if (state.initialized === null) {
        $("quickstart-panel").open = !initialized;
        $("create-token-panel").open = !state.tokens.length;
        $("project-forms-panel").open = !state.projects.length;
      }
      state.initialized = initialized;
      renderSelectors();
      renderTenantsTable();
      renderPrincipalsTable();
      renderProjectsTable();
      renderTokensTable();
      if (!state.locationReady) {
        state.locationReady = true;
        await restoreLocation();
      } else if (!(options.quiet && detailIsOpen())) {
        await loadMemories({ quiet: options.quiet });
      }
      await loadRecentMemories();
      $("last-refresh").textContent = t("已更新 ") + new Date().toLocaleTimeString(locale, { hour12: false });
    } catch (err) {
      showToast(err.message, true);
    }
  }

  // refreshIfStale reloads quietly when the console has been idle longer than maxAge.
  function refreshIfStale(maxAge) {
    if (!state.locationReady || $("app-view").classList.contains("hidden")) return;
    if (Date.now() - state.lastRefresh >= maxAge) refreshAllData({ quiet: true });
  }

  function detailIsOpen() { return !$("memory-detail-card").classList.contains("hidden"); }

  // renderSelectors preserves valid selections when shared administration data refreshes.
  function renderSelectors() {
    const tenantSelects = ["prin-tenant", "proj-tenant", "tok-tenant"];
    tenantSelects.forEach((id) => {
      const sel = $(id);
      if (!sel) return;
      const prev = sel.value;
      sel.innerHTML = state.tenants
        .map((t) => `<option value="${t.id}">${escapeHtml(tenantLabel(t))}</option>`)
        .join("");
      if (prev && Array.from(sel.options).some((option) => option.value === prev)) sel.value = prev;
    });

    fillSelect("mem-filter-tenant", state.tenants.map((t) => [t.id, tenantLabel(t)]));
    renderMemoryProjectFilter();
    fillSelect("mem-filter-category", state.facets.categories.map((f) => [f.value, t`${categoryLabel(f.value)}（${f.count}）`]));
    fillSelect("mem-filter-tag", state.facets.tags.map((f) => [f.value, t`${f.value}（${f.count}）`]));

    const prinSelects = ["pm-principal", "tok-principal", "prin-existing"];
    prinSelects.forEach((id) => {
      const sel = $(id);
      if (!sel) return;
      const prev = sel.value;
      sel.innerHTML = visiblePrincipals()
        .map((p) => t`<option value="${p.id}">${escapeHtml(p.display_name)}（${escapeHtml(p.subject)}）</option>`)
        .join("");
      if (prev && Array.from(sel.options).some((option) => option.value === prev)) sel.value = prev;
    });

    const projSelects = ["pm-project", "pr-project"];
    projSelects.forEach((id) => {
      const sel = $(id);
      if (!sel) return;
      const prev = sel.value;
      sel.innerHTML = state.projects
        .map((p) => `<option value="${p.id}">${escapeHtml(projectLabel(p))}</option>`)
        .join("");
      if (prev && Array.from(sel.options).some((option) => option.value === prev)) sel.value = prev;
    });
    updatePrincipalMode();
  }

  // fillSelect rebuilds a filter list behind an "all" option, keeping a still-requested value selectable.
  function fillSelect(id, entries) {
    const sel = $(id);
    const prev = sel.value || sel.dataset.pending || "";
    const known = new Set(entries.map(([value]) => value));
    if (prev && !known.has(prev)) entries = entries.concat([[prev, prev]]);
    sel.innerHTML = t('<option value="">全部</option>') + entries.map(([value, label]) => `<option value="${escapeHtml(value)}">${escapeHtml(label)}</option>`).join("");
    sel.value = prev;
    delete sel.dataset.pending;
  }

  // renderMemoryProjectFilter lists projects of the selected tenant so the two filters cannot contradict.
  function renderMemoryProjectFilter() {
    const tenant = $("mem-filter-tenant").value;
    fillSelect("mem-filter-project", state.projects.filter((p) => !tenant || p.tenant_id === tenant).map((p) => [p.id, projectLabel(p)]));
  }

  // tenantLabel adds a short identifier only when two tenants share a name.
  function tenantLabel(tenant) {
    return state.tenants.filter((other) => other.name === tenant.name).length > 1 ? t`${tenant.name}（${tenant.id.slice(0, 6)}）` : tenant.name;
  }

  // projectLabel names a project with its tenant and, when names repeat, a short identifier.
  function projectLabel(p) {
    const twins = state.projects.filter((other) => other.name === p.name && other.tenant_id === p.tenant_id).length > 1;
    return t`${p.name}（${p.tenant_name}${twins ? " · " + p.id.slice(0, 6) : ""}）`;
  }

  // renderTenantsTable shows tenant status and copyable compact identifiers.
  function renderTenantsTable() {
    const tbody = $("tenants-table").querySelector("tbody");
    if (!state.tenants.length) { tbody.innerHTML = t('<tr><td colspan="4" class="empty-state">尚无租户，请先在概览完成快速接入。</td></tr>'); return; }
    tbody.innerHTML = state.tenants
      .map(
        (t) => `
      <tr>
        <td>${identifier(t.id)}</td>
        <td><strong>${escapeHtml(t.name)}</strong></td>
        <td><span class="badge ${t.status === "active" ? "success" : "danger"}">${statusLabel(t.status)}</span></td>
        <td>${formatDate(t.created_at)}</td>
      </tr>
    `
      )
      .join("");
  }

  const PROVIDERS = { api_token: t("客户端密钥"), oidc: t("单点登录"), mtls: t("双向 TLS 证书") };

  // renderPrincipalsTable presents identities with their explicit tenant memberships.
  function renderPrincipalsTable() {
    const tbody = $("principals-table").querySelector("tbody");
    const principals = visiblePrincipals();
    if (!principals.length) { tbody.innerHTML = t('<tr><td colspan="5" class="empty-state">尚无成员，请先创建租户和成员。</td></tr>'); return; }
    tbody.innerHTML = principals
      .map((p) => {
        const roles = (p.tenant_roles || [])
          .map((r) => `<span class="badge">${escapeHtml(r.tenant_name)} · ${escapeHtml(TENANT_ROLES[r.role] || r.role)}</span>`)
          .join(" ");
        return `
      <tr>
        <td>${identifier(p.id)}</td>
        <td><strong>${escapeHtml(p.display_name)}</strong></td>
        <td>${escapeHtml(PROVIDERS[p.identity_provider] || p.identity_provider)}<div class="field-hint"><code>${escapeHtml(p.subject)}</code></div></td>
        <td><span class="badge ${p.status === "active" ? "success" : "danger"}">${statusLabel(p.status)}</span></td>
        <td>${roles || t('<span class="muted">未加入任何租户</span>')}</td>
      </tr>`;
      })
      .join("");
  }

  // renderProjectsTable keeps project names primary and technical identifiers compact.
  function renderProjectsTable() {
    const tbody = $("projects-table").querySelector("tbody");
    if (!state.projects.length) { tbody.innerHTML = t('<tr><td colspan="5" class="empty-state">尚未关联项目，可在下方注册第一个项目。</td></tr>'); return; }
    tbody.innerHTML = state.projects
      .map((p) => {
        const markerHtml = p.marker
          ? `${identifier(p.marker)}`
          : t`<span class="muted">未生成</span>`;
        const reposHtml = (p.repositories || [])
          .map(
            (r) =>
              `<div><code>${escapeHtml(r.canonical_remote)}</code> ${
                r.workspace_path ? t`<span class="badge">子目录 ${escapeHtml(r.workspace_path)}</span>` : ""
              }</div>`
          )
          .join("");
        const membersHtml = (p.members || [])
          .map((m) => `<span class="badge">${escapeHtml(m.display_name)} · ${escapeHtml(PROJECT_ROLES[m.role] || m.role)}</span>`)
          .join(" ");
        return `
      <tr>
        <td><strong>${escapeHtml(p.name)}</strong><br/>${identifier(p.id)}</td>
        <td>${escapeHtml(p.tenant_name)}</td>
        <td>${markerHtml}</td>
        <td>${reposHtml || t('<span class="muted">未关联仓库，通过标记文件识别</span>')}</td>
        <td>${membersHtml || t('<span class="muted">仅租户管理员</span>')}</td>
      </tr>`;
      })
      .join("");
  }

  async function resolveTokenSecret(tokenId) {
    if (state.revealedTokens[tokenId]) {
      return state.revealedTokens[tokenId];
    }
    const res = await api("/api/admin/tokens/reveal", {
      method: "POST",
      body: JSON.stringify({ id: tokenId }),
    });
    if (res && res.token) {
      state.revealedTokens[tokenId] = res.token;
      return res.token;
    }
    throw new Error(t("未能获取密钥明文。"));
  }

  // renderTokensTable lists client credentials and exposes their configuration actions.
  function renderTokensTable() {
    const tbody = $("tokens-table").querySelector("tbody");
    if (!state.tokens.length) { tbody.innerHTML = t('<tr><td colspan="7" class="empty-state">尚无客户端密钥，可在下方为客户端生成密钥。</td></tr>'); return; }
    tbody.innerHTML = state.tokens
      .map((tk) => {
        const active = !tk.revoked_at;
        const revealed = state.revealedTokens[tk.id];
        const tokenCell = revealed
          ? `<code>${escapeHtml(revealed)}</code>`
          : `<code>${escapeHtml(tk.token_prefix)}…</code>${
              tk.can_reveal ? "" : t(' <small class="muted" title="服务端签名密钥变更后无法再解密这个密钥，它仍可正常鉴权；需要明文时请重新签发。">（无法查看明文）</small>')
            }`;
        const revealBtns = tk.can_reveal
          ? t`
            <button type="button" class="secondary small btn-toggle-token" data-id="${tk.id}">${revealed ? t("隐藏") : t("查看")}</button>
            <button type="button" class="secondary small btn-copy-token" data-id="${tk.id}">复制</button>
            <button type="button" class="secondary small btn-apply-token" data-id="${tk.id}">填入配置</button>
          `
          : "";
        const lastUsed = tk.last_used_at
          ? `<time title="${escapeHtml(formatDate(tk.last_used_at))}">${relativeTime(tk.last_used_at)}</time>`
          : t('<span class="muted">从未使用</span>');
        return `
      <tr class="${active ? "" : "row-muted"}">
        <td><strong>${escapeHtml(tk.name)}</strong></td>
        <td>${tokenCell}</td>
        <td>${escapeHtml(tk.tenant_name)}<div class="field-hint">${escapeHtml(tk.principal_name)}</div></td>
        <td><span class="badge nowrap ${active ? "success" : "danger"}">${active ? t("正常") : t("已吊销")}</span></td>
        <td class="date-cell">${lastUsed}</td>
        <td class="date-cell">${formatDate(tk.created_at)}</td>
        <td>
          <div class="row-actions">
            ${active ? revealBtns : ""}
            ${active ? t`<button type="button" class="danger-outline small btn-revoke-token" data-id="${tk.id}">吊销</button>` : ""}
          </div>
        </td>
      </tr>`;
      })
      .join("");

    tbody.querySelectorAll(".btn-toggle-token").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const id = btn.dataset.id;
        if (state.revealedTokens[id]) {
          delete state.revealedTokens[id];
          renderTokensTable();
          return;
        }
        try {
          await resolveTokenSecret(id);
          renderTokensTable();
        } catch (err) {
          showToast(err.message, true);
        }
      });
    });

    tbody.querySelectorAll(".btn-copy-token").forEach((btn) => {
      btn.addEventListener("click", async () => {
        try {
          const tok = await resolveTokenSecret(btn.dataset.id);
          await copyText(tok);
        } catch (err) {
          showToast(err.message, true);
        }
      });
    });

    tbody.querySelectorAll(".btn-apply-token").forEach((btn) => {
      btn.addEventListener("click", async () => {
        try {
          const tok = await resolveTokenSecret(btn.dataset.id);
          state.latestToken = tok;
          if ($("guide-token-input")) {
            $("guide-token-input").value = tok;
          }
          renderClientGuides();
          const card = $("client-guides-card");
          card.open = true;
          if (card) card.scrollIntoView({ behavior: "smooth", block: "start" });
          showToast(t("已把密钥填入客户端接入配置"));
        } catch (err) {
          showToast(err.message, true);
        }
      });
    });

    tbody.querySelectorAll(".btn-revoke-token").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const token = state.tokens.find((item) => item.id === btn.dataset.id);
        const confirmed = await confirmDialog({
          title: t("吊销这个客户端密钥？"),
          message: t`「${token ? token.name : t("该密钥")}」（${token ? token.token_prefix + "…" : ""}）吊销后立即失效，使用它的 AI 客户端将无法再访问 Engram。此操作无法撤销。`,
          confirmText: t("吊销密钥"),
        });
        if (!confirmed) return;
        try {
          await api("/api/admin/tokens/revoke", {
            method: "POST",
            body: JSON.stringify({ id: btn.dataset.id }),
          });
          showToast(t("客户端密钥已吊销"));
          refreshAllData();
        } catch (err) {
          showToast(err.message, true);
        }
      });
    });
  }

  // memoryFilters reads the list filters in the order they are written to the URL.
  const MEMORY_FILTERS = [["tenant", "mem-filter-tenant", "tenant_id"], ["scope", "mem-filter-scope", "scope"], ["project", "mem-filter-project", "project_id"], ["category", "mem-filter-category", "category"], ["tag", "mem-filter-tag", "tag"], ["deleted", "mem-filter-deleted", "deleted"], ["q", "mem-filter-q", "q"]];

  function memoryFilterValues() {
    return Object.fromEntries(MEMORY_FILTERS.map(([key, id]) => [key, $(id).value.trim()]));
  }

  // loadMemories ignores stale responses; quiet reloads keep the current rows until new ones arrive.
  async function loadMemories(options = {}) {
    const request = ++state.memoryRequest;
    if (!options.keepDetail) closeMemoryDetail(false, false);
    const tbody = $("memories-table").querySelector("tbody");
    if (!options.quiet) {
      // Remove stale actions while a new result set is pending, including on slow connections.
      state.memories = [];
      tbody.innerHTML = t('<tr><td colspan="6" class="empty-state">正在加载知识…</td></tr>');
      $("memory-list-status").textContent = t("正在加载知识…");
    }
    const filters = memoryFilterValues();
    const params = new URLSearchParams({ page: state.memoryPage, page_size: state.memoryPageSize });
    MEMORY_FILTERS.forEach(([key, , param]) => { if (filters[key]) params.set(param, filters[key]); });
    $("memories-table").setAttribute("aria-busy", "true");
    $("btn-memory-prev").disabled = true;
    $("btn-memory-next").disabled = true;
    try {
      const res = await api("/api/admin/memories?" + params.toString());
      if (request !== state.memoryRequest) return;
      state.memories = res.items || [];
      state.memoryPage = res.page;
      state.memoryTotal = res.total;
      tbody.innerHTML = state.memories.map((m) => memoryRow(m, filters.q)).join("");
      const filtered = !!(filters.tenant || filters.scope || filters.project || filters.category || filters.tag || filters.q || filters.deleted !== "active");
      if (!state.memories.length) {
        const trash = filters.deleted === "deleted";
        tbody.innerHTML = `<tr><td colspan="6" class="empty-state"><strong>${trash ? t("回收站是空的") : filtered ? t("未找到匹配的知识") : t("知识库还是空的")}</strong><p>${trash ? t("删除的知识会先进入回收站，可随时恢复。") : filtered ? t("试试其他关键词，或点击清除筛选。") : t("连接 AI 客户端后，让 Agent 保存第一条共享记忆。")}</p></td></tr>`;
      }
      const trashHint = filters.deleted === "active" && state.deletedCount ? t` · <button type="button" class="link-btn" id="btn-open-trash">回收站 ${state.deletedCount} 条</button>` : "";
      $("memory-list-status").innerHTML = t`共 ${res.total} 条知识${filtered ? t(" · 已应用筛选") : ""}${trashHint}`;
      const pages = Math.max(1, Math.ceil(res.total / state.memoryPageSize));
      $("memory-page-info").textContent = t`第 ${res.page} / ${pages} 页`;
      $("btn-memory-prev").disabled = res.page <= 1;
      $("btn-memory-next").disabled = res.page >= pages;
      if (activeTab() === "tab-memories") syncLocation();
    } catch (err) {
      if (request !== state.memoryRequest) return;
      state.memories = [];
      tbody.innerHTML = t('<tr><td colspan="6" class="empty-state">知识加载失败，请点击搜索重试。</td></tr>');
      $("memory-list-status").textContent = t("加载失败，当前结果不可用");
      $("memory-page-info").textContent = "";
      showToast(err.message, true);
    } finally {
      if (request === state.memoryRequest) $("memories-table").removeAttribute("aria-busy");
    }
  }

  // memoryRow renders one list entry; tags, categories and projects double as filter shortcuts.
  function memoryRow(m, keyword) {
    const deleted = !!m.deleted_at;
    const project = m.scope === "global" ? "" : `<button type="button" class="chip-link" data-filter="mem-filter-project" data-value="${m.project_id}">${escapeHtml(m.project_name)}</button>`;
    const tags = (m.tags || []).map((tag) => `<button type="button" class="tag-chip" data-filter="mem-filter-tag" data-value="${escapeHtml(tag)}">${escapeHtml(tag)}</button>`).join("");
    return `<tr data-memory-id="${m.id}" class="memory-row${deleted ? " row-muted" : ""}">
      <td class="memory-main"><button type="button" class="memory-title btn-view-mem" data-id="${m.id}">${highlight(m.title, keyword)}</button>${m.is_pinned ? t('<span class="badge warning">置顶</span>') : ""}<p class="memory-snippet">${memorySnippet(m.content_md, keyword)}</p></td>
      <td><span class="badge nowrap">${m.scope === "global" ? t("全局") : t("项目")}</span><div>${project}</div></td>
      <td><button type="button" class="chip-link" data-filter="mem-filter-category" data-value="${escapeHtml(m.category)}">${escapeHtml(categoryLabel(m.category))}</button><div class="tag-list">${tags}</div></td>
      <td class="nowrap">v${m.version} <span class="badge ${deleted ? "danger" : "success"}">${deleted ? t("已删除") : statusLabel(m.status)}</span></td>
      <td class="date-cell"><time title="${escapeHtml(formatDate(m.updated_at))}">${relativeTime(m.updated_at)}</time></td>
      <td><div class="row-actions">${deleted ? t`<button type="button" class="small btn-restore-mem" data-id="${m.id}">恢复</button>` : t`<button type="button" class="secondary small btn-view-mem" data-id="${m.id}">查看</button>`}</div></td>
    </tr>`;
  }

  // splitFrontmatter separates a leading YAML block so its simple fields can be shown as metadata.
  function splitFrontmatter(markdown) {
    const match = /^---\r?\n([\s\S]*?)\r?\n---\r?\n?/.exec(String(markdown || ""));
    if (!match) return { fields: [], body: String(markdown || "") };
    const fields = match[1].split(/\r?\n/).filter((line) => line.trim()).map((line) => {
      const pair = /^([\w.-]+):\s*(.*)$/.exec(line);
      return pair ? [pair[1], pair[2]] : ["", line.trim()];
    });
    return { fields, body: String(markdown).slice(match[0].length) };
  }

  // memorySnippet produces a one-line plain-text excerpt, centred on the keyword when it matches.
  function memorySnippet(markdown, keyword) {
    const text = splitFrontmatter(markdown).body
      .replace(/```[\s\S]*?```/g, " ")
      .replace(/^\s{0,3}(#{1,6}|>|[-*+]|\d+\.)\s+/gm, "")
      .replace(/[`*|]/g, "")
      .replace(/\[([^\]]*)\]\([^)]*\)/g, "$1")
      .replace(/\s+/g, " ")
      .trim();
    if (!text) return t('<span class="muted">（无正文）</span>');
    let start = 0;
    const index = keyword ? text.toLowerCase().indexOf(keyword.toLowerCase()) : -1;
    if (index > 40) start = index - 30;
    const excerpt = (start > 0 ? "…" : "") + text.slice(start, start + 140) + (start + 140 < text.length ? "…" : "");
    return highlight(excerpt, keyword);
  }

  // highlight escapes text and marks case-insensitive keyword matches.
  function highlight(text, keyword) {
    const value = String(text == null ? "" : text);
    if (!keyword) return escapeHtml(value);
    const lower = value.toLowerCase();
    const needle = keyword.toLowerCase();
    let output = "", cursor = 0, index;
    while ((index = lower.indexOf(needle, cursor)) !== -1) {
      output += escapeHtml(value.slice(cursor, index)) + "<mark>" + escapeHtml(value.slice(index, index + needle.length)) + "</mark>";
      cursor = index + needle.length;
    }
    return output + escapeHtml(value.slice(cursor));
  }

  // applyMemoryFilter narrows the list from a chip in a row or the detail drawer.
  function applyMemoryFilter(id, value) {
    if (id === "mem-filter-project") { $("mem-filter-scope").value = "project"; }
    const sel = $(id);
    if (!Array.from(sel.options).some((option) => option.value === value)) sel.insertAdjacentHTML("beforeend", `<option value="${escapeHtml(value)}">${escapeHtml(value)}</option>`);
    sel.value = value;
    if (activeTab() !== "tab-memories") switchMainTab("tab-memories");
    state.memoryPage = 1;
    syncLocation(true);
    loadMemories();
  }

  const REVISION_FIELDS = { title: t("标题"), category: t("分类"), tags: t("标签"), status: t("状态"), is_pinned: t("置顶"), deleted_at: t("删除时间") };

  // revisionContent reads snapshot text from both the current and the legacy snapshot field names.
  function revisionContent(snapshot) { return snapshot.content ?? snapshot.content_md ?? ""; }

  // viewMemoryDetail opens immediately and guards against out-of-order history requests.
  async function viewMemoryDetail(memId, updateURL = true) {
    const request = ++state.detailRequest;
    state.selectedMemory = null;
    if ($("memory-detail-card").classList.contains("hidden")) state.detailTrigger = document.activeElement;
    $("memory-detail-card").classList.remove("hidden");
    document.body.classList.add("drawer-open");
    $("detail-title").textContent = t("正在加载知识…");
    $("detail-meta").textContent = "";
    $("detail-frontmatter").classList.add("hidden");
    $("detail-preview").textContent = t("正在加载正文与历史…");
    $("detail-revisions").textContent = "";
    $("detail-content").textContent = "";
    ["btn-pin-memory", "btn-delete-memory", "btn-restore-memory", "btn-hard-delete-memory"].forEach((id) => $(id).classList.add("hidden"));
    $("btn-detail-preview").click();
    $("btn-close-detail").focus();
    try {
      const res = await api(`/api/admin/memories?memory_id=${encodeURIComponent(memId)}`);
      const mem = (res.items || [])[0];
      if (!mem) throw new Error(t("这条知识已不存在，请关闭详情并刷新列表。"));
      const revRes = await api(`/api/admin/memories/revisions?memory_id=${encodeURIComponent(memId)}`);
      if (request !== state.detailRequest) return;
      state.selectedMemory = mem;
      const deleted = !!mem.deleted_at;
      $("detail-title").textContent = mem.title;
      const chips = [
        `<span class="badge">${mem.scope === "global" ? t("全局记忆") : t("项目 · ") + escapeHtml(mem.project_name)}</span>`,
        `<button type="button" class="chip-link" data-filter="mem-filter-category" data-value="${escapeHtml(mem.category)}">${escapeHtml(categoryLabel(mem.category))}</button>`,
        ...(mem.tags || []).map((tag) => `<button type="button" class="tag-chip" data-filter="mem-filter-tag" data-value="${escapeHtml(tag)}">${escapeHtml(tag)}</button>`),
        mem.is_pinned ? t('<span class="badge warning">置顶</span>') : "",
        deleted ? t('<span class="badge danger">已删除</span>') : "",
        t`<span class="meta-text">v${mem.version} · ${escapeHtml(formatDate(mem.updated_at))} 更新</span>`,
        identifier(mem.id),
      ];
      $("detail-meta").innerHTML = chips.filter(Boolean).join("");
      const { fields, body } = splitFrontmatter(mem.content_md);
      $("detail-frontmatter").innerHTML = fields.map(([key, value]) => `<span>${key ? `<b>${escapeHtml(key)}</b> ` : ""}${escapeHtml(value)}</span>`).join("");
      $("detail-frontmatter").classList.toggle("hidden", !fields.length);
      $("detail-content").textContent = mem.content_md || "";
      $("detail-preview").innerHTML = window.EngramMarkdown.render(body);
      $("btn-pin-memory").textContent = mem.is_pinned ? t("取消置顶") : t("置顶");
      $("btn-pin-memory").classList.toggle("hidden", deleted);
      $("btn-delete-memory").classList.toggle("hidden", deleted);
      $("btn-restore-memory").classList.toggle("hidden", !deleted);
      $("btn-hard-delete-memory").classList.toggle("hidden", !deleted);
      const revisions = revRes.items || [];
      $("detail-revisions").innerHTML = revisions.map((revision, index) => {
        const previous = revisions[index + 1];
        const snap = revision.snapshot || {};
        const old = previous ? previous.snapshot || {} : {};
        const operation = { create: t("创建"), update: t("更新"), delete: t("删除"), restore: t("恢复"), promote: t("提升至全局") }[revision.operation] || revision.operation;
        const meta = previous ? Object.keys(REVISION_FIELDS).filter((key) => JSON.stringify(snap[key] ?? null) !== JSON.stringify(old[key] ?? null)).map((key) => t`${REVISION_FIELDS[key]}：${revisionValue(key, old[key])} → ${revisionValue(key, snap[key])}`).join("\n") : "";
        const diff = previous || revision.operation === "create" ? window.EngramMarkdown.diff(revisionContent(old), revisionContent(snap)) : "";
        return t`<details class="revision-item"><summary><strong>v${revision.version} · ${escapeHtml(operation)}</strong><span>${formatDate(revision.created_at)} · ${escapeHtml(actorName(revision.actor_id))}</span></summary>${meta ? `<pre class="revision-meta">${escapeHtml(meta)}</pre>` : ""}${diff}<details><summary>原始快照</summary><pre class="code-box">${escapeHtml(JSON.stringify(revision.snapshot, null, 2))}</pre></details></details>`;
      }).join("") || t('<p class="empty-state">暂无版本历史</p>');
      if (updateURL) syncLocation(true);
    } catch (err) {
      if (request !== state.detailRequest) return;
      state.selectedMemory = null;
      $("detail-title").textContent = t("无法打开知识");
      $("detail-preview").textContent = err.message;
      showToast(err.message, true);
    }
  }

  // revisionValue formats a changed revision field for the history summary.
  function revisionValue(key, value) {
    if (key === "is_pinned") return value ? t("是") : t("否");
    if (key === "deleted_at") return value ? formatDate(value) : t("无");
    if (key === "status") return value ? statusLabel(value) : t("无");
    if (Array.isArray(value)) return value.length ? value.join(t("、")) : t("无");
    return value == null || value === "" ? t("无") : JSON.stringify(value);
  }

  // closeMemoryDetail cancels pending rendering and restores the original list focus.
  function closeMemoryDetail(updateURL = true, restoreFocus = true) {
    const wasOpen = !$("memory-detail-card").classList.contains("hidden");
    state.detailRequest++;
    state.selectedMemory = null;
    $("memory-detail-card").classList.add("hidden");
    document.body.classList.remove("drawer-open");
    if (wasOpen && restoreFocus && state.detailTrigger && state.detailTrigger.isConnected) state.detailTrigger.focus({ preventScroll: true });
    if (updateURL && activeTab() === "tab-memories") syncLocation();
  }

  // memoryAction applies delete, restore or pin on the displayed version and refreshes both views.
  async function memoryAction(action, mem, extra = {}) {
    const labels = { delete: t("已移入回收站，可随时恢复"), restore: t("已从回收站恢复"), pin: extra.is_pinned ? t("已置顶") : t("已取消置顶") };
    try {
      await api(`/api/admin/memories/${action}`, {
        method: "POST",
        body: JSON.stringify(Object.assign({ tenant_id: mem.tenant_id, memory_id: mem.id, expected_version: mem.version }, extra)),
      });
      showToast(t`「${mem.title}」${labels[action]}`);
      const reopen = detailIsOpen() && state.selectedMemory && state.selectedMemory.id === mem.id;
      await refreshAllData({ quiet: true });
      if (reopen) {
        await loadMemories({ quiet: true, keepDetail: true });
        await viewMemoryDetail(mem.id, false);
      }
    } catch (err) {
      showToast(/version conflict/i.test(err.message) ? t("这条知识刚被其他客户端修改，请刷新后重试。") : err.message, true);
    }
  }

  function formatMCPConfig(rawToken) {
    const mcpUrl = `${window.location.origin}/mcp`;
    return JSON.stringify(
      {
        token: rawToken,
        mcpServers: {
          engram: {
            type: "streamable-http",
            url: mcpUrl,
            headers: {
              Authorization: `Bearer ${rawToken}`,
            },
          },
        },
      },
      null,
      2
    );
  }

  function getMemoryInstructionSnippet() {
    return [
      t("## Engram 共享记忆使用规范"),
      "",
      t("如果当前会话已接入 `engram` MCP 工具，请按以下流程使用："),
      "",
      t("1. **会话开始先识别项目**："),
      t("   - 获取当前仓库的 `git remote -v`（以及根目录或子目录 `.engram-project` 文件中的 UUID，如有），调用 `memory_discover` 获取 `project_context` 与范围索引。"),
      t("   - 注意：`workspace_path` 仅用于 Monorepo 子目录（相对于 Git 根目录），**不要传入本地绝对路径**；若当前不在 Git 仓库中且仅需全局记忆，传入 `allow_global_only: true`。"),
      t("   - 同一会话内复用返回的 `project_context`，无需重复调用 `memory_discover`。"),
      t("2. **先查索引，再按需检索与读取**："),
      t("   - 先浏览 `memory_discover` 返回的 `pinned`、`category_stats` 和 `recent`，若已命中目标可直接调用 `memory_read`。"),
      t("   - 需要按关键词、分类或标签检索时调用 `memory_query`，定位到 `memory_id` 后再调用 `memory_read` 读取完整 Markdown 正文。"),
      t("3. **任务过程中及时沉淀与更新**："),
      t("   - 遇到有长期价值的架构约定、环境配置、排障结论或跨项目规范时，调用 `memory_create` / `memory_update` 沉淀或更新记忆（更新需传 `expected_version` 与唯一 `idempotency_key`），**严禁写入密码、Token、私钥等敏感凭据**。"),
    ].join("\n");
  }

  // renderClientGuides previews credentials only in memory and uses a client-reachable origin.
  function renderClientGuides() {
    const tokenInput = $("guide-token-input");
    const rawToken = (tokenInput ? tokenInput.value.trim() : state.latestToken) || "<YOUR_API_TOKEN>";
    const serverInput = $("guide-server-input");
    const address = serverInput.value.trim() || window.location.origin;
    let serverURL;
    try {
      serverURL = new URL(address);
      if (!["http:", "https:"].includes(serverURL.protocol) || serverURL.username || serverURL.password || serverURL.search || serverURL.hash || !["/", ""].includes(serverURL.pathname)) throw new Error("invalid origin");
      serverInput.setCustomValidity("");
    } catch (_) {
      serverInput.setCustomValidity(t("请输入不含路径、账号和参数的 HTTP 或 HTTPS 服务地址。"));
      document.querySelectorAll('.client-pane [id$="-mcp"], #guide-claude-cli').forEach((el) => { el.textContent = t("请先填写有效的服务地址。"); });
      return;
    }
    const mcpUrl = `${serverURL.origin}/mcp`;
    $("guide-server-hint").textContent = ["localhost", "127.0.0.1", "[::1]"].includes(serverURL.hostname) ? t("当前地址仅适合同机使用；跨设备时请改为可访问的域名或 IP。") : t("请确认 AI 客户端所在设备可以访问此地址。");

    const codexToml = [
      "[mcp_servers.engram]",
      "enabled = true",
      `url = "${mcpUrl}"`,
      `http_headers = { Authorization = ${JSON.stringify("Bearer " + rawToken)} }`,
      "",
      t("# 若仅在命令行 CLI 使用，也可改用环境变量方式："),
      '# bearer_token_env_var = "ENGRAM_API_TOKEN"',
    ].join("\n");

    const claudeJson = JSON.stringify(
      {
        mcpServers: {
          engram: {
            type: "http",
            url: mcpUrl,
            headers: {
              Authorization: `Bearer ${rawToken}`,
            },
          },
        },
      },
      null,
      2
    );

    const claudeCli = `claude mcp add --transport http --scope user engram ${shellQuote(mcpUrl)} --header ${shellQuote("Authorization: Bearer " + rawToken)}`;

    const geminiCliJson = JSON.stringify(
      {
        mcpServers: {
          engram: {
            httpUrl: mcpUrl,
            headers: {
              Authorization: `Bearer ${rawToken}`,
            },
          },
        },
      },
      null,
      2
    );

    const geminiAgyJson = JSON.stringify(
      {
        mcpServers: {
          engram: {
            url: mcpUrl,
            headers: {
              Authorization: `Bearer ${rawToken}`,
            },
          },
        },
      },
      null,
      2
    );

    const instructions = getMemoryInstructionSnippet();
    if ($("guide-codex-mcp")) $("guide-codex-mcp").textContent = codexToml;
    if ($("guide-codex-rules")) $("guide-codex-rules").textContent = instructions;
    if ($("guide-claude-mcp")) $("guide-claude-mcp").textContent = claudeJson;
    if ($("guide-claude-cli")) $("guide-claude-cli").textContent = claudeCli;
    if ($("guide-claude-rules")) $("guide-claude-rules").textContent = instructions;
    if ($("guide-gemini-cli-mcp")) $("guide-gemini-cli-mcp").textContent = geminiCliJson;
    if ($("guide-gemini-agy-mcp")) $("guide-gemini-agy-mcp").textContent = geminiAgyJson;
    if ($("guide-gemini-rules")) $("guide-gemini-rules").textContent = instructions;
  }

  // activeTab returns the current navigation destination for URL persistence.
  function activeTab() { return document.querySelector(".tab-btn.active").dataset.tab; }

  // switchMainTab maintains navigation state without carrying an unrelated detail panel.
  function switchMainTab(tabId, updateURL = true) {
    if (!document.querySelector(`.tab-btn[data-tab="${tabId}"]`)) tabId = "tab-overview";
    closeMemoryDetail(false, false);
    document.querySelectorAll(".tab-btn").forEach((b) => {
      b.classList.toggle("active", b.dataset.tab === tabId);
      b.setAttribute("aria-current", b.dataset.tab === tabId ? "page" : "false");
    });
    document.querySelectorAll(".tab-pane").forEach((p) => p.classList.toggle("hidden", p.id !== tabId));
    if (updateURL) syncLocation(true);
  }

  // syncLocation stores only navigation and search state; credentials never enter URLs.
  function syncLocation(push = false) {
    if (push) state.locationRequest++;
    const params = new URLSearchParams({ tab: activeTab() });
    if (activeTab() === "tab-memories") {
      MEMORY_FILTERS.forEach(([key, id]) => { const value = $(id).value; if (value && !(key === "deleted" && value === "active")) params.set(key, value); });
      params.set("page", state.memoryPage);
      params.set("size", state.memoryPageSize);
      if (state.selectedMemory) params.set("memory", state.selectedMemory.id);
    }
    const hash = "#" + params.toString();
    if (location.hash !== hash) history[push ? "pushState" : "replaceState"](null, "", hash);
  }

  // restoreLocation restores filters after selectors are populated and supports off-page detail links.
  async function restoreLocation() {
    const request = ++state.locationRequest;
    const params = new URLSearchParams(location.hash.slice(1));
    const tab = params.get("tab");
    const allowed = Array.from(document.querySelectorAll(".tab-btn")).map((button) => button.dataset.tab);
    switchMainTab(allowed.includes(tab) ? tab : "tab-overview", false);
    MEMORY_FILTERS.forEach(([key, id]) => {
      const el = $(id); const fallback = key === "deleted" ? "active" : ""; const value = params.get(key) || fallback;
      if (el.tagName === "SELECT" && value && !Array.from(el.options).some((option) => option.value === value)) {
        // Deep links may name a category, tag or project that has no active entries yet.
        if (["category", "tag", "project"].includes(key)) el.insertAdjacentHTML("beforeend", `<option value="${escapeHtml(value)}">${escapeHtml(value)}</option>`);
        else { el.value = fallback; return; }
      }
      el.value = value;
    });
    const page = Number(params.get("page"));
    state.memoryPage = Number.isInteger(page) && page > 0 && page <= 1000000 ? page : 1;
    const size = Number(params.get("size"));
    state.memoryPageSize = [20, 50, 100].includes(size) ? size : 20;
    $("mem-page-size").value = state.memoryPageSize;
    await loadMemories();
    if (request !== state.locationRequest) return;
    const memoryID = params.get("memory");
    if (activeTab() === "tab-memories" && /^[a-f0-9-]{36}$/i.test(memoryID || "")) await viewMemoryDetail(memoryID, false);
    if (request === state.locationRequest) syncLocation();
  }

  // loadRecentMemories keeps the dashboard useful after the first client is connected.
  async function loadRecentMemories() {
    const res = await api("/api/admin/memories?page_size=5&deleted=active&sort=recent");
    $("recent-memories").innerHTML = (res.items || []).map((mem) => `<button type="button" class="recent-item" data-id="${mem.id}"><span><strong>${escapeHtml(mem.title)}</strong><small>${escapeHtml(mem.project_name || t("全局记忆"))} · ${escapeHtml(mem.category)}</small></span><time title="${escapeHtml(formatDate(mem.updated_at))}">${relativeTime(mem.updated_at)}</time></button>`).join("") || t('<p class="empty-state">还没有知识。完成客户端接入后，让 Agent 保存第一条记忆。</p>');
    $("recent-memories").querySelectorAll("button").forEach((btn) => btn.addEventListener("click", () => { switchMainTab("tab-memories"); viewMemoryDetail(btn.dataset.id); }));
  }

  // identifier shortens technical IDs while retaining their complete value for copying.
  function identifier(value) { return t`<button type="button" class="id-copy" data-copy-value="${escapeHtml(value)}" title="复制完整标识：${escapeHtml(value)}" aria-label="复制完整标识 ${escapeHtml(value)}">${escapeHtml(value.slice(0, 8))}… ⧉</button>`; }

  // statusLabel translates storage states into consistent user-facing labels.
  function statusLabel(value) { return { active: t("正常"), disabled: t("已停用"), deprecated: t("已过时"), archived: t("已归档") }[value] || escapeHtml(value); }

  // formatDate uses a consistent local-language timestamp across tables and history.
  function formatDate(value) { return new Date(value).toLocaleString(locale, { hour12: false }); }

  // relativeTime describes recent moments in words and falls back to the calendar date.
  function relativeTime(value) {
    const seconds = Math.round((Date.now() - new Date(value).getTime()) / 1000);
    if (seconds < 60) return t("刚刚");
    if (seconds < 3600) return t`${Math.floor(seconds / 60)} 分钟前`;
    if (seconds < 86400) return t`${Math.floor(seconds / 3600)} 小时前`;
    if (seconds < 86400 * 7) return t`${Math.floor(seconds / 86400)} 天前`;
    return new Date(value).toLocaleDateString(locale);
  }

  // categoryLabel shows common memory categories in Chinese while keeping custom ones verbatim.
  function categoryLabel(value) {
    const names = { convention: t("约定规范"), architecture: t("架构设计"), decision: t("决策记录"), troubleshooting: t("排障经验"), environment: t("环境配置"), runbook: t("操作手册"), preference: t("个人偏好"), note: t("笔记"), workflow: t("工作流程"), reference: t("参考资料") };
    return names[value] ? `${names[value]} · ${value}` : value;
  }

  // confirmDialog asks before irreversible actions and resolves to the person's choice.
  function confirmDialog({ title, message, confirmText = t("确认"), danger = true }) {
    const modal = $("confirm-modal");
    $("confirm-title").textContent = title;
    $("confirm-message").textContent = message;
    $("btn-confirm-ok").textContent = confirmText;
    $("btn-confirm-ok").className = danger ? "danger" : "";
    const opener = document.activeElement;
    modal.classList.remove("hidden");
    $("btn-confirm-cancel").focus();
    return new Promise((resolve) => {
      const finish = (result) => {
        modal.classList.add("hidden");
        state.confirmResolve = null;
        if (opener && opener.isConnected) opener.focus({ preventScroll: true });
        resolve(result);
      };
      state.confirmResolve = finish;
    });
  }

  // FormError attaches a message to a specific field instead of the whole form.
  class FormError extends Error {
    constructor(message, field) { super(message); this.field = field; }
  }

  // clearFormErrors removes inline messages once the person edits or resubmits the form.
  function clearFormErrors(form) {
    form.querySelectorAll(".form-error, .field-error").forEach((el) => el.remove());
    form.querySelectorAll('[aria-invalid="true"]').forEach((el) => { el.removeAttribute("aria-invalid"); el.removeAttribute("aria-errormessage"); });
  }

  // showFormError places the failure next to the field or submit button that caused it.
  function showFormError(form, err) {
    const message = (err && err.message) || t("操作失败，请重试。");
    const field = err && err.field ? $(err.field) : null;
    if (field) {
      const hint = document.createElement("small");
      hint.className = "field-error";
      hint.id = field.id + "-error";
      hint.textContent = message;
      field.setAttribute("aria-invalid", "true");
      field.setAttribute("aria-errormessage", hint.id);
      field.insertAdjacentElement("afterend", hint);
      field.focus();
      return;
    }
    const box = document.createElement("div");
    box.className = "form-error";
    box.setAttribute("role", "alert");
    box.textContent = message;
    const button = form.querySelector('button[type="submit"]');
    const anchor = button && button.parentElement !== form ? button.parentElement : button;
    if (anchor) anchor.before(box); else form.append(box);
  }

  // shellQuote protects copied CLI arguments from expansion by POSIX shells.
  function shellQuote(value) { return "'" + value.replace(/'/g, "'\"'\"'") + "'"; }

  // updatePrincipalMode exposes identity creation and tenant permission changes as distinct actions.
  function updatePrincipalMode() {
    const mode = $("prin-mode").value;
    const creating = mode === "create";
    ["provider", "subject", "name"].forEach((field) => $("prin-" + field + "-group").classList.toggle("hidden", !creating));
    $("prin-subject").required = creating;
    $("prin-name").required = creating;
    $("prin-existing-group").classList.toggle("hidden", creating);
    $("prin-existing").required = !creating;
    const principal = state.principals.find((item) => item.id === $("prin-existing").value);
    const membership = principal && (principal.tenant_roles || []).find((role) => role.tenant_id === $("prin-tenant").value);
    $("prin-existing-hint").textContent = principal ? t`成员：${principal.display_name} · 当前租户角色：${membership ? TENANT_ROLES[membership.role] || membership.role : t("尚未绑定")}。${mode === "update_role" ? t("提交将明确替换此租户角色。") : t("绑定不会修改成员名称或已有角色。")}` : t("请先创建成员。");
    $("btn-save-principal").textContent = { create: t("创建新成员"), bind: t("绑定到当前租户"), update_role: t("保存租户角色变更") }[mode];
  }

  async function copyText(text) {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(text);
        showToast(t("已复制到剪贴板"));
        return;
      }
    } catch (_) {
      // Fallback below
    }
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    try {
      document.execCommand("copy");
      showToast(t("已复制到剪贴板"));
    } catch (_) {
      showToast(t("复制失败，请手动选中复制"), true);
    } finally {
      document.body.removeChild(ta);
    }
  }

  // escapeHtml escapes text and attributes inserted into dynamic console markup.
  function escapeHtml(s) {
    return String(s == null ? "" : s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  // onSubmit prevents duplicate writes, shows failures inline, and releases the busy state afterwards.
  function onSubmit(id, handler) {
    const form = $(id);
    form.addEventListener("input", () => clearFormErrors(form));
    form.addEventListener("submit", async (event) => {
      event.preventDefault();
      if (form.dataset.submitting === "true") return;
      clearFormErrors(form);
      const button = form.querySelector('button[type="submit"]');
      form.dataset.submitting = "true";
      if (button) { button.disabled = true; button.setAttribute("aria-busy", "true"); }
      try { await handler(event); }
      catch (err) { showFormError(form, err); }
      finally { form.dataset.submitting = "false"; if (button) { button.disabled = false; button.removeAttribute("aria-busy"); } }
    });
  }

  // Keep wide tables scrollable inside their cards instead of widening the document.
  document.querySelectorAll("table").forEach((table) => {
    const wrapper = document.createElement("div");
    wrapper.className = "table-scroll";
    wrapper.tabIndex = 0;
    wrapper.setAttribute("role", "region");
    wrapper.setAttribute("aria-label", (table.closest(".card, .glossary-section")?.querySelector("h2, h3")?.textContent || t("数据表格")) + t("，可横向滚动"));
    table.before(wrapper);
    wrapper.appendChild(table);
  });

  // Event Listeners
  document.querySelectorAll(".tab-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      switchMainTab(btn.dataset.tab);
      window.scrollTo(0, 0);
      refreshIfStale(3000);
    });
  });
  $("btn-refresh").addEventListener("click", () => refreshAllData({ quiet: true }));
  // Agents write through MCP in the background, so a returning tab picks up their changes.
  document.addEventListener("visibilitychange", () => { if (document.visibilityState === "visible") refreshIfStale(30000); });
  window.addEventListener("focus", () => refreshIfStale(30000));
  document.querySelectorAll("[data-open-tab]").forEach((btn) => btn.addEventListener("click", () => { switchMainTab(btn.dataset.openTab); window.scrollTo(0, 0); }));
  window.addEventListener("popstate", () => { if (state.locationReady) restoreLocation(); });

  document.querySelectorAll("#client-guides-card .client-tab-btn[data-client]").forEach((btn) => {
    btn.addEventListener("click", () => {
      const target = btn.dataset.client;
      document.querySelectorAll("#client-guides-card .client-tab-btn[data-client]").forEach((b) => b.classList.toggle("active", b === btn));
      document.querySelectorAll(".client-pane").forEach((p) => p.classList.toggle("hidden", p.id !== `client-pane-${target}`));
    });
  });

  document.querySelectorAll(".copy-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      const target = $(btn.dataset.copyTarget);
      if (target) {
        copyText(target.textContent || "");
      }
    });
  });

  if ($("guide-token-input")) {
    $("guide-token-input").addEventListener("input", renderClientGuides);
  }

  $("btn-qs-again").addEventListener("click", () => {
    $("qs-result").classList.add("hidden");
    $("quickstart-form").classList.remove("hidden");
    $("qs-client").value = "";
    $("qs-client").focus();
  });

  if ($("btn-go-client-guides")) {
    $("btn-go-client-guides").addEventListener("click", () => {
      switchMainTab("tab-tokens");
      const card = $("client-guides-card");
      card.open = true;
      if (card) card.scrollIntoView({ behavior: "smooth", block: "start" });
    });
  }

  onSubmit("login-form", async (e) => {
    e.preventDefault();
    const s = await api("/api/admin/login", {
      method: "POST",
      body: JSON.stringify({
        username: $("login-username").value,
        password: $("login-password").value,
      }),
    });
    $("login-password").value = "";
    renderView(s);
  });

  onSubmit("force-password-form", async (e) => {
    e.preventDefault();
    const curPwd = $("current-admin-password").value;
    const p1 = $("new-admin-password").value;
    const p2 = $("confirm-admin-password").value;
    if (p1 !== p2) throw new FormError(t("两次输入的密码不一致，请重新确认。"), "confirm-admin-password");
    const s = await api("/api/admin/change-password", {
      method: "POST",
      body: JSON.stringify({
        current_password: curPwd,
        new_password: p1,
      }),
    });
    $("current-admin-password").value = "";
    $("new-admin-password").value = "";
    $("confirm-admin-password").value = "";
    showToast(t("管理员密码已更新！"));
    renderView(s);
  });

  $("btn-open-change-pwd").addEventListener("click", () => {
    document.querySelector(".account-menu").open = false;
    $("pwd-view-title").textContent = t("修改管理员密码");
    $("pwd-view-subtitle").textContent = t("请输入当前密码，再设置新的管理员密码。修改后其他会话需要重新登录。");
    $("app-view").classList.add("hidden");
    $("current-admin-password-group").classList.remove("hidden");
    $("current-admin-password").required = true;
    $("btn-cancel-change-pwd").classList.remove("hidden");
    $("force-password-view").classList.remove("hidden");
  });

  $("btn-cancel-change-pwd").addEventListener("click", () => {
    $("current-admin-password").value = "";
    $("new-admin-password").value = "";
    $("confirm-admin-password").value = "";
    $("force-password-view").classList.add("hidden");
    $("app-view").classList.remove("hidden");
  });

  $("btn-logout").addEventListener("click", async () => {
    await api("/api/admin/logout", { method: "POST" });
    state.latestToken = "";
    state.revealedTokens = {};
    if ($("guide-token-input")) {
      $("guide-token-input").value = "";
    }
    renderClientGuides();
    closeMemoryDetail(false, false);
    $("qs-result").classList.add("hidden");
    $("quickstart-form").classList.remove("hidden");
    $("qs-result-box").textContent = "";
    $("new-token-box").classList.add("hidden");
    $("new-token-content").textContent = "";
    $("client-guides-card").open = false;
    document.querySelector(".account-menu").open = false;
    renderView({ authenticated: false });
  });

  onSubmit("quickstart-form", async (e) => {
    e.preventDefault();
    const res = await api("/api/admin/quickstart", {
      method: "POST",
      body: JSON.stringify({
        tenant_name: $("qs-tenant").value,
        principal_subject: $("qs-subject").value,
        principal_display_name: $("qs-display").value,
        tenant_role: $("qs-tenant-role").value,
        project_name: $("qs-project").value,
        remote_url: $("qs-remote").value,
        workspace_path: $("qs-workspace").value,
        client_name: $("qs-client").value,
      }),
    });
    state.latestToken = res.token || "";
    if (res.token_id && res.token) {
      state.revealedTokens[res.token_id] = res.token;
    }
    if ($("guide-token-input")) {
      $("guide-token-input").value = state.latestToken;
    }
    renderClientGuides();
    // Fold the form away so a second click cannot silently issue another credential.
    $("quickstart-form").classList.add("hidden");
    $("qs-result").classList.remove("hidden");
    const notes = [t`已为客户端「${res.token_name}」生成密钥。`];
    if (res.project_reused) notes.push(t`项目「${$("qs-project").value.trim()}」已存在，已直接复用。`);
    if (res.remote_elsewhere) notes.push(t("该仓库地址已关联到其他项目，未重复关联。"));
    notes.push(t("下一步选择你的 AI 客户端并复制配置。"));
    $("qs-result-summary").textContent = notes.join("");
    $("qs-result-box").textContent = JSON.stringify(
      {
        tenant_id: res.tenant_id,
        principal_id: res.principal_id,
        project_id: res.project_id || null,
        project_marker: res.project_marker || null,
        canonical_remote: res.canonical_remote || null,
        api_token: res.token,
        mcp_client_config: JSON.parse(formatMCPConfig(res.token)).mcpServers,
      },
      null,
      2
    );
    showToast(t("接入信息已就绪，下一步复制客户端配置。"));
    await refreshAllData();
    $("qs-result").scrollIntoView({ behavior: "smooth", block: "center" });
  });

  onSubmit("create-tenant-form", async (e) => {
    e.preventDefault();
    await api("/api/admin/tenants", {
      method: "POST",
      body: JSON.stringify({ name: $("tenant-name").value }),
    });
    $("tenant-name").value = "";
    showToast(t("租户已创建"));
    refreshAllData();
  });

  ["prin-mode", "prin-existing", "prin-tenant"].forEach((id) => $(id).addEventListener("change", updatePrincipalMode));

  onSubmit("create-principal-form", async (e) => {
    e.preventDefault();
    await api("/api/admin/principals", {
      method: "POST",
      body: JSON.stringify({
        mode: $("prin-mode").value,
        principal_id: $("prin-existing").value,
        tenant_id: $("prin-tenant").value,
        identity_provider: $("prin-provider").value,
        subject: $("prin-subject").value,
        display_name: $("prin-name").value,
        tenant_role: $("prin-role").value,
      }),
    });
    $("prin-subject").value = "";
    $("prin-name").value = "";
    showToast({ create: t("新成员已创建并授权"), bind: t("已有成员已绑定，名称和其他权限未变更"), update_role: t("当前租户角色已更新") }[$("prin-mode").value]);
    refreshAllData();
  });

  onSubmit("create-project-form", async (e) => {
    e.preventDefault();
    await api("/api/admin/projects", {
      method: "POST",
      body: JSON.stringify({
        tenant_id: $("proj-tenant").value,
        name: $("proj-name").value,
        remote_url: $("proj-remote").value,
        workspace_path: $("proj-workspace").value,
        with_marker: $("proj-with-marker").checked,
      }),
    });
    $("proj-name").value = "";
    $("proj-remote").value = "";
    $("proj-workspace").value = "";
    showToast(t("项目注册成功"));
    refreshAllData();
  });

  onSubmit("add-project-member-form", async (e) => {
    e.preventDefault();
    await api("/api/admin/projects/members", {
      method: "POST",
      body: JSON.stringify({
        project_id: $("pm-project").value,
        principal_id: $("pm-principal").value,
        role: $("pm-role").value,
      }),
    });
    showToast(t("项目成员角色已更新"));
    refreshAllData();
  });

  onSubmit("add-project-remote-form", async (e) => {
    e.preventDefault();
    await api("/api/admin/projects/remotes", {
      method: "POST",
      body: JSON.stringify({
        project_id: $("pr-project").value,
        remote_url: $("pr-remote").value,
        workspace_path: $("pr-workspace").value,
      }),
    });
    $("pr-remote").value = "";
    $("pr-workspace").value = "";
    showToast(t("仓库地址已添加"));
    refreshAllData();
  });

  onSubmit("create-token-form", async (e) => {
    e.preventDefault();
    const res = await api("/api/admin/tokens", {
      method: "POST",
      body: JSON.stringify({
        tenant_id: $("tok-tenant").value,
        principal_id: $("tok-principal").value,
        name: $("tok-name").value,
      }),
    });
    state.latestToken = res.token || "";
    if (res.id && res.token) {
      state.revealedTokens[res.id] = res.token;
    }
    if ($("guide-token-input")) {
      $("guide-token-input").value = state.latestToken;
    }
    renderClientGuides();
    $("new-token-box").classList.remove("hidden");
    $("new-token-content").textContent = formatMCPConfig(res.token);
    showToast(t("客户端密钥已生成，下一步复制配置。"));
    await refreshAllData();
    $("client-guides-card").open = true;
    $("client-guides-card").scrollIntoView({ behavior: "smooth", block: "start" });
  });

  onSubmit("filter-memories-form", async (e) => {
    e.preventDefault();
    state.memoryPage = 1;
    syncLocation(true);
    await loadMemories();
  });

  $("btn-close-detail").addEventListener("click", () => closeMemoryDetail());
  // Whole rows open the detail drawer; buttons inside a row keep their own action.
  $("memories-table").querySelector("tbody").addEventListener("click", (event) => {
    const row = event.target.closest("tr[data-memory-id]");
    if (!row) return;
    const mem = state.memories.find((item) => item.id === row.dataset.memoryId);
    if (event.target.closest(".btn-restore-mem")) { if (mem) memoryAction("restore", mem); return; }
    if (event.target.closest("[data-filter], .id-copy")) return;
    if (window.getSelection && String(window.getSelection()).trim()) return;
    viewMemoryDetail(row.dataset.memoryId);
  });
  document.addEventListener("click", (event) => {
    const chip = event.target.closest("[data-filter]");
    if (chip) { event.preventDefault(); applyMemoryFilter(chip.dataset.filter, chip.dataset.value); return; }
    if (event.target.closest("#btn-open-trash")) applyMemoryFilter("mem-filter-deleted", "deleted");
  });
  ["mem-filter-tenant", "mem-filter-scope", "mem-filter-project", "mem-filter-category", "mem-filter-tag", "mem-filter-deleted"].forEach((id) => $(id).addEventListener("change", () => {
    if (id === "mem-filter-tenant") renderMemoryProjectFilter();
    if (id === "mem-filter-project" && $(id).value) $("mem-filter-scope").value = "project";
    state.memoryPage = 1;
    syncLocation(true);
    loadMemories();
  }));
  $("btn-pin-memory").addEventListener("click", () => { const mem = state.selectedMemory; if (mem) memoryAction("pin", mem, { is_pinned: !mem.is_pinned }); });
  $("btn-restore-memory").addEventListener("click", () => { if (state.selectedMemory) memoryAction("restore", state.selectedMemory); });
  $("btn-delete-memory").addEventListener("click", async () => {
    const mem = state.selectedMemory;
    if (!mem) return;
    const confirmed = await confirmDialog({ title: t("删除这条知识？"), message: t`「${mem.title}」将移入回收站，AI 客户端将不再检索到它。之后可在回收站中恢复。`, confirmText: t("移入回收站") });
    if (confirmed) memoryAction("delete", mem);
  });
  $("btn-confirm-ok").addEventListener("click", () => state.confirmResolve && state.confirmResolve(true));
  $("btn-confirm-cancel").addEventListener("click", () => state.confirmResolve && state.confirmResolve(false));
  $("confirm-modal").addEventListener("click", (event) => { if (event.target === $("confirm-modal") && state.confirmResolve) state.confirmResolve(false); });
  $("memory-detail-card").addEventListener("click", (event) => { if (event.target === $("memory-detail-card")) closeMemoryDetail(); });
  $("btn-memory-prev").addEventListener("click", () => { state.memoryPage--; syncLocation(true); loadMemories(); });
  $("btn-memory-next").addEventListener("click", () => { state.memoryPage++; syncLocation(true); loadMemories(); });
  $("mem-page-size").addEventListener("change", () => { state.memoryPageSize = Number($("mem-page-size").value); state.memoryPage = 1; syncLocation(true); loadMemories(); });
  $("btn-clear-memory-filters").addEventListener("click", () => { $("filter-memories-form").reset(); renderMemoryProjectFilter(); state.memoryPage = 1; syncLocation(true); loadMemories(); });
  $("btn-detail-preview").addEventListener("click", () => setDetailMode(false));
  $("btn-detail-source").addEventListener("click", () => setDetailMode(true));
  $("btn-copy-memory").addEventListener("click", () => { if (state.selectedMemory) copyText(state.selectedMemory.content_md); });
  $("show-admin-passwords").addEventListener("change", (event) => { ["current-admin-password", "new-admin-password", "confirm-admin-password"].forEach((id) => { $(id).type = event.target.checked ? "text" : "password"; }); });
  $("guide-server-input").value = window.location.origin;
  $("guide-server-input").addEventListener("input", renderClientGuides);
  document.addEventListener("click", (event) => {
    const copy = event.target.closest("[data-copy-value]");
    if (copy) copyText(copy.dataset.copyValue);
    const code = event.target.closest(".btn-copy-code");
    if (code) copyText(code.closest(".markdown-code").querySelector("code").textContent);
  });

  // setDetailMode switches between the safe reading preview and the original Markdown source.
  function setDetailMode(source) {
    $("detail-content").classList.toggle("hidden", !source);
    $("detail-preview").classList.toggle("hidden", source);
    $("btn-detail-source").classList.toggle("active", source);
    $("btn-detail-preview").classList.toggle("active", !source);
  }

  $("btn-check-mcp").addEventListener("click", async () => {
    const output = $("mcp-check-result");
    const button = $("btn-check-mcp");
    const token = $("guide-token-input").value.trim();
    const report = (text, kind) => { output.textContent = text; output.className = "check-result " + (kind || ""); };
    if (!token) { report(t("请先选择或生成客户端密钥。"), "warn"); return; }
    renderClientGuides();
    if (!$("guide-server-input").checkValidity()) { $("guide-server-input").reportValidity(); return; }
    const origin = new URL($("guide-server-input").value.trim() || location.origin).origin;
    if (origin !== location.origin) { report(t("跨地址检查请在 AI 客户端完成；此页面只检查当前服务，避免把密钥发送到其他地址。"), "warn"); return; }
    button.disabled = true;
    report(t("正在检查 MCP 连接…"));
    try {
      const response = await fetch("/mcp", { method: "POST", headers: { "Content-Type": "application/json", Accept: "application/json, text/event-stream", Authorization: `Bearer ${token}` }, body: JSON.stringify({ jsonrpc: "2.0", id: 1, method: "initialize", params: { protocolVersion: "2025-03-26", capabilities: {}, clientInfo: { name: "engram-web-check", version: "1" } } }), signal: AbortSignal.timeout(10000) });
      if (!response.ok) throw new Error(t("连接失败，请检查密钥是否有效以及服务是否可用。"));
      const body = await response.text();
      if (!body.includes('"protocolVersion"') || body.includes('"error"')) throw new Error(t("服务未返回有效的 MCP 初始化结果。"));
      report(t("✓ 服务与密钥连接正常。接下来在 AI 客户端里重新连接 Engram，让它查询一次记忆即可。"), "ok");
    } catch (err) { report("✕ " + (err.name === "TimeoutError" ? t("连接超时，请稍后重试。") : err.message), "error"); }
    finally { button.disabled = false; }
  });

  // toggleGlossaryModal keeps account actions and keyboard focus consistent with the help dialog.
  function toggleGlossaryModal(open) {
    const modal = $("glossary-modal");
    if (!modal) return;
    if (open) {
      modal.classList.remove("hidden");
      document.querySelector(".account-menu").open = false;
      $("btn-close-glossary").focus();
    } else {
      modal.classList.add("hidden");
      document.querySelector(".account-menu summary").focus();
    }
  }

  $("btn-open-glossary").addEventListener("click", () => toggleGlossaryModal(true));
  $("btn-close-glossary").addEventListener("click", () => toggleGlossaryModal(false));
  $("glossary-modal").addEventListener("click", (e) => {
    if (e.target === $("glossary-modal")) {
      toggleGlossaryModal(false);
    }
  });
  document.addEventListener("keydown", (e) => {
    const confirmOpen = !$("confirm-modal").classList.contains("hidden");
    const purgeOpen = !$("purge-modal").classList.contains("hidden");
    const glossaryOpen = !$("glossary-modal").classList.contains("hidden");
    const detailOpen = !$("memory-detail-card").classList.contains("hidden");
    if (e.key === "Escape") {
      if (confirmOpen) state.confirmResolve && state.confirmResolve(false);
      else if (purgeOpen) closePurgeModal();
      else if (glossaryOpen) toggleGlossaryModal(false);
      else if (detailOpen) closeMemoryDetail();
    }
    if (e.key === "Tab" && (confirmOpen || purgeOpen || glossaryOpen || detailOpen)) {
      const dialog = $(confirmOpen ? "confirm-modal" : purgeOpen ? "purge-modal" : glossaryOpen ? "glossary-modal" : "memory-detail-card");
      const targets = Array.from(dialog.querySelectorAll('button:not([disabled]), a[href], input, select, textarea, summary, [tabindex="0"]')).filter((el) => el.getClientRects().length);
      const first = targets[0], last = targets[targets.length - 1];
      if (e.shiftKey && (document.activeElement === first || !dialog.contains(document.activeElement))) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && (document.activeElement === last || !dialog.contains(document.activeElement))) { e.preventDefault(); first.focus(); }
    }
  });

  async function saveBlobToLocal(blob, suggestedName) {
    if (typeof window.showSaveFilePicker === "function") {
      try {
        const handle = await window.showSaveFilePicker({
          suggestedName,
          types: [
            {
              description: "ZIP Archive",
              accept: { "application/zip": [".zip"] },
            },
          ],
        });
        const writable = await handle.createWritable();
        await writable.write(blob);
        await writable.close();
        return true;
      } catch (pickerErr) {
        if (pickerErr && pickerErr.name === "AbortError") {
          return false;
        }
        // Fall back to standard browser download if picker cannot open.
      }
    }

    const url = URL.createObjectURL(blob);
    const a = document.createElement("a");
    a.href = url;
    a.download = suggestedName;
    document.body.appendChild(a);
    a.click();
    document.body.removeChild(a);
    setTimeout(() => URL.revokeObjectURL(url), 1000);
    return true;
  }

  function parseDownloadFilename(resp, fallbackName) {
    const cd = resp.headers.get("Content-Disposition") || "";
    const m = /filename="?([^";]+)"?/i.exec(cd);
    return m && m[1] ? m[1] : fallbackName;
  }

  if ($("btn-download-export-zip")) {
    $("btn-download-export-zip").addEventListener("click", async () => {
      try {
        const resp = await fetch("/api/admin/export/archive", { credentials: "same-origin" });
        if (!resp.ok) {
          let msg = `HTTP ${resp.status}`;
          try {
            const errData = await resp.json();
            if (errData && errData.message) msg = errData.message;
          } catch (_) {}
          throw new Error(msg);
        }
        const blob = await resp.blob();
        const cnt = resp.headers.get("X-Exported-Count");
        const saved = await saveBlobToLocal(blob, "engram-markdown-export.zip");
        if (saved) {
          showToast(cnt ? t`Markdown 压缩包已导出（共 ${cnt} 条记录）` : t("Markdown 压缩包已导出"));
        }
      } catch (err) {
        showToast(err.message, true);
      }
    });
  }

  // Bulk purge: preview the scope, save a backup, then clear exactly what the backup contains.
  function purgeSelection() {
    const tenant = state.tenants.find((t) => t.id === $("purge-tenant").value);
    const scope = $("purge-scope").value;
    const project = scope === "project" ? state.projects.find((p) => p.id === $("purge-project").value) : null;
    return { tenant, scope, project, mode: $("purge-mode").value };
  }

  const PURGE_MODES = {
    soft: { deleted: "active", verb: t("清空"), effect: t("备份保存后才会清空。清空的条目进入回收站，之后仍可逐条恢复；"), button: t("下载备份并清空") },
    hard: { deleted: "all", verb: t("彻底删除"), effect: t("备份保存后才会删除。条目连同版本历史从数据库中移除，无法恢复，只能依靠这份备份；"), button: t("下载备份并彻底删除") },
    trash: { deleted: "deleted", verb: t("彻底删除"), effect: t("只处理回收站里的条目，正常条目不受影响。删除后无法恢复，只能依靠这份备份；"), button: t("下载备份并彻底删除") },
  };

  // refreshPurgePreview counts the active memories in scope and names the word the person must type.
  async function refreshPurgePreview() {
    const { tenant, scope, project, mode } = purgeSelection();
    const modeInfo = PURGE_MODES[mode];
    $("purge-step-effect").textContent = modeInfo.effect;
    $("btn-run-purge").textContent = modeInfo.button;
    $("purge-summary").classList.toggle("danger-summary", mode !== "soft");
    const request = (state.purgePreviewRequest = (state.purgePreviewRequest || 0) + 1);
    state.purgeCount = 0;
    $("btn-run-purge").disabled = true;
    $("purge-project-group").classList.toggle("hidden", scope !== "project");
    $("purge-confirm-word").textContent = project ? project.name : tenant ? tenant.name : "";
    if (!tenant || (scope === "project" && !project)) { $("purge-summary").textContent = scope === "project" ? t("这个租户下还没有项目。") : t("请先选择租户。"); return; }
    $("purge-summary").textContent = t("正在统计…");
    const params = new URLSearchParams({ page_size: 1, deleted: modeInfo.deleted, tenant_id: tenant.id });
    if (scope) params.set("scope", scope);
    if (project) params.set("project_id", project.id);
    try {
      const res = await api("/api/admin/memories?" + params.toString());
      if (request !== state.purgePreviewRequest) return;
      state.purgeCount = res.total;
      const target = (project ? t`项目「${escapeHtml(project.name)}」` : t`租户「${escapeHtml(tenant.name)}」${scope === "global" ? t("的全局记忆") : t("（全局记忆和所有项目）")}`) + (mode === "trash" ? t("回收站里") : mode === "hard" ? t("（含回收站）") : "");
      $("purge-summary").innerHTML = res.total ? t`将${modeInfo.verb}${target}的知识，共 <strong>${res.total}</strong> 条。` : t`${target}没有需要${modeInfo.verb}的知识。`;
      updatePurgeButton();
    } catch (err) {
      if (request === state.purgePreviewRequest) $("purge-summary").textContent = err.message;
    }
  }

  function updatePurgeButton() {
    $("btn-run-purge").disabled = !state.purgeCount || $("purge-confirm").value.trim() !== $("purge-confirm-word").textContent;
  }

  function renderPurgeProjects() {
    const tenant = $("purge-tenant").value;
    const prev = $("purge-project").value;
    const projects = state.projects.filter((p) => p.tenant_id === tenant);
    $("purge-project").innerHTML = projects.map((p) => `<option value="${p.id}">${escapeHtml(projectLabel(p))}</option>`).join("");
    if (projects.some((p) => p.id === prev)) $("purge-project").value = prev;
  }

  // openPurgeModal starts from the knowledge-base filters so the scope matches what is on screen.
  function openPurgeModal() {
    if (!state.tenants.length) { showToast(t("还没有租户，无需清空。"), true); return; }
    const project = state.projects.find((p) => p.id === $("mem-filter-project").value);
    const tenantID = project ? project.tenant_id : $("mem-filter-tenant").value || state.tenants[0].id;
    $("purge-tenant").innerHTML = state.tenants.map((t) => `<option value="${t.id}">${escapeHtml(tenantLabel(t))}</option>`).join("");
    $("purge-tenant").value = tenantID;
    renderPurgeProjects();
    if (project) { $("purge-scope").value = "project"; $("purge-project").value = project.id; }
    else $("purge-scope").value = $("mem-filter-scope").value === "global" ? "global" : "";
    $("purge-confirm").value = "";
    $("purge-mode").value = $("mem-filter-deleted").value === "deleted" ? "trash" : "soft";
    clearFormErrors($("purge-form"));
    state.purgeOpener = document.activeElement;
    $("purge-modal").classList.remove("hidden");
    $("purge-tenant").focus();
    refreshPurgePreview();
  }

  function closePurgeModal() {
    $("purge-modal").classList.add("hidden");
    if (state.purgeOpener && state.purgeOpener.isConnected) state.purgeOpener.focus({ preventScroll: true });
  }

  // backupFilename names the archive after its scope and time so several backups stay distinguishable.
  function backupFilename({ tenant, scope, project, memory, mode }) {
    const clean = (value) => String(value).replace(/[\\/:*?"<>|\s]+/g, "-").replace(/^-+|-+$/g, "") || "unnamed";
    const stamp = new Date().toLocaleString("sv-SE", { hour12: false }).replace(/[-: ]/g, "").replace(/^(\d{8})(\d{6}).*/, "$1-$2");
    const part = memory ? "memory-" + memory.id.slice(0, 8) : project ? clean(project.name) : scope === "global" ? "global" : "all";
    return `engram-backup-${clean(tenant.name)}-${part}${mode === "trash" ? "-trash" : ""}-${stamp}.zip`;
  }

  // runPurge saves the backup first and only then asks the server to remove what it contains.
  async function runPurge(selection) {
    const body = { tenant_id: selection.tenant.id, scope: selection.scope || "", project_id: selection.project ? selection.project.id : "", memory_id: selection.memory ? selection.memory.id : "", mode: selection.mode };
    const resp = await fetch("/api/admin/memories/purge/backup", { method: "POST", credentials: "same-origin", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
    if (!resp.ok) {
      let message = t`备份失败（${resp.status}），本次没有删除任何知识。`;
      try { const data = await resp.json(); message = ERROR_CODES[data.code] || translateError(data.message || "") || data.message || message; } catch (_) {}
      throw new Error(message);
    }
    const token = resp.headers.get("X-Purge-Token");
    const count = Number(resp.headers.get("X-Purge-Count") || 0);
    const filename = backupFilename(selection);
    const saved = await saveBlobToLocal(await resp.blob(), filename);
    if (!saved) throw new Error(t("已取消保存备份，本次没有删除任何知识。"));
    if (!count) return { purged: 0, filename };
    const res = await api("/api/admin/memories/purge", { method: "POST", body: JSON.stringify({ purge_token: token }) });
    return { purged: res.purged, filename };
  }

  $("btn-open-purge").addEventListener("click", openPurgeModal);
  $("btn-close-purge").addEventListener("click", closePurgeModal);
  $("btn-cancel-purge").addEventListener("click", closePurgeModal);
  $("purge-modal").addEventListener("click", (event) => { if (event.target === $("purge-modal")) closePurgeModal(); });
  $("purge-tenant").addEventListener("change", () => { renderPurgeProjects(); $("purge-confirm").value = ""; refreshPurgePreview(); });
  $("purge-scope").addEventListener("change", () => { $("purge-confirm").value = ""; refreshPurgePreview(); });
  $("purge-project").addEventListener("change", () => { $("purge-confirm").value = ""; refreshPurgePreview(); });
  $("purge-mode").addEventListener("change", refreshPurgePreview);
  $("purge-confirm").addEventListener("input", updatePurgeButton);

  onSubmit("purge-form", async (e) => {
    e.preventDefault();
    const selection = purgeSelection();
    const { purged, filename } = await runPurge(selection);
    closePurgeModal();
    if (!purged) { showToast(t("范围内没有需要删除的知识。")); return; }
    showToast(selection.mode === "soft"
      ? t`已清空 ${purged} 条知识，备份已保存为 ${filename}。需要时可在回收站恢复。`
      : t`已彻底删除 ${purged} 条知识，备份已保存为 ${filename}。`);
    await refreshAllData();
  });

  $("btn-hard-delete-memory").addEventListener("click", async () => {
    const mem = state.selectedMemory;
    if (!mem) return;
    const confirmed = await confirmDialog({ title: t("彻底删除这条知识？"), message: t`「${mem.title}」会先下载为 Markdown 备份，然后连同版本历史从数据库中移除，无法再恢复。`, confirmText: t("下载备份并彻底删除") });
    if (!confirmed) return;
    const tenant = state.tenants.find((t) => t.id === mem.tenant_id) || { id: mem.tenant_id, name: mem.tenant_id.slice(0, 8) };
    try {
      const { purged, filename } = await runPurge({ tenant, memory: mem, mode: "trash" });
      closeMemoryDetail();
      showToast(purged ? t`「${mem.title}」已彻底删除，备份已保存为 ${filename}。` : t("这条知识已不在回收站，未删除。"));
      await refreshAllData({ quiet: true });
    } catch (err) {
      showToast(err.message, true);
    }
  });

  if ($("migration-export-form")) {
    onSubmit("migration-export-form", async (e) => {
      e.preventDefault();
      const passphrase = $("mig-export-passphrase") ? $("mig-export-passphrase").value : "";
      const resp = await fetch("/api/admin/migration/export", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ passphrase }),
      });
      if (!resp.ok) {
        let msg = `HTTP ${resp.status}`;
        try {
          const errData = await resp.json();
          if (errData && errData.message) msg = errData.message;
        } catch (_) {}
        throw new Error(msg);
      }
      const blob = await resp.blob();
      const filename = parseDownloadFilename(resp, "engram-system-snapshot.zip");
      const saved = await saveBlobToLocal(blob, filename);
      if (saved) {
        const enc = resp.headers.get("X-Snapshot-Encrypted") === "true";
        const mems = resp.headers.get("X-Snapshot-Memories") || "0";
        showToast(t`系统迁移包已导出（${enc ? t("已加密") : t("未加密")}，含 ${mems} 条记忆）`);
      }
    });
  }

  if ($("migration-import-form")) {
    onSubmit("migration-import-form", async (e) => {
      e.preventDefault();
      const fileInput = $("mig-import-file");
      if (!fileInput || !fileInput.files || fileInput.files.length === 0) throw new FormError(t("请先选择要还原的系统迁移包（.zip）。"), "mig-import-file");
      const fd = new FormData();
      fd.append("file", fileInput.files[0]);
      fd.append("passphrase", $("mig-import-passphrase") ? $("mig-import-passphrase").value : "");
      fd.append("confirm_replace", $("mig-import-confirm") && $("mig-import-confirm").checked ? "true" : "false");

      const resp = await fetch("/api/admin/migration/import", {
        method: "POST",
        credentials: "same-origin",
        body: fd,
      });
      const text = await resp.text();
      let data = null;
      try {
        data = text ? JSON.parse(text) : null;
      } catch (_) {
        data = { message: text };
      }
      if (!resp.ok) {
        throw new Error((data && (data.message || data.error)) || `HTTP ${resp.status}`);
      }
      state.revealedTokens = {};
      if ($("mig-import-result") && $("mig-import-result-box")) {
        $("mig-import-result").classList.remove("hidden");
        $("mig-import-result-box").textContent = JSON.stringify(data, null, 2);
      }
      if ($("mig-import-passphrase")) $("mig-import-passphrase").value = "";
      showToast(t("系统快照还原成功！"));
      await refreshAllData();
    });
  }

  renderClientGuides();
  checkSession();
})();
