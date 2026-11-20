(function () {
  const { t, locale } = window.EngramI18n;
  window.EngramI18n.initialize();
  const state = {
    appVersion: "",
    tenants: [],
    principals: [],
    projects: [],
    tokens: [],
    memberTenant: "",
    selectedPrincipal: "",
    selectedProject: "",
    memories: [],
    facets: { categories: [], tags: [] },
    lastRefresh: 0,
    latestToken: "",
    clientConfigTokenID: "",
    clientConfigRequest: 0,
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
    toastTimer: null,
    migInspect: null,
    migExportTenantsInitialized: false,
    auditSubTab: "logs",
    auditLogs: [],
    auditPage: 1,
    auditPageSize: 20,
    auditTotal: 0,
    auditRequest: 0,
    auditStats: null,
    auditTaskDefs: [],
    auditTaskRuns: [],
    auditSelectedRun: null,
    schedulerTasks: [],
    schedulerRuns: [],
    schedulerRunsPage: 1,
    schedulerRunsPageSize: 20,
    schedulerRunsTotal: 0,
    safekeepSettings: null,
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
    [/^principal is not a member of the specified tenant/i, t("该成员还不属于所选租户，请先在「租户管理」页把成员绑定到这个租户。")],
    [/^principal must be a member of the project's tenant/i, t("该成员还不属于这个项目所在的租户，请先绑定租户。")],
    [/^project name is required/i, t("请填写项目名称。")],
    [/^tenant name is required/i, t("请填写租户名称。")],
    [/^display_name is required/i, t("请填写成员显示名称。")],
    [/^tenant not found/i, t("租户不存在，请刷新后重试。")],
    [/^principal not found/i, t("成员不存在，请刷新后重试。")],
    [/^cannot (modify|delete) system principal/i, t("系统内置账号不允许修改或删除。")],
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

  const TENANT_ROLES = { none: t("无权限"), member: t("只读"), global_writer: t("读写") };
  const PROJECT_ROLES = { none: t("无权限"), reader: t("只读"), writer: t("读写") };

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
    if (sessionState && sessionState.version) {
      state.appVersion = String(sessionState.version).trim();
    }
    const versionEl = $("app-version");
    if (versionEl) {
      versionEl.textContent = state.appVersion || "";
      versionEl.title = state.appVersion ? `${t("系统版本")}: ${state.appVersion}` : t("系统版本");
      versionEl.classList.toggle("hidden", !state.appVersion);
    }

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

    const username = sessionState.username || "admin";
    $("current-admin-user").textContent = username;
    if ($("sidebar-admin-user")) $("sidebar-admin-user").textContent = username;
    if ($("sidebar-user-avatar")) $("sidebar-user-avatar").textContent = username.slice(0, 1).toUpperCase();
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

      renderOverviewSetup();
      renderSelectors();
      renderManagementViews();
      if (!state.locationReady) {
        state.locationReady = true;
        await restoreLocation();
      } else {
        if (!(options.quiet && detailIsOpen())) {
          await loadMemories({ quiet: options.quiet });
        }
        if (activeTab() === "tab-audit") {
          await loadAuditCenter();
        } else if (activeTab() === "tab-scheduler") {
          await loadSchedulerCenter();
        } else if (activeTab() === "tab-settings") {
          await loadSafekeepSettings();
        }
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
    const tenantSelects = ["proj-tenant"];
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
    if ($("audit-filter-tenant")) fillSelect("audit-filter-tenant", state.tenants.map((t) => [t.id, tenantLabel(t)]));
    if ($("audit-task-tenant")) fillSelect("audit-task-tenant", state.tenants.map((t) => [t.id, tenantLabel(t)]));
    renderMigrationSelectors();
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

  // tenantMembership resolves a shared identity's role only within the selected tenant.
  function tenantMembership(principal, tenantID = state.memberTenant) {
    return principal && (principal.tenant_roles || []).find((role) => role.tenant_id === tenantID);
  }

  // selectedMember rejects identities that no longer belong to the current tenant.
  function selectedMember() {
    return visiblePrincipals().find((principal) => principal.id === state.selectedPrincipal && tenantMembership(principal));
  }

  // tenantRoleHint explains effective abilities without requiring knowledge of role names.
  function tenantRoleHint(role) {
    if (role === "none") return t("无法访问全局记忆；仍可单独授予项目权限。");
    if (role === "global_writer") return t("可读写当前租户的公共（全局）记忆；访问项目记忆仍需单独授权。");
    return t("可查看当前租户的公共（全局）记忆；访问项目记忆仍需单独授权。");
  }

  // renderTenantContext keeps member operations scoped to one tenant across refreshes.
  function renderTenantContext() {
    $("prin-tenant").value = state.memberTenant;
    const tenant = state.tenants.find((item) => item.id === state.memberTenant);
    const active = tenant && tenant.status === "active";
    $("btn-add-member").disabled = !active;
    $("tenant-detail-title").textContent = tenant ? tenant.name : "";
    $("tenant-status").textContent = tenant ? statusLabel(tenant.status) : t("尚无租户");
    $("tenant-status").className = "badge " + (active ? "success" : "danger");
    const members = visiblePrincipals().filter((principal) => tenantMembership(principal));
    const projects = state.projects.filter((project) => project.tenant_id === state.memberTenant && project.status === "active");
    const tokens = state.tokens.filter((token) => token.tenant_id === state.memberTenant && !token.revoked_at);
    $("tenant-summary").textContent = tenant ? t`${members.length} 位成员 · ${projects.length} 个有效项目 · ${tokens.length} 个未吊销密钥` : t("请先新建租户，再添加成员。");
    $("tenant-identity").innerHTML = tenant ? `${identifier(tenant.id)}<p class="field-hint">${t("创建时间")} · ${formatDate(tenant.created_at)}</p>` : "";
    $("member-form-context").textContent = tenant ? t`添加到租户「${tenant.name}」` : "";
    updatePrincipalMode();
  }

  const PROVIDERS = { api_token: t("客户端密钥"), oidc: t("单点登录"), mtls: t("双向 TLS 证书") };

  // renderPrincipalsTable presents identities with their explicit tenant memberships.
  function renderPrincipalsTable() {
    const tbody = $("principals-table").querySelector("tbody");
    const principals = visiblePrincipals().filter((principal) => tenantMembership(principal));
    if (!principals.length) { tbody.innerHTML = t('<tr><td colspan="7" class="empty-state">当前租户还没有成员，请点击“添加成员”。</td></tr>'); return; }
    tbody.innerHTML = principals
      .map((p) => {
        const membership = tenantMembership(p);
        const projects = state.projects.filter((project) => project.tenant_id === state.memberTenant && project.status === "active" && (project.members || []).some((member) => member.principal_id === p.id));
        const tokens = state.tokens.filter((token) => token.tenant_id === state.memberTenant && token.principal_id === p.id && !token.revoked_at);
        const projectAccess = projects.length ? t`已授权 ${projects.length} 个项目` : t("未授权任何项目");
        return `
      <tr class="${p.id === state.selectedPrincipal ? "member-selected" : ""}">
        <td><strong>${escapeHtml(p.display_name)}</strong></td>
        <td><span class="badge">${escapeHtml(TENANT_ROLES[membership.role] || membership.role)}</span></td>
        <td>${membership.can_create_project ? t("允许") : t("不允许")}</td>
        <td>${escapeHtml(projectAccess)}</td>
        <td>${tokens.length}</td>
        <td><span class="badge ${p.status === "active" ? "success" : "danger"}">${statusLabel(p.status)}</span></td>
        <td>
          <div class="row-actions">
            <button type="button" class="secondary small btn-manage-member" data-id="${p.id}" aria-controls="member-detail-card" aria-expanded="${p.id === state.selectedPrincipal}">${t("管理")}</button>
            <button type="button" class="danger-outline small btn-delete-member" data-id="${p.id}">${t("删除")}</button>
          </div>
        </td>
      </tr>`;
      })
      .join("");
  }

  // renderMemberDetail joins effective roles, project grants, and tenant-bound credentials.
  function renderMemberDetail() {
    const principal = selectedMember();
    $("member-detail-card").classList.toggle("hidden", !principal);
    if (!principal) { state.selectedPrincipal = ""; return; }
    const tenant = state.tenants.find((item) => item.id === state.memberTenant);
    const membership = tenantMembership(principal);
    const multiTenant = (principal.tenant_roles || []).length > 1;
    if ($("btn-remove-member-tenant")) $("btn-remove-member-tenant").classList.toggle("hidden", !multiTenant);
    const context = state.memberTenant + ":" + principal.id;
    const sameContext = $("member-role").dataset.context === context;
    if (!sameContext || $("member-role").dataset.saved !== membership.role) $("member-role").value = membership.role;
    $("member-role").dataset.context = context;
    $("member-role").dataset.saved = membership.role;
    if (!sameContext || $("member-create-project").dataset.saved !== String(!!membership.can_create_project)) $("member-create-project").checked = !!membership.can_create_project;
    $("member-create-project").dataset.saved = String(!!membership.can_create_project);
    const active = tenant.status === "active" && principal.status === "active";
    $("member-role").disabled = !active;
    $("member-create-project").disabled = !active;
    $("member-role-form").querySelector('button[type="submit"]').disabled = !active;
    $("member-token-name").disabled = !active;
    $("member-token-form").querySelector('button[type="submit"]').disabled = !active;
    if (!sameContext) { $("member-token-name").value = ""; clearFormErrors($("member-role-form")); clearFormErrors($("member-token-form")); }
    $("member-detail-context").textContent = t`当前租户：${tenant.name}`;
    $("member-detail-title").textContent = principal.display_name;
    $("member-detail-status").textContent = t`全局记忆：${TENANT_ROLES[membership.role] || membership.role} · 成员状态：${statusLabel(principal.status)}`;
    $("member-role-hint").textContent = tenantRoleHint($("member-role").value);
    $("member-identity").innerHTML = `${identifier(principal.id)}<p class="field-hint">${escapeHtml(PROVIDERS[principal.identity_provider] || principal.identity_provider)} · <code>${escapeHtml(principal.subject)}</code></p><p class="field-hint">${t("创建时间")} · ${formatDate(principal.created_at)}</p>`;
    const tbody = $("member-projects-table").querySelector("tbody");
    const drafts = sameContext ? new Map(Array.from(tbody.querySelectorAll("select[data-project]")).filter((select) => select.value !== select.dataset.saved).map((select) => [select.dataset.project, select.value])) : new Map();
    const projects = state.projects.filter((project) => project.tenant_id === state.memberTenant);
    tbody.innerHTML = projects.map((project) => {
      const member = (project.members || []).find((item) => item.principal_id === principal.id);
      const enabled = active && project.status === "active";
      const role = member ? member.role : "none";
      const label = project.status !== "active" ? t("项目已停用，无法访问") : PROJECT_ROLES[role] || t("无权限");
      const options = Object.entries(PROJECT_ROLES).map(([value, name]) => `<option value="${value}"${(drafts.get(project.id) || role) === value ? " selected" : ""}>${escapeHtml(name)}</option>`).join("");
      return `<tr><td><strong>${escapeHtml(project.name)}</strong></td><td>${escapeHtml(label)}</td><td><div class="row-actions"><select data-project="${project.id}" data-saved="${role}" aria-label="${escapeHtml(t`项目「${project.name}」的权限`)}"${enabled ? "" : " disabled"}>${options}</select><button type="button" class="secondary small btn-save-member-project" data-project="${project.id}"${enabled ? "" : " disabled"}>${t("保存授权")}</button></div></td></tr>`;
    }).join("") || t('<tr><td colspan="3" class="empty-state">当前租户还没有项目，可前往“项目”页注册。</td></tr>');
  }

  // renderManagementViews validates parent contexts before displaying any scoped management data.
  function renderManagementViews() {
    const tenant = state.tenants.find((item) => item.id === state.memberTenant);
    if (!tenant) { state.memberTenant = ""; state.selectedPrincipal = ""; }
    if (!selectedMember()) state.selectedPrincipal = "";
    const member = selectedMember();
    validateClientConfiguration();
    $("tenant-list-view").classList.toggle("hidden", !!tenant);
    $("tenant-detail-view").classList.toggle("hidden", !tenant || !!member);
    $("tenant-breadcrumb").classList.toggle("hidden", !tenant);
    $("btn-tenant-detail").textContent = tenant ? tenant.name : "";
    $("btn-tenant-detail").disabled = !member;
    $("btn-tenant-detail").setAttribute("aria-current", member ? "false" : "page");
    $("member-breadcrumb-name").textContent = member ? member.display_name : "";
    $("member-breadcrumb-separator").classList.toggle("hidden", !member);
    renderTenantsTable();
    renderTenantContext();
    renderPrincipalsTable();
    renderMemberDetail();
    renderProjectsTable();
    renderProjectDetail();
    renderTokensTable();
  }

  // renderOverviewSetup guides first-time users into the tenant management flow.
  function renderOverviewSetup() {
    const initialized = state.tenants.length > 0;
    $("overview-setup").classList.toggle("hidden", initialized);
    $("overview-title").textContent = initialized ? t("让知识在项目与客户端之间流动") : t("创建你的第一个租户");
    $("overview-description").textContent = initialized ? t("浏览最近沉淀的知识，管理项目和客户端接入。") : t("先创建租户，再添加成员并在成员详情中配置客户端。");
  }

  // renderTenantsTable makes the tenant list the starting point for member and key management.
  function renderTenantsTable() {
    const tbody = $("tenants-table").querySelector("tbody");
    tbody.innerHTML = state.tenants.map((tenant) => {
      const members = visiblePrincipals().filter((person) => tenantMembership(person, tenant.id));
      const projects = state.projects.filter((project) => project.tenant_id === tenant.id && project.status === "active");
      const tokens = state.tokens.filter((token) => token.tenant_id === tenant.id && !token.revoked_at);
      return `<tr><td><button type="button" class="text-button btn-open-tenant" data-id="${tenant.id}">${escapeHtml(tenant.name)}</button></td><td>${members.length}</td><td>${projects.length}</td><td>${tokens.length}</td><td><span class="badge ${tenant.status === "active" ? "success" : "danger"}">${statusLabel(tenant.status)}</span></td><td><div class="row-actions"><button type="button" class="secondary small btn-open-tenant" data-id="${tenant.id}">${t("管理")}</button><button type="button" class="danger-outline small btn-delete-tenant" data-id="${tenant.id}">${t("删除")}</button></div></td></tr>`;
    }).join("") || `<tr><td colspan="6" class="empty-state">${t("尚无租户，请先新建租户。")}</td></tr>`;
  }

  // openTenant changes the tenant/member route together so browser history retains each level.
  function openTenant(tenantID, principalID = "") {
    state.memberTenant = tenantID;
    state.selectedPrincipal = principalID;
    $("tenant-forms-panel").classList.add("hidden");
    $("create-tenant-card").classList.add("hidden");
    switchMainTab("tab-tenants", false);
    syncLocation(true);
    $(principalID && selectedMember() ? "member-detail-title" : state.memberTenant ? "tenant-detail-title" : "tenant-list-title").focus();
  }

  // projectPrincipals includes only members with an explicit project grant.
  function projectPrincipals(project) {
    return visiblePrincipals().filter((person) => {
      const membership = tenantMembership(person, project.tenant_id);
      return membership && (project.members || []).some((member) => member.principal_id === person.id);
    });
  }

  // renderProjectsTable leaves detailed repositories and permissions on the project detail page.
  function renderProjectsTable() {
    $("projects-table").querySelector("tbody").innerHTML = state.projects.map((project) => `<tr><td><button type="button" class="text-button btn-open-project" data-id="${project.id}">${escapeHtml(project.name)}</button></td><td>${escapeHtml(project.tenant_name)}</td><td>${(project.repositories || []).length}</td><td>${projectPrincipals(project).length}</td><td><span class="badge ${project.status === "active" ? "success" : "danger"}">${statusLabel(project.status)}</span></td><td><button type="button" class="secondary small btn-open-project" data-id="${project.id}">${t("管理")}</button></td></tr>`).join("") || `<tr><td colspan="6" class="empty-state">${t("尚无项目，请注册第一个项目。")}</td></tr>`;
  }

  // renderProjectDetail binds all repository and permission operations to the displayed project.
  function renderProjectDetail() {
    const project = state.projects.find((item) => item.id === state.selectedProject);
    if (!project) state.selectedProject = "";
    $("project-list-view").classList.toggle("hidden", !!project);
    $("project-detail-view").classList.toggle("hidden", !project);
    $("project-breadcrumb").classList.toggle("hidden", !project);
    if (!project) return;
    if ($("add-project-remote-form").dataset.project !== project.id) {
      $("add-project-remote-form").reset();
      clearFormErrors($("add-project-remote-form"));
      clearFormErrors($("add-project-member-form"));
      $("add-project-remote-form").dataset.project = project.id;
    }
    $("project-breadcrumb-name").textContent = project.name;
    $("project-detail-title").textContent = project.name;
    $("project-detail-context").textContent = t`所属租户：${project.tenant_name}`;
    $("project-detail-status").textContent = statusLabel(project.status);
    $("project-detail-status").className = "badge " + (project.status === "active" ? "success" : "danger");
    $("project-identity").innerHTML = `<p>${t("项目 ID")} · ${identifier(project.id)}</p><p>${t("标记文件 UUID")} · ${project.marker ? identifier(project.marker) : t("未生成")}</p><p class="field-hint">${t("创建时间")} · ${formatDate(project.created_at)}</p>`;
    $("project-remotes-table").querySelector("tbody").innerHTML = (project.repositories || []).map((remote) => `<tr><td><code>${escapeHtml(remote.canonical_remote)}</code></td><td>${escapeHtml(remote.workspace_path || t("仓库根目录"))}</td></tr>`).join("") || `<tr><td colspan="2" class="empty-state">${t("未关联仓库，通过标记文件识别")}</td></tr>`;
    $("project-members-table").querySelector("tbody").innerHTML = projectPrincipals(project).map((person) => {
      const grant = (project.members || []).find((item) => item.principal_id === person.id);
      return `<tr><td>${escapeHtml(person.display_name)}</td><td>${escapeHtml(PROJECT_ROLES[grant.role] || grant.role)}</td><td>${statusLabel(person.status)}</td></tr>`;
    }).join("") || `<tr><td colspan="3" class="empty-state">${t("尚无项目成员，请从当前租户选择成员授权。")}</td></tr>`;
    const select = $("pm-principal");
    const previous = select.dataset.project === project.id ? select.value : "";
    const previousRole = $("pm-role").value;
    const savedRole = $("pm-role").dataset.saved;
    select.dataset.project = project.id;
    const members = visiblePrincipals().filter((person) => person.status === "active" && tenantMembership(person, project.tenant_id));
    select.innerHTML = members.map((person) => t`<option value="${person.id}">${escapeHtml(person.display_name)}（${escapeHtml(person.subject)}）</option>`).join("");
    if (members.some((person) => person.id === previous)) select.value = previous;
    const tenant = state.tenants.find((item) => item.id === project.tenant_id);
    const enabled = project.status === "active" && tenant && tenant.status === "active";
    $("add-project-remote-form").querySelectorAll("input, button").forEach((element) => { element.disabled = !enabled; });
    select.disabled = !enabled || !members.length;
    renderProjectRole();
    if (previous === select.value && savedRole === $("pm-role").dataset.saved && !$("pm-role").disabled) $("pm-role").value = previousRole;
  }

  // renderProjectRole shows explicit grants and supports revoking project access.
  function renderProjectRole() {
    const project = state.projects.find((item) => item.id === state.selectedProject);
    const person = visiblePrincipals().find((item) => item.id === $("pm-principal").value);
    const membership = project && person && tenantMembership(person, project.tenant_id);
    const grant = project && (project.members || []).find((item) => item.principal_id === $("pm-principal").value);
    $("pm-role").value = grant ? grant.role : "none";
    $("pm-role").dataset.saved = $("pm-role").value;
    const disabled = $("pm-principal").disabled || !membership;
    $("pm-role").disabled = disabled;
    $("add-project-member-form").querySelector('button[type="submit"]').disabled = disabled;
  }

  // openProject resets form drafts when moving between project contexts.
  function openProject(projectID) {
    if (state.selectedProject !== projectID) {
      $("add-project-remote-form").reset();
      clearFormErrors($("add-project-remote-form"));
      clearFormErrors($("add-project-member-form"));
    }
    state.selectedProject = projectID;
    $("project-forms-panel").classList.add("hidden");
    switchMainTab("tab-projects", false);
    syncLocation(true);
    $(state.selectedProject ? "project-detail-title" : "project-list-title").focus();
  }

  // resolveTokenSecret reads an existing key and prevents late responses from crossing logout.
  async function resolveTokenSecret(tokenId) {
    const cache = state.revealedTokens;
    if (cache[tokenId]) {
      return cache[tokenId];
    }
    const res = await api("/api/admin/tokens/reveal", {
      method: "POST",
      body: JSON.stringify({ id: tokenId }),
    });
    if (cache !== state.revealedTokens) throw new Error(t("密钥读取已取消，请重新选择客户端。"));
    if (res && res.token) {
      cache[tokenId] = res.token;
      return res.token;
    }
    throw new Error(t("未能获取密钥明文。"));
  }

  // renderTokensTable lists client credentials and exposes their configuration actions.
  function renderTokensTable() {
    renderTokenRows("member-tokens-table", state.tokens.filter((token) => token.tenant_id === state.memberTenant && token.principal_id === state.selectedPrincipal));
  }

  // renderTokenRows shares credential actions between the client list and member detail.
  function renderTokenRows(tableID, tokens) {
    const tbody = $(tableID).querySelector("tbody");
    if (!tokens.length) { tbody.innerHTML = `<tr><td colspan="6" class="empty-state">${t("尚无客户端密钥，可签发新密钥连接客户端。")}</td></tr>`; return; }
    tbody.innerHTML = tokens
      .map((tk) => {
        const active = !tk.revoked_at;
        const revealed = state.revealedTokens[tk.id];
        const tokenCell = revealed
          ? `<code>${escapeHtml(revealed)}</code>`
          : `<code>${escapeHtml(tk.token_prefix)}…</code>${
              tk.can_reveal ? "" : t(' <small class="muted" title="无法还原此密钥明文；可在配置客户端时手动粘贴原密钥。">（无法查看明文）</small>')
            }`;
        const revealBtns = tk.can_reveal
          ? t`
            <button type="button" class="secondary small btn-toggle-token" data-id="${tk.id}">${revealed ? t("隐藏") : t("查看")}</button>
            <button type="button" class="secondary small btn-copy-token" data-id="${tk.id}">复制</button>
          `
          : "";
        const lastUsed = tk.last_used_at
          ? `<time title="${escapeHtml(formatDate(tk.last_used_at))}">${relativeTime(tk.last_used_at)}</time>`
          : t('<span class="muted">从未使用</span>');
        return `
      <tr class="${active ? "" : "row-muted"}">
        <td><strong>${escapeHtml(tk.name)}</strong></td>
        <td>${tokenCell}</td>
        <td><span class="badge nowrap ${active ? "success" : "danger"}">${active ? t("正常") : t("已吊销")}</span></td>
        <td class="date-cell">${lastUsed}</td>
        <td class="date-cell">${formatDate(tk.created_at)}</td>
        <td>
          <div class="row-actions">
            ${active ? revealBtns : ""}
            ${active ? `<button type="button" class="secondary small btn-apply-token" data-id="${escapeHtml(tk.id)}">${t("配置客户端")}</button>` : ""}
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
          const token = state.tokens.find((item) => item.id === btn.dataset.id && !item.revoked_at);
          if (!token) throw new Error(t("该客户端密钥已失效或已吊销，请选择其他客户端或粘贴有效密钥。"));
          btn.focus();
          await openClientGuides(token);
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
    updateMemoryFilterToggle();
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

  // agentName labels missing self-reported attribution without inferring it from member identity.
  function agentName(value) { return typeof value === "string" && value.trim() ? value : t("未知 Agent"); }

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
        `<span class="meta-text">${t`创建 Agent：${escapeHtml(agentName(mem.created_agent))}`}</span>`,
        `<span class="meta-text">${t`最后修改 Agent：${escapeHtml(agentName(mem.updated_agent))}`}</span>`,
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
        return t`<details class="revision-item"><summary><strong>v${revision.version} · ${escapeHtml(operation)}</strong><span>${formatDate(revision.created_at)} · ${escapeHtml(actorName(revision.actor_id))} · ${escapeHtml(agentName(snap.updated_agent))}</span></summary>${meta ? `<pre class="revision-meta">${escapeHtml(meta)}</pre>` : ""}${diff}<details><summary>原始快照</summary><pre class="code-box">${escapeHtml(JSON.stringify(revision.snapshot, null, 2))}</pre></details></details>`;
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

  // getMemoryInstructionSnippet guides discovery, autonomous registration, and scoped memory use.
  function getMemoryInstructionSnippet() {
    return [
      t("## Engram 共享记忆使用规范"),
      "",
      t("如果当前会话已接入 `engram` MCP 工具，请按以下流程使用："),
      "",
      t("1. **会话开始先识别项目**："),
      t("   - 获取当前仓库的 `git remote -v`（以及根目录或子目录 `.engram-project` 文件中的 UUID，如有），调用 `memory_discover` 获取 `project_context` 与范围索引。"),
      t("   - 注意：`workspace_path` 仅用于 Monorepo 子目录（相对于 Git 根目录），**不要传入本地绝对路径**；若当前不在 Git 仓库中且仅需全局记忆，传入 `allow_global_only: true`。"),
      t("   - 当 `memory_discover` 返回 `PROJECT_NOT_REGISTERED` 时，若当前成员有创建项目权限，可根据当前仓库信息调用 `project_register`（提供项目名称、仓库地址、相对 `workspace_path` 及唯一 `idempotency_key`）自主注册，无需再次询问用户；已有 `.engram-project` UUID 时同时传入 `project_marker`。注册成功后重新调用 `memory_discover`，再读写项目记忆。无创建权限时提示用户联系管理员；已有项目无访问权限时不得通过注册为自己增加权限。"),
      t("   - 仅以 Marker 注册（不提供仓库地址）时，省略 `workspace_path`；后续发现时通过 `marker_path` 报告 `.engram-project` 所在目录（相对于 Git 根目录）。"),
      t("   - 同一会话内复用返回的 `project_context`，无需重复调用 `memory_discover`。"),
      t("2. **先查索引，再按需检索与读取**："),
      t("   - 先浏览 `memory_discover` 返回的 `pinned`、`category_stats` 和 `recent`，若已命中目标可直接调用 `memory_read`。"),
      t("   - 需要按关键词、分类或标签检索时调用 `memory_query`，定位到 `memory_id` 后再调用 `memory_read` 读取完整 Markdown 正文。"),
      t("3. **任务过程中及时沉淀与更新**："),
      t("   - 调用 `memory_create`、`memory_update`、`memory_delete` 或 `memory_promote` 写入时，在顶层参数 `agent_name` 中填写当前 Agent 名称，例如 `Codex`、`Claude Code` 或自定义 `review-agent`。这是自报信息，权限仍由 Token 决定；使用同一 `idempotency_key` 重试时保持 `agent_name` 一致。"),
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

  // currentMemberToken rejects credentials outside the visible tenant and member detail.
  function currentMemberToken(tokenID) {
    if (activeTab() !== "tab-tenants" || !selectedMember() || $("app-view").classList.contains("hidden") || $("member-detail-card").classList.contains("hidden")) return null;
    return state.tokens.find((token) => token.id === tokenID && !token.revoked_at && token.tenant_id === state.memberTenant && token.principal_id === state.selectedPrincipal) || null;
  }

  // clientTokenContext names the fixed member credential without exposing its secret.
  function clientTokenContext(token) {
    return `${token.tenant_name} / ${token.principal_name} / ${token.name} · ${token.token_prefix ? token.token_prefix + "…" : token.id.slice(0, 8)}`;
  }

  // validateClientConfiguration closes an invalidated or out-of-context credential configuration.
  function validateClientConfiguration() {
    if (!state.clientConfigTokenID || currentMemberToken(state.clientConfigTokenID)) return;
    const wasOpen = !$("client-config-modal").classList.contains("hidden");
    state.clientConfigTokenID = "";
    $("guide-token-input").value = "";
    $("guide-token-hint").textContent = "";
    $("mcp-check-result").textContent = "";
    $("client-config-context").textContent = "";
    closeClientGuides(false);
    renderClientGuides();
    if (wasOpen) showToast(t("当前成员或密钥已变化，请从成员详情重新打开配置。"), true);
  }

  // loadClientTokenSecret reads only the fixed member credential and ignores superseded responses.
  async function loadClientTokenSecret(tokenID) {
    const request = state.clientConfigRequest = (state.clientConfigRequest || 0) + 1;
    const token = currentMemberToken(tokenID);
    $("guide-token-input").value = "";
    $("mcp-check-result").textContent = "";
    $("guide-token-hint").textContent = t("正在获取已有密钥…");
    renderClientGuides();
    if (!token) { validateClientConfiguration(); return; }
    try {
      if (!token.can_reveal && !state.revealedTokens[token.id]) throw new Error("token plaintext unavailable");
      const secret = await resolveTokenSecret(token.id);
      if (request !== state.clientConfigRequest) return;
      if (!currentMemberToken(token.id)) { validateClientConfiguration(); return; }
      $("guide-token-input").value = secret;
      $("guide-token-hint").textContent = "";
      renderClientGuides();
    } catch (_) {
      if (request !== state.clientConfigRequest) return;
      if (!currentMemberToken(token.id)) { validateClientConfiguration(); return; }
      $("guide-token-input").value = "";
      $("guide-token-hint").textContent = t("无法还原此密钥明文。请粘贴原密钥查看配置；此操作不会签发新密钥。");
      renderClientGuides();
    }
  }

  // openClientGuides opens configuration only for a key belonging to the current member detail.
  function openClientGuides(token) {
    const current = token && currentMemberToken(token.id);
    if (!current) {
      validateClientConfiguration();
      showToast(t("请从当前租户的成员密钥列表打开客户端配置。"), true);
      return;
    }
    state.clientConfigOpener = document.activeElement;
    state.clientConfigTokenID = current.id;
    $("client-config-context").textContent = clientTokenContext(current);
    $("mcp-check-result").textContent = "";
    $("client-guides-card").open = true;
    $("client-config-modal").classList.remove("hidden");
    $("client-config-modal").querySelector(".modal-card").scrollTop = 0;
    document.body.classList.add("client-config-open");
    $("btn-close-client-config").focus();
    return loadClientTokenSecret(current.id);
  }

  // closeClientGuides cancels pending reveals and restores focus to its invoking control.
  function closeClientGuides(restoreFocus = true) {
    state.clientConfigRequest = (state.clientConfigRequest || 0) + 1;
    const modal = $("client-config-modal");
    const wasOpen = !modal.classList.contains("hidden");
    modal.classList.add("hidden");
    document.body.classList.remove("client-config-open");
    if (wasOpen && restoreFocus) {
      // A background refresh may replace the original row while configuration is open.
      const opener = state.clientConfigOpener && state.clientConfigOpener.isConnected ? state.clientConfigOpener : currentMemberToken(state.clientConfigTokenID) && $("member-tokens-table").querySelector(`.btn-apply-token[data-id="${state.clientConfigTokenID}"]`);
      if (opener) opener.focus({ preventScroll: true });
    }
  }

  // activeTab returns the current navigation destination for URL persistence.
  function activeTab() { return document.querySelector(".tab-btn.active").dataset.tab; }

  // updateMemoryFilterToggle keeps collapsed filters visible through their active count.
  function updateMemoryFilterToggle() {
    const count = ["tenant", "scope", "project", "category", "tag", "deleted"].filter((key) => {
      const value = $("mem-filter-" + key).value;
      return value && !(key === "deleted" && value === "active");
    }).length;
    $("memory-filter-count").textContent = count;
    $("memory-filter-count").classList.toggle("hidden", count === 0);
  }

  function setSidebarOpen(open) {
    const sidebar = $("app-navigation");
    const backdrop = $("sidebar-backdrop");
    const btn = $("btn-mobile-menu");
    if (sidebar) sidebar.classList.toggle("open", !!open);
    if (backdrop) backdrop.classList.toggle("hidden", !open);
    if (btn) btn.setAttribute("aria-expanded", String(!!open));
  }

  // switchMainTab maintains navigation state without carrying an unrelated detail panel.
  function switchMainTab(tabId, updateURL = true) {
    if (tabId === "tab-tokens") tabId = "tab-tenants";
    const activeBtn = document.querySelector(`.tab-btn[data-tab="${tabId}"]`);
    if (!activeBtn) tabId = "tab-overview";
    if (updateURL && tabId === "tab-tenants") { state.memberTenant = ""; state.selectedPrincipal = ""; }
    if (updateURL && tabId === "tab-projects") state.selectedProject = "";
    setSidebarOpen(false);
    closeClientGuides(false);
    closeMemoryDetail(false, false);
    document.querySelectorAll(".tab-btn").forEach((b) => {
      b.classList.toggle("active", b.dataset.tab === tabId);
      b.setAttribute("aria-current", b.dataset.tab === tabId ? "page" : "false");
    });
    const currentBtn = document.querySelector(`.tab-btn[data-tab="${tabId}"]`);
    if ($("topbar-current-page") && currentBtn) {
      const labelEl = currentBtn.querySelector("span:not(.nav-chevron)");
      $("topbar-current-page").textContent = (labelEl ? labelEl.textContent : currentBtn.textContent).trim();
    }
    document.querySelectorAll(".tab-pane").forEach((p) => p.classList.toggle("hidden", p.id !== tabId));
    renderManagementViews();
    if (tabId === "tab-audit" && state.locationReady) loadAuditCenter().catch((err) => showToast(err.message, true));
    if (tabId === "tab-scheduler" && state.locationReady) loadSchedulerCenter().catch((err) => showToast(err.message, true));
    if (tabId === "tab-settings" && state.locationReady) loadSafekeepSettings().catch((err) => showToast(err.message, true));
    if (updateURL) syncLocation(true);
  }

  const TAB_PATHS = {
    "tab-overview": "/overview",
    "tab-memories": "/memories",
    "tab-projects": "/projects",
    "tab-tenants": "/tenants",
    "tab-audit": "/audit",
    "tab-scheduler": "/scheduler",
    "tab-migration": "/migration",
    "tab-settings": "/settings",
  };

  // syncLocation stores navigation in URL paths and keeps only active list filters in query params.
  function syncLocation(push = false) {
    if (push) state.locationRequest++;
    const tab = activeTab();
    let path = TAB_PATHS[tab] || "/overview";
    if (tab === "tab-memories" && state.selectedMemory) {
      path = `/memories/${encodeURIComponent(state.selectedMemory.id)}`;
    } else if (tab === "tab-projects" && state.selectedProject) {
      path = `/projects/${encodeURIComponent(state.selectedProject)}`;
    } else if (tab === "tab-tenants" && state.memberTenant) {
      path = state.selectedPrincipal
        ? `/tenants/${encodeURIComponent(state.memberTenant)}/members/${encodeURIComponent(state.selectedPrincipal)}`
        : `/tenants/${encodeURIComponent(state.memberTenant)}`;
    }

    const params = new URLSearchParams();
    const currentLang = new URLSearchParams(location.search).get("lang");
    if (currentLang) params.set("lang", currentLang);
    if (tab === "tab-memories") {
      MEMORY_FILTERS.forEach(([key, id]) => {
        const value = $(id).value;
        if (value && !(key === "deleted" && value === "active")) params.set(key, value);
      });
      if (state.memoryPage > 1) params.set("page", state.memoryPage);
      if (state.memoryPageSize !== 20) params.set("size", state.memoryPageSize);
    }

    const query = params.toString();
    const nextURL = path + (query ? "?" + query : "");
    const currentURL = location.pathname + location.search;
    if (currentURL !== nextURL || location.hash) history[push ? "pushState" : "replaceState"](null, "", nextURL);
  }

  // parseRouteFromLocation resolves clean SPA paths and falls back to legacy hash links when present.
  function parseRouteFromLocation() {
    if (location.pathname === "/" && location.hash.startsWith("#tab=")) {
      const legacy = new URLSearchParams(location.hash.slice(1));
      const legacyTab = legacy.get("tab") === "tab-tokens" ? "tab-tenants" : legacy.get("tab");
      return {
        tab: legacyTab,
        tenantID: legacyTab === "tab-tenants" ? legacy.get("tenant") || "" : "",
        memberID: legacyTab === "tab-tenants" ? legacy.get("member") || "" : "",
        projectID: legacyTab === "tab-projects" ? legacy.get("project") || "" : "",
        memoryID: legacyTab === "tab-memories" ? legacy.get("memory") || "" : "",
        queryParams: legacy,
      };
    }

    const parts = location.pathname.replace(/^\/+|\/+$/g, "").split("/").filter(Boolean).map((part) => {
      try { return decodeURIComponent(part); } catch (_) { return part; }
    });
    const searchParams = new URLSearchParams(location.search);
    const out = { tab: "tab-overview", tenantID: "", memberID: "", projectID: "", memoryID: "", queryParams: searchParams };
    if (!parts.length || parts[0] === "overview") {
      out.tab = "tab-overview";
    } else if (parts[0] === "memories") {
      out.tab = "tab-memories";
      if (parts[1]) out.memoryID = parts[1];
    } else if (parts[0] === "projects") {
      out.tab = "tab-projects";
      if (parts[1]) out.projectID = parts[1];
    } else if (parts[0] === "tenants") {
      out.tab = "tab-tenants";
      if (parts[1]) out.tenantID = parts[1];
      if (parts[2] === "members" && parts[3]) out.memberID = parts[3];
    } else if (parts[0] === "audit") {
      out.tab = "tab-audit";
    } else if (parts[0] === "scheduler") {
      out.tab = "tab-scheduler";
    } else if (parts[0] === "migration") {
      out.tab = "tab-migration";
    } else if (parts[0] === "settings") {
      out.tab = "tab-settings";
    }
    return out;
  }

  // restoreLocation restores filters after selectors are populated and supports off-page detail links.
  async function restoreLocation() {
    const request = ++state.locationRequest;
    const route = parseRouteFromLocation();
    const tab = route.tab;
    const params = route.queryParams;
    const allowed = Array.from(document.querySelectorAll(".tab-btn")).map((button) => button.dataset.tab);
    state.memberTenant = route.tenantID;
    state.selectedPrincipal = route.memberID;
    const projectID = route.projectID;
    if (state.selectedProject !== projectID) $("add-project-remote-form").reset();
    state.selectedProject = projectID;
    $("tenant-forms-panel").classList.add("hidden");
    $("create-tenant-card").classList.add("hidden");
    $("project-forms-panel").classList.add("hidden");
    switchMainTab(allowed.includes(tab) ? tab : "tab-overview", false);
    const filterParams = tab === "tab-memories" ? params : new URLSearchParams();
    MEMORY_FILTERS.forEach(([key, id]) => {
      const el = $(id); const fallback = key === "deleted" ? "active" : ""; const value = filterParams.get(key) || fallback;
      if (el.tagName === "SELECT" && value && !Array.from(el.options).some((option) => option.value === value)) {
        // Deep links may name a category, tag or project that has no active entries yet.
        if (["category", "tag", "project"].includes(key)) el.insertAdjacentHTML("beforeend", `<option value="${escapeHtml(value)}">${escapeHtml(value)}</option>`);
        else { el.value = fallback; return; }
      }
      el.value = value;
    });
    const page = Number(filterParams.get("page"));
    state.memoryPage = Number.isInteger(page) && page > 0 && page <= 1000000 ? page : 1;
    const size = Number(filterParams.get("size"));
    state.memoryPageSize = [20, 50, 100].includes(size) ? size : 20;
    $("mem-page-size").value = state.memoryPageSize;
    await loadMemories();
    if (request !== state.locationRequest) return;
    const memoryID = route.memoryID;
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

  function closeRenameModal() {
    const modal = $("rename-modal");
    if (!modal || modal.classList.contains("hidden")) return;
    modal.classList.add("hidden");
    const opener = state.renameOpener;
    state.renameTask = null;
    state.renameOpener = null;
    if (opener && opener.isConnected) opener.focus({ preventScroll: true });
  }

  function openRenameModal({ title, desc, label, initialValue, onSubmitValue }) {
    const modal = $("rename-modal");
    clearFormErrors($("rename-form"));
    $("rename-title").textContent = title;
    $("rename-desc").textContent = desc || "";
    $("rename-desc").classList.toggle("hidden", !desc);
    $("rename-label").textContent = label;
    $("rename-input").value = initialValue || "";
    state.renameOpener = document.activeElement;
    state.renameTask = onSubmitValue;
    modal.classList.remove("hidden");
    $("rename-input").focus();
    $("rename-input").select();
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

  // updatePrincipalMode exposes creation and adding an existing identity as distinct actions.
  function updatePrincipalMode() {
    const mode = $("prin-mode").value;
    const creating = mode === "create";
    ["provider", "subject", "name"].forEach((field) => $("prin-" + field + "-group").classList.toggle("hidden", !creating));
    $("prin-subject").required = creating;
    $("prin-name").required = creating;
    $("prin-provider").disabled = !creating;
    $("prin-existing-group").classList.toggle("hidden", creating);
    $("prin-existing").required = !creating;
    const select = $("prin-existing");
    const previous = select.value;
    const available = visiblePrincipals().filter((principal) => principal.status === "active" && !tenantMembership(principal));
    select.innerHTML = available.map((principal) => `<option value="${principal.id}">${escapeHtml(principal.display_name)}（${escapeHtml(principal.subject)}）</option>`).join("");
    if (available.some((principal) => principal.id === previous)) select.value = previous;
    select.disabled = creating || !available.length;
    $("prin-existing-hint").textContent = available.length ? t("加入当前租户不会修改该成员在其他租户的角色和权限。") : t("没有可加入的已有成员，请选择“创建新成员”。");
    const tenant = state.tenants.find((item) => item.id === state.memberTenant);
    $("btn-save-principal").disabled = !tenant || tenant.status !== "active" || (!creating && !available.length);
    $("btn-save-principal").textContent = creating ? t("创建新成员") : t("加入当前租户");
    $("prin-role-hint").textContent = tenantRoleHint($("prin-role").value);
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
    wrapper.setAttribute("aria-label", (table.closest(".member-section, .card, .glossary-section")?.querySelector("h2, h3")?.textContent || table.closest(".tab-pane")?.querySelector("h1")?.textContent || t("数据表格")) + t("，可横向滚动"));
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
  $("btn-refresh").addEventListener("click", async () => {
    const button = $("btn-refresh");
    button.disabled = true;
    button.setAttribute("aria-busy", "true");
    try { await refreshAllData({ quiet: true }); }
    finally { button.disabled = false; button.removeAttribute("aria-busy"); }
  });
  // Account actions close when another part of the page is clicked.
  document.addEventListener("click", (event) => {
    const menu = document.querySelector(".account-menu");
    if (!menu.contains(event.target)) menu.open = false;
  });
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
    $("guide-token-input").addEventListener("input", () => {
      // Manual input stays tied to the member key and supersedes any pending reveal.
      state.clientConfigRequest = (state.clientConfigRequest || 0) + 1;
      validateClientConfiguration();
      $("guide-token-hint").textContent = "";
      $("mcp-check-result").textContent = "";
      renderClientGuides();
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
    state.clientConfigRequest = (state.clientConfigRequest || 0) + 1;
    await api("/api/admin/logout", { method: "POST" });
    state.latestToken = "";
    state.clientConfigTokenID = "";
    state.clientConfigRequest = (state.clientConfigRequest || 0) + 1;
    $("guide-token-hint").textContent = "";
    state.revealedTokens = {};
    if ($("guide-token-input")) {
      $("guide-token-input").value = "";
    }
    renderClientGuides();
    closeMemoryDetail(false, false);
    closeClientGuides(false);
    $("client-guides-card").open = false;
    document.querySelector(".account-menu").open = false;
    renderView({ authenticated: false });
  });

  onSubmit("create-tenant-form", async (e) => {
    e.preventDefault();
    const res = await api("/api/admin/tenants", {
      method: "POST",
      body: JSON.stringify({ name: $("tenant-name").value }),
    });
    state.memberTenant = res.id;
    state.selectedPrincipal = "";
    $("tenant-name").value = "";
    $("create-tenant-card").classList.add("hidden");
    $("tenant-forms-panel").classList.add("hidden");
    showToast(t("租户已创建"));
    await refreshAllData();
    openTenant(res.id);
  });

  function promptRenameTenant(tenantID) {
    const tenant = state.tenants.find((item) => item.id === tenantID);
    if (!tenant) return;
    openRenameModal({
      title: t("重命名租户"),
      desc: t`当前租户名称：${tenant.name}`,
      label: t("新租户名称"),
      initialValue: tenant.name,
      onSubmitValue: async (newName) => {
        await api("/api/admin/tenants", {
          method: "PATCH",
          body: JSON.stringify({ id: tenant.id, name: newName }),
        });
        showToast(t("租户已重命名"));
        await refreshAllData();
      },
    });
  }

  async function confirmDeleteTenant(tenantID) {
    const tenant = state.tenants.find((item) => item.id === tenantID);
    if (!tenant) return;
    const confirmed = await confirmDialog({
      title: t("删除租户？"),
      message: t`将永久删除租户「${tenant.name}」及其全部项目、仓库关联、客户端密钥和知识库（共已关联成员不会被删除）。此操作无法撤销。`,
      confirmText: t("确认删除"),
    });
    if (!confirmed) return;
    try {
      await api("/api/admin/tenants", {
        method: "DELETE",
        body: JSON.stringify({ id: tenant.id }),
      });
      if (state.memberTenant === tenant.id) {
        state.memberTenant = "";
        state.selectedPrincipal = "";
      }
      showToast(t`租户「${tenant.name}」已删除`);
      await refreshAllData();
      syncLocation(true);
    } catch (err) {
      showToast(err.message, true);
    }
  }

  function promptRenameMember(principalID) {
    const principal = visiblePrincipals().find((item) => item.id === principalID);
    if (!principal) return;
    openRenameModal({
      title: t("修改成员名称"),
      desc: t`成员标识：${principal.subject}`,
      label: t("新显示名称"),
      initialValue: principal.display_name,
      onSubmitValue: async (newName) => {
        await api("/api/admin/principals", {
          method: "PATCH",
          body: JSON.stringify({ id: principal.id, display_name: newName }),
        });
        showToast(t("成员名称已更新"));
        await refreshAllData();
      },
    });
  }

  async function confirmRemoveMemberFromTenant(principalID) {
    const principal = visiblePrincipals().find((item) => item.id === principalID);
    const tenant = state.tenants.find((item) => item.id === state.memberTenant);
    if (!principal || !tenant) return;
    const confirmed = await confirmDialog({
      title: t("从当前租户移除成员？"),
      message: t`将「${principal.display_name}」移出租户「${tenant.name}」，并清除该成员在此租户下的项目权限与客户端密钥；成员账号及其他租户的权限保留不变。`,
      confirmText: t("确认移除"),
    });
    if (!confirmed) return;
    try {
      await api("/api/admin/principals", {
        method: "DELETE",
        body: JSON.stringify({ id: principal.id, tenant_id: tenant.id, scope: "tenant" }),
      });
      if (state.selectedPrincipal === principal.id) state.selectedPrincipal = "";
      showToast(t`已将「${principal.display_name}」移出当前租户`);
      await refreshAllData();
      syncLocation(true);
    } catch (err) {
      showToast(err.message, true);
    }
  }

  async function confirmDeleteMember(principalID) {
    const principal = visiblePrincipals().find((item) => item.id === principalID);
    if (!principal) return;
    const confirmed = await confirmDialog({
      title: t("彻底删除成员？"),
      message: t`将删除成员「${principal.display_name}」（${principal.subject}）及其在所有租户的权限和客户端密钥；该成员历史创建的知识与版本记录会保留并归属为「管理控制台」。此操作无法撤销。`,
      confirmText: t("确认删除"),
    });
    if (!confirmed) return;
    try {
      await api("/api/admin/principals", {
        method: "DELETE",
        body: JSON.stringify({ id: principal.id, scope: "all" }),
      });
      if (state.selectedPrincipal === principal.id) state.selectedPrincipal = "";
      showToast(t`成员「${principal.display_name}」已删除`);
      await refreshAllData();
      syncLocation(true);
    } catch (err) {
      showToast(err.message, true);
    }
  }

  // Parent navigation always rebuilds the scoped member and credential lists.
  $("tenants-table").addEventListener("click", (event) => {
    const openBtn = event.target.closest(".btn-open-tenant");
    if (openBtn) { openTenant(openBtn.dataset.id); return; }
    const deleteBtn = event.target.closest(".btn-delete-tenant");
    if (deleteBtn) confirmDeleteTenant(deleteBtn.dataset.id);
  });
  if ($("btn-rename-tenant")) $("btn-rename-tenant").addEventListener("click", () => state.memberTenant && promptRenameTenant(state.memberTenant));
  if ($("btn-delete-tenant")) $("btn-delete-tenant").addEventListener("click", () => state.memberTenant && confirmDeleteTenant(state.memberTenant));
  $("btn-tenant-list").addEventListener("click", () => openTenant(""));
  $("btn-tenant-detail").addEventListener("click", () => openTenant(state.memberTenant));
  $("projects-table").addEventListener("click", (event) => {
    const button = event.target.closest(".btn-open-project");
    if (button) openProject(button.dataset.id);
  });
  $("btn-project-list").addEventListener("click", () => openProject(""));
  $("btn-new-project").addEventListener("click", () => { $("project-forms-panel").classList.remove("hidden"); $("proj-name").focus(); });
  $("btn-cancel-project").addEventListener("click", () => { $("project-forms-panel").classList.add("hidden"); $("btn-new-project").focus(); });
  $("pm-principal").addEventListener("change", renderProjectRole);
  $("btn-close-client-config").addEventListener("click", () => closeClientGuides());
  $("client-config-modal").addEventListener("click", (event) => { if (event.target === $("client-config-modal")) closeClientGuides(); });
  $("btn-overview-create-tenant").addEventListener("click", () => {
    openTenant("");
    $("btn-new-tenant").click();
  });
  $("btn-new-tenant").addEventListener("click", () => { $("create-tenant-card").classList.remove("hidden"); $("tenant-name").focus(); });
  $("btn-cancel-tenant").addEventListener("click", () => { $("create-tenant-card").classList.add("hidden"); $("btn-new-tenant").focus(); });
  $("btn-add-member").addEventListener("click", () => {
    $("tenant-forms-panel").classList.remove("hidden");
    clearFormErrors($("create-principal-form"));
    $("prin-create-project").checked = false;
    updatePrincipalMode();
    $("prin-mode").focus();
  });
  $("btn-cancel-member").addEventListener("click", () => { $("tenant-forms-panel").classList.add("hidden"); $("btn-add-member").focus(); });
  $("principals-table").addEventListener("click", (event) => {
    const manageBtn = event.target.closest(".btn-manage-member");
    if (manageBtn) { openTenant(state.memberTenant, manageBtn.dataset.id); return; }
    const deleteBtn = event.target.closest(".btn-delete-member");
    if (deleteBtn) confirmDeleteMember(deleteBtn.dataset.id);
  });
  if ($("btn-rename-member")) $("btn-rename-member").addEventListener("click", () => state.selectedPrincipal && promptRenameMember(state.selectedPrincipal));
  if ($("btn-remove-member-tenant")) $("btn-remove-member-tenant").addEventListener("click", () => state.selectedPrincipal && confirmRemoveMemberFromTenant(state.selectedPrincipal));
  if ($("btn-delete-member")) $("btn-delete-member").addEventListener("click", () => state.selectedPrincipal && confirmDeleteMember(state.selectedPrincipal));
  $("btn-close-member").addEventListener("click", () => openTenant(state.memberTenant));
  if ($("btn-rename-cancel")) $("btn-rename-cancel").addEventListener("click", closeRenameModal);
  if ($("rename-modal")) {
    $("rename-modal").addEventListener("click", (event) => {
      if (event.target === $("rename-modal")) closeRenameModal();
    });
  }
  onSubmit("rename-form", async (e) => {
    e.preventDefault();
    const val = $("rename-input").value.trim();
    if (!val) throw new FormError(t("名称不能为空。"), "rename-input");
    if (state.renameTask) {
      await state.renameTask(val);
      closeRenameModal();
    }
  });
  ["prin-mode", "prin-existing", "prin-role"].forEach((id) => $(id).addEventListener("change", updatePrincipalMode));
  $("member-role").addEventListener("change", () => { $("member-role-hint").textContent = tenantRoleHint($("member-role").value); });

  onSubmit("create-principal-form", async (e) => {
    e.preventDefault();
    const tenantID = state.memberTenant;
    const mode = $("prin-mode").value;
    if (!tenantID || (mode === "bind" && !$("prin-existing").value)) throw new Error(t("请先选择租户和有效成员。"));
    const res = await api("/api/admin/principals", {
      method: "POST",
      body: JSON.stringify({
        mode: $("prin-mode").value,
        principal_id: $("prin-existing").value,
        tenant_id: tenantID,
        identity_provider: $("prin-provider").value,
        subject: $("prin-subject").value,
        display_name: $("prin-name").value,
        tenant_role: $("prin-role").value,
        can_create_project: $("prin-create-project").checked,
      }),
    });
    $("prin-subject").value = "";
    $("prin-name").value = "";
    if (state.memberTenant === tenantID) {
      state.selectedPrincipal = res.principal_id;
      $("tenant-forms-panel").classList.add("hidden");
    }
    showToast(mode === "create" ? t("新成员已创建并授权") : t("已有成员已加入当前租户，其他租户的权限未变更"));
    await refreshAllData();
    if (selectedMember() && state.memberTenant === tenantID) openTenant(tenantID, res.principal_id);
  });

  onSubmit("member-role-form", async (e) => {
    e.preventDefault();
    const principal = selectedMember();
    if (!principal) throw new Error(t("请先选择租户和有效成员。"));
    await api("/api/admin/principals", {
      method: "POST",
      body: JSON.stringify({ mode: "update_role", tenant_id: state.memberTenant, principal_id: principal.id, tenant_role: $("member-role").value, can_create_project: $("member-create-project").checked }),
    });
    showToast(t("成员权限已更新"));
    await refreshAllData();
  });

  // Project updates capture the current identity before awaiting the scoped write.
  $("member-projects-table").addEventListener("click", async (event) => {
    const button = event.target.closest(".btn-save-member-project");
    const principal = selectedMember();
    if (!button || button.disabled || !principal) return;
    const select = button.closest("tr").querySelector("select");
    button.disabled = true;
    button.setAttribute("aria-busy", "true");
    try {
      await api("/api/admin/projects/members", {
        method: "POST",
        body: JSON.stringify({ project_id: button.dataset.project, principal_id: principal.id, role: select.value }),
      });
      showToast(t("项目权限已更新"));
      await refreshAllData();
    } catch (err) {
      showToast(err.message, true);
    } finally {
      button.disabled = false;
      button.removeAttribute("aria-busy");
    }
  });

  onSubmit("member-token-form", async (e) => {
    e.preventDefault();
    const principal = selectedMember();
    if (!principal || activeTab() !== "tab-tenants") throw new Error(t("请先选择租户和有效成员。"));
    const tenantID = state.memberTenant;
    const principalID = principal.id;
    const locationRequest = state.locationRequest;
    const cache = state.revealedTokens;
    const sameContext = () => activeTab() === "tab-tenants" && state.memberTenant === tenantID && state.selectedPrincipal === principalID && state.locationRequest === locationRequest && state.revealedTokens === cache && !$("app-view").classList.contains("hidden") && !$("member-detail-card").classList.contains("hidden");
    const res = await api("/api/admin/tokens", {
      method: "POST",
      body: JSON.stringify({ tenant_id: tenantID, principal_id: principalID, name: $("member-token-name").value }),
    });
    if (state.revealedTokens !== cache) return;
    if (res.id && res.token && sameContext()) cache[res.id] = res.token;
    if (sameContext()) $("member-token-name").value = "";
    showToast(t("客户端密钥已生成，可在该成员的密钥列表中配置。"));
    await refreshAllData();
    // Refresh first so closing the dialog returns focus to the newly created key's own action.
    if (!sameContext()) return;
    const token = currentMemberToken(res.id);
    const button = $("member-tokens-table").querySelector(`.btn-apply-token[data-id="${res.id}"]`);
    if (token && button) { button.focus(); await openClientGuides(token); }
  });

  onSubmit("create-project-form", async (e) => {
    e.preventDefault();
    const res = await api("/api/admin/projects", {
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
    await refreshAllData();
    openProject(res.project_id);
  });

  onSubmit("add-project-member-form", async (e) => {
    e.preventDefault();
    const project = state.projects.find((item) => item.id === state.selectedProject);
    const principal = visiblePrincipals().find((item) => item.id === $("pm-principal").value);
    if (!project || !principal || !tenantMembership(principal, project.tenant_id) || $("pm-role").disabled) throw new Error(t("请选择当前项目所属租户的有效成员。"));
    await api("/api/admin/projects/members", {
      method: "POST",
      body: JSON.stringify({
        project_id: project.id,
        principal_id: $("pm-principal").value,
        role: $("pm-role").value,
      }),
    });
    showToast(t("项目权限已更新"));
    refreshAllData();
  });

  onSubmit("add-project-remote-form", async (e) => {
    e.preventDefault();
    const project = state.projects.find((item) => item.id === state.selectedProject);
    if (!project) throw new Error(t("项目不存在，请刷新后重新选择。"));
    await api("/api/admin/projects/remotes", {
      method: "POST",
      body: JSON.stringify({
        project_id: project.id,
        remote_url: $("pr-remote").value,
        workspace_path: $("pr-workspace").value,
      }),
    });
    $("pr-remote").value = "";
    $("pr-workspace").value = "";
    showToast(t("仓库地址已添加"));
    refreshAllData();
  });

  onSubmit("filter-memories-form", async (e) => {
    e.preventDefault();
    state.memoryPage = 1;
    syncLocation(true);
    await loadMemories();
  });

  $("btn-toggle-memory-filters").addEventListener("click", () => {
    const button = $("btn-toggle-memory-filters");
    button.setAttribute("aria-expanded", button.getAttribute("aria-expanded") === "true" ? "false" : "true");
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
      modal.querySelector(".modal-card").scrollTop = 0;
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
    const renameOpen = $("rename-modal") && !$("rename-modal").classList.contains("hidden");
    const purgeOpen = !$("purge-modal").classList.contains("hidden");
    const auditDetailOpen = $("audit-detail-modal") && !$("audit-detail-modal").classList.contains("hidden");
    const auditCleanupOpen = $("audit-cleanup-modal") && !$("audit-cleanup-modal").classList.contains("hidden");
    const schedConfigOpen = $("scheduler-config-modal") && !$("scheduler-config-modal").classList.contains("hidden");
    const glossaryOpen = !$("glossary-modal").classList.contains("hidden");
    const clientConfigOpen = !$("client-config-modal").classList.contains("hidden");
    const detailOpen = !$("memory-detail-card").classList.contains("hidden");
    if (e.key === "Escape") {
      if (confirmOpen) state.confirmResolve && state.confirmResolve(false);
      else if (renameOpen) closeRenameModal();
      else if (purgeOpen) closePurgeModal();
      else if (auditDetailOpen) $("audit-detail-modal").classList.add("hidden");
      else if (auditCleanupOpen) $("audit-cleanup-modal").classList.add("hidden");
      else if (schedConfigOpen) $("scheduler-config-modal").classList.add("hidden");
      else if (glossaryOpen) toggleGlossaryModal(false);
      else if (clientConfigOpen) closeClientGuides();
      else if (detailOpen) closeMemoryDetail();
      else if ($("app-navigation") && $("app-navigation").classList.contains("open")) setSidebarOpen(false);
      else if (document.querySelector(".account-menu").open) {
        document.querySelector(".account-menu").open = false;
        document.querySelector(".account-menu summary").focus();
      }
    }
    if (e.key === "Tab" && (confirmOpen || renameOpen || purgeOpen || glossaryOpen || clientConfigOpen || detailOpen)) {
      const dialog = $(confirmOpen ? "confirm-modal" : renameOpen ? "rename-modal" : purgeOpen ? "purge-modal" : glossaryOpen ? "glossary-modal" : clientConfigOpen ? "client-config-modal" : "memory-detail-card");
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

  function selectedMigrationExportMode() {
    const checked = document.querySelector('input[name="mig-export-mode"]:checked');
    return checked ? checked.value : "knowledge";
  }

  function syncMigrationExportMode() {
    const mode = selectedMigrationExportMode();
    const isKnowledge = mode === "knowledge";
    if ($("mig-export-knowledge-options")) $("mig-export-knowledge-options").classList.toggle("hidden", !isKnowledge);
    if ($("mig-export-passphrase-hint-knowledge")) $("mig-export-passphrase-hint-knowledge").classList.toggle("hidden", !isKnowledge);
    if ($("mig-export-passphrase-hint-system")) $("mig-export-passphrase-hint-system").classList.toggle("hidden", isKnowledge);
  }

  function renderMigrationSelectors() {
    const list = $("mig-export-tenants-list");
    if (list) {
      if (!state.tenants.length) {
        list.innerHTML = `<span class="muted">${t("尚无租户")}</span>`;
      } else {
        const prevChecked = new Set(Array.from(list.querySelectorAll('input[type="checkbox"]:checked')).map((el) => el.value));
        const defaultAll = !state.migExportTenantsInitialized;
        list.innerHTML = state.tenants
          .map((tenant) => {
            const checked = defaultAll || prevChecked.has(tenant.id) ? "checked" : "";
            return `<label class="checkbox-label"><input type="checkbox" value="${escapeHtml(tenant.id)}" ${checked} /><span>${escapeHtml(tenantLabel(tenant))}</span></label>`;
          })
          .join("");
        state.migExportTenantsInitialized = true;
      }
    }

    const targetSelect = $("mig-import-target-tenant");
    if (targetSelect) {
      const prev = targetSelect.value;
      targetSelect.innerHTML = state.tenants
        .map((tenant) => `<option value="${escapeHtml(tenant.id)}">${escapeHtml(tenantLabel(tenant))}</option>`)
        .join("");
      if (prev && Array.from(targetSelect.options).some((opt) => opt.value === prev)) {
        targetSelect.value = prev;
      }
    }
  }

  function migrationFileKey(file) {
    return file ? `${file.name}:${file.size}:${file.lastModified}` : "";
  }

  function syncMigrationImportTenantMode() {
    const mode = $("mig-import-tenant-mode") ? $("mig-import-tenant-mode").value : "by_name";
    if ($("mig-import-target-tenant-group")) {
      $("mig-import-target-tenant-group").classList.toggle("hidden", mode !== "single");
    }
  }

  function renderMigrationInspect() {
    const box = $("mig-import-inspect-box");
    const knowledgeOpts = $("mig-import-knowledge-options");
    const systemOpts = $("mig-import-system-options");
    const submitBtn = $("btn-mig-import-submit");
    if (!box || !knowledgeOpts || !systemOpts || !submitBtn) return;

    const info = state.migInspect;
    if (!info) {
      box.classList.add("hidden");
      box.innerHTML = "";
      knowledgeOpts.classList.add("hidden");
      systemOpts.classList.add("hidden");
      submitBtn.textContent = t("导入迁移包");
      submitBtn.classList.remove("danger");
      return;
    }

    const isKnowledge = info.format === "engram-knowledge-bundle";
    const typeBadge = isKnowledge ? `<span class="badge">${t("知识库迁移包")}</span>` : `<span class="badge warning">${t("全量系统迁移包")}</span>`;
    const encBadge = info.encrypted ? `<span class="badge">${t("已加密")}</span>` : `<span class="badge success">${t("未加密")}</span>`;
    const c = info.counts || {};
    const statsParts = [
      t`${c.tenants || 0} 个租户`,
      t`${c.projects || 0} 个项目`,
      t`${c.memories || 0} 条知识`,
      t`${c.memory_revisions || 0} 条版本历史`,
    ];
    if (!isKnowledge) {
      statsParts.push(t`${c.principals || 0} 位成员`, t`${c.api_tokens || 0} 个密钥`);
    }

    let detailsHtml = `<div class="mig-inspect-header">${typeBadge}${encBadge}<span class="meta-text">${escapeHtml(formatDate(info.exported_at))}</span></div>`;
    detailsHtml += `<p class="desc" style="margin-bottom:0;">${escapeHtml(statsParts.join(" · "))}</p>`;
    if (info.needs_passphrase) {
      detailsHtml += `<p class="field-hint" style="margin-top:8px;">${t("该迁移包已加密，请填写解密口令后点击「解析迁移包」。")}</p>`;
      box.innerHTML = detailsHtml;
      box.classList.remove("hidden");
      knowledgeOpts.classList.add("hidden");
      systemOpts.classList.add("hidden");
      submitBtn.textContent = t("导入迁移包");
      submitBtn.classList.remove("danger");
      return;
    }

    if (Array.isArray(info.tenants) && info.tenants.length) {
      detailsHtml += `<ul class="mig-inspect-tenants">${info.tenants
        .map((item) => `<li>${escapeHtml(item.name)} · ${escapeHtml(t`${item.projects_count} 个项目`)} · ${escapeHtml(t`${item.memories_count} 条知识`)}</li>`)
        .join("")}</ul>`;
    }
    box.innerHTML = detailsHtml;
    box.classList.remove("hidden");

    if (isKnowledge) {
      knowledgeOpts.classList.remove("hidden");
      systemOpts.classList.add("hidden");
      syncMigrationImportTenantMode();
      submitBtn.textContent = t("导入知识库");
      submitBtn.classList.remove("danger");
    } else {
      knowledgeOpts.classList.add("hidden");
      systemOpts.classList.remove("hidden");
      submitBtn.textContent = t("上传并还原");
      submitBtn.classList.add("danger");
    }
  }

  async function inspectMigrationFile() {
    const fileInput = $("mig-import-file");
    if (!fileInput || !fileInput.files || fileInput.files.length === 0) {
      state.migInspect = null;
      renderMigrationInspect();
      return null;
    }
    const file = fileInput.files[0];
    const fd = new FormData();
    fd.append("file", file);
    fd.append("passphrase", $("mig-import-passphrase") ? $("mig-import-passphrase").value : "");
    const resp = await fetch("/api/admin/migration/inspect", {
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
    data.fileKey = migrationFileKey(file);
    state.migInspect = data;
    renderMigrationInspect();
    return data;
  }

  document.querySelectorAll('input[name="mig-export-mode"]').forEach((radio) => {
    radio.addEventListener("change", syncMigrationExportMode);
  });
  syncMigrationExportMode();

  if ($("btn-mig-tenants-all")) {
    $("btn-mig-tenants-all").addEventListener("click", () => {
      document.querySelectorAll('#mig-export-tenants-list input[type="checkbox"]').forEach((el) => { el.checked = true; });
    });
  }
  if ($("btn-mig-tenants-none")) {
    $("btn-mig-tenants-none").addEventListener("click", () => {
      document.querySelectorAll('#mig-export-tenants-list input[type="checkbox"]').forEach((el) => { el.checked = false; });
    });
  }
  if ($("mig-import-tenant-mode")) {
    $("mig-import-tenant-mode").addEventListener("change", syncMigrationImportTenantMode);
  }
  if ($("mig-import-file")) {
    $("mig-import-file").addEventListener("change", async () => {
      state.migInspect = null;
      if ($("mig-import-result")) $("mig-import-result").classList.add("hidden");
      renderMigrationInspect();
      try {
        await inspectMigrationFile();
      } catch (err) {
        showToast(err.message, true);
      }
    });
  }
  if ($("btn-mig-inspect")) {
    $("btn-mig-inspect").addEventListener("click", async () => {
      const fileInput = $("mig-import-file");
      if (!fileInput || !fileInput.files || fileInput.files.length === 0) {
        showToast(t("请先选择要导入的迁移包（.zip）。"), true);
        return;
      }
      try {
        await inspectMigrationFile();
      } catch (err) {
        showToast(err.message, true);
      }
    });
  }

  if ($("migration-export-form")) {
    onSubmit("migration-export-form", async (e) => {
      e.preventDefault();
      const mode = selectedMigrationExportMode();
      const passphrase = $("mig-export-passphrase") ? $("mig-export-passphrase").value : "";
      const payload = { mode, passphrase };
      if (mode === "knowledge") {
        const selectedTenants = Array.from(document.querySelectorAll('#mig-export-tenants-list input[type="checkbox"]:checked')).map((el) => el.value);
        if (state.tenants.length > 0 && selectedTenants.length === 0) {
          throw new Error(t("请至少选择一个要导出的租户。"));
        }
        payload.tenant_ids = selectedTenants;
        payload.include_deleted = $("mig-export-include-deleted") ? $("mig-export-include-deleted").checked : true;
      }
      const resp = await fetch("/api/admin/migration/export", {
        method: "POST",
        credentials: "same-origin",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
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
      const fallbackName = mode === "knowledge" ? "engram-knowledge-bundle.zip" : "engram-system-snapshot.zip";
      const filename = parseDownloadFilename(resp, fallbackName);
      const saved = await saveBlobToLocal(blob, filename);
      if (saved) {
        const enc = resp.headers.get("X-Snapshot-Encrypted") === "true";
        const mems = resp.headers.get("X-Snapshot-Memories") || "0";
        const tenants = resp.headers.get("X-Snapshot-Tenants") || "0";
        const encText = enc ? t("已加密") : t("未加密");
        showToast(
          mode === "knowledge"
            ? t`知识库迁移包已导出（${encText}，${tenants} 个租户，含 ${mems} 条记忆）`
            : t`系统迁移包已导出（${encText}，含 ${mems} 条记忆）`
        );
      }
    });
  }

  if ($("migration-import-form")) {
    onSubmit("migration-import-form", async (e) => {
      e.preventDefault();
      const fileInput = $("mig-import-file");
      if (!fileInput || !fileInput.files || fileInput.files.length === 0) throw new FormError(t("请先选择要导入的迁移包（.zip）。"), "mig-import-file");
      const file = fileInput.files[0];
      const fileKey = migrationFileKey(file);

      if (!state.migInspect || state.migInspect.needs_passphrase || state.migInspect.fileKey !== fileKey) {
        const inspected = await inspectMigrationFile();
        if (!inspected) return;
        if (inspected.needs_passphrase) {
          throw new FormError(t("该迁移包已加密，请先填写解密口令。"), "mig-import-passphrase");
        }
        if (inspected.format === "engram-system-snapshot" && inspected.target_has_data && !($("mig-import-confirm") && $("mig-import-confirm").checked)) {
          throw new FormError(t("已识别为全量系统迁移包，请勾选确认覆盖后再导入。"), "mig-import-confirm");
        }
      }

      const fd = new FormData();
      fd.append("file", file);
      fd.append("passphrase", $("mig-import-passphrase") ? $("mig-import-passphrase").value : "");
      if (state.migInspect && state.migInspect.format === "engram-knowledge-bundle") {
        const tenantMode = $("mig-import-tenant-mode") ? $("mig-import-tenant-mode").value : "by_name";
        fd.append("tenant_mode", tenantMode);
        if (tenantMode === "single") {
          const targetTenantID = $("mig-import-target-tenant") ? $("mig-import-target-tenant").value : "";
          if (!targetTenantID) throw new FormError(t("请选择要导入的目标租户。"), "mig-import-target-tenant");
          fd.append("target_tenant_id", targetTenantID);
        }
      } else {
        fd.append("confirm_replace", $("mig-import-confirm") && $("mig-import-confirm").checked ? "true" : "false");
      }

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
        const c = data.counts || {};
        if ($("mig-import-result-badge")) {
          $("mig-import-result-badge").textContent = data.format === "engram-knowledge-bundle" ? t("导入成功") : t("还原成功");
        }
        if ($("mig-import-result-summary")) {
          $("mig-import-result-summary").textContent = data.format === "engram-knowledge-bundle"
            ? t`匹配租户 ${c.tenants_matched || 0} 个、新建 ${c.tenants_created || 0} 个；导入知识 ${c.memories_imported || 0} 条、跳过重复 ${c.memories_skipped || 0} 条、导入版本历史 ${c.revisions_imported || 0} 条。`
            : t`已还原 ${c.tenants || 0} 个租户、${c.projects || 0} 个项目、${c.memories || 0} 条知识、${c.api_tokens || 0} 个密钥。`;
        }
        $("mig-import-result").classList.remove("hidden");
        $("mig-import-result-box").textContent = JSON.stringify(data, null, 2);
      }
      if ($("mig-import-passphrase")) $("mig-import-passphrase").value = "";
      showToast(data.format === "engram-knowledge-bundle" ? t("知识库导入成功！") : t("系统快照还原成功！"));
      await refreshAllData();
    });
  }

  // Audit Center & Scheduled Tasks
  const AUDIT_SOURCES = {
    mcp: t("MCP 客户端"),
    admin_ui: t("管理控制台"),
    scheduler: t("定时任务"),
    system: t("系统"),
  };
  const AUDIT_STATUSES = {
    success: t("成功"),
    failed: t("失败"),
    denied: t("拒绝"),
  };
  const SCHED_STATUSES = {
    idle: t("待运行"),
    running: t("执行中"),
    success: t("成功"),
    failed: t("失败"),
  };
  const SEVERITY_LABELS = {
    high: t("高风险"),
    medium: t("中风险"),
    low: t("低风险"),
    info: t("提示"),
  };

  function auditStatusBadgeClass(status) {
    if (status === "success") return "success";
    if (status === "denied") return "warning";
    return "danger";
  }

  function schedStatusBadgeClass(status) {
    if (status === "success") return "success";
    if (status === "failed") return "danger";
    if (status === "running") return "warning";
    return "";
  }

  function formatDurationSeconds(sec) {
    if (!Number.isFinite(sec) || sec <= 0) return "—";
    if (sec % 86400 === 0) return t`${sec / 86400} 天`;
    if (sec % 3600 === 0) return t`${sec / 3600} 小时`;
    if (sec % 60 === 0) return t`${sec / 60} 分钟`;
    return t`${sec} 秒`;
  }

  function switchAuditSubTab(sub) {
    state.auditSubTab = sub === "tasks" ? "tasks" : "logs";
    const isLogs = state.auditSubTab === "logs";
    if ($("btn-audit-sub-logs")) {
      $("btn-audit-sub-logs").classList.toggle("active", isLogs);
      $("btn-audit-sub-logs").setAttribute("aria-selected", String(isLogs));
    }
    if ($("btn-audit-sub-tasks")) {
      $("btn-audit-sub-tasks").classList.toggle("active", !isLogs);
      $("btn-audit-sub-tasks").setAttribute("aria-selected", String(!isLogs));
    }
    if ($("audit-logs-panel")) $("audit-logs-panel").classList.toggle("hidden", !isLogs);
    if ($("audit-tasks-panel")) $("audit-tasks-panel").classList.toggle("hidden", isLogs);
  }

  function buildAuditFilterParams(includePaging = true) {
    const p = new URLSearchParams();
    const q = $("audit-filter-q") ? $("audit-filter-q").value.trim() : "";
    const src = $("audit-filter-source") ? $("audit-filter-source").value : "";
    const status = $("audit-filter-status") ? $("audit-filter-status").value : "";
    const tenant = $("audit-filter-tenant") ? $("audit-filter-tenant").value : "";
    const action = $("audit-filter-action") ? $("audit-filter-action").value.trim() : "";
    if (q) p.set("q", q);
    if (src) p.set("source", src);
    if (status) p.set("status", status);
    if (tenant) p.set("tenant_id", tenant);
    if (action) p.set("action", action);
    if (includePaging) {
      p.set("page", state.auditPage);
      p.set("page_size", state.auditPageSize);
    }
    return p;
  }

  async function loadAuditStats() {
    if (!$("audit-stat-total")) return;
    const stats = await api("/api/admin/audit/stats");
    state.auditStats = stats;
    $("audit-stat-total").textContent = stats.total_count || 0;
    $("audit-stat-24h").textContent = stats.last_24h_count || 0;
    $("audit-stat-failed").textContent = stats.last_24h_failed || 0;
    $("audit-stat-retention").textContent = stats.default_retention || "30d";
    $("audit-stat-oldest").textContent = stats.oldest_at ? formatDate(stats.oldest_at) : "—";
  }

  async function loadAuditLogs() {
    if (!$("audit-logs-table")) return;
    const reqId = ++state.auditRequest;
    const data = await api("/api/admin/audit/logs?" + buildAuditFilterParams(true).toString());
    if (reqId !== state.auditRequest) return;
    state.auditLogs = data.items || [];
    state.auditTotal = data.total || 0;
    state.auditPage = data.page || 1;
    state.auditPageSize = data.page_size || 20;
    renderAuditLogsTable();
  }

  function renderAuditLogsTable() {
    const tbody = $("audit-logs-table") && $("audit-logs-table").querySelector("tbody");
    if (!tbody) return;
    const totalPages = Math.max(1, Math.ceil(state.auditTotal / state.auditPageSize));
    if ($("audit-list-status")) {
      $("audit-list-status").textContent = state.auditTotal
        ? t`共 ${state.auditTotal} 条审计日志`
        : t("暂无匹配的审计日志");
    }
    if ($("audit-page-info")) $("audit-page-info").textContent = t`第 ${state.auditPage} / ${totalPages} 页`;
    if ($("btn-audit-prev")) $("btn-audit-prev").disabled = state.auditPage <= 1;
    if ($("btn-audit-next")) $("btn-audit-next").disabled = state.auditPage >= totalPages;

    if (!state.auditLogs.length) {
      tbody.innerHTML = `<tr><td colspan="8" class="empty-state">${t("暂无匹配的审计日志。")}</td></tr>`;
      return;
    }

    tbody.innerHTML = state.auditLogs.map((entry) => {
      const actor = entry.actor_name || entry.actor_id || entry.actor_type || "—";
      const actorSub = entry.agent_name ? `<div class="muted" style="font-size:12px;">${escapeHtml(entry.agent_name)}</div>` : "";
      const resLabel = entry.resource_type
        ? `${escapeHtml(entry.resource_type)}${entry.resource_id ? ` · <code>${escapeHtml(entry.resource_id.slice(0, 12))}</code>` : ""}`
        : "—";
      return `<tr>
        <td><time title="${escapeHtml(formatDate(entry.created_at))}">${escapeHtml(formatDate(entry.created_at))}</time></td>
        <td><span class="badge">${escapeHtml(AUDIT_SOURCES[entry.source] || entry.source || "—")}</span></td>
        <td><code>${escapeHtml(entry.action)}</code></td>
        <td><div>${escapeHtml(actor)}</div>${actorSub}</td>
        <td>${resLabel}</td>
        <td><span class="badge ${auditStatusBadgeClass(entry.status)}">${escapeHtml(AUDIT_STATUSES[entry.status] || entry.status)}</span></td>
        <td>${entry.duration_ms || 0} ms</td>
        <td><button type="button" class="secondary small btn-audit-detail" data-id="${entry.id}">${t("详情")}</button></td>
      </tr>`;
    }).join("");
  }

  function openAuditDetail(entry) {
    if (!entry || !$("audit-detail-modal")) return;
    const meta = [
      `<span class="badge ${auditStatusBadgeClass(entry.status)}">${escapeHtml(AUDIT_STATUSES[entry.status] || entry.status)}</span>`,
      `<span class="badge">${escapeHtml(AUDIT_SOURCES[entry.source] || entry.source)}</span>`,
      `<code>${escapeHtml(entry.action)}</code>`,
      `<span class="meta-text">${escapeHtml(formatDate(entry.created_at))}</span>`,
    ];
    if (entry.request_id) meta.push(`<span class="meta-text">Request: <code>${escapeHtml(entry.request_id)}</code></span>`);
    if (entry.client_ip) meta.push(`<span class="meta-text">IP: <code>${escapeHtml(entry.client_ip)}</code></span>`);
    $("audit-detail-meta").innerHTML = meta.join("");
    $("audit-detail-json").textContent = JSON.stringify(entry, null, 2);
    $("audit-detail-modal").classList.remove("hidden");
  }

  async function loadAuditTasks() {
    if (!$("audit-task-definitions")) return;
    const data = await api("/api/admin/audit/tasks?page=1&page_size=20");
    state.auditTaskDefs = data.definitions || [];
    state.auditTaskRuns = data.runs || [];
    renderAuditTaskDefinitions();
    renderAuditTaskRuns();
  }

  function renderAuditTaskDefinitions() {
    const container = $("audit-task-definitions");
    if (!container) return;
    container.innerHTML = state.auditTaskDefs.map((def) => `
      <article class="audit-task-card">
        <div>
          <h3>${escapeHtml(t(def.name))}</h3>
          <p>${escapeHtml(t(def.description))}</p>
        </div>
        <div>
          <button type="button" class="secondary small btn-run-audit-task" data-type="${escapeHtml(def.task_type)}">${t("执行审计任务")}</button>
        </div>
      </article>
    `).join("");
  }

  function renderAuditTaskFindings(run) {
    const card = $("audit-task-latest-findings");
    const list = $("audit-findings-list");
    if (!card || !list) return;
    if (!run) {
      card.classList.add("hidden");
      return;
    }
    const def = state.auditTaskDefs.find((d) => d.task_type === run.task_type);
    $("audit-findings-title").textContent = def ? t(def.name) : run.task_type;
    $("audit-findings-summary").textContent = `${run.summary || ""} · ${formatDate(run.started_at)}`;
    const findings = run.findings || [];
    if (!findings.length) {
      list.innerHTML = `<div class="audit-finding-item"><p>${escapeHtml(run.summary || t("未发现异常风险项。"))}</p></div>`;
    } else {
      list.innerHTML = findings.map((f) => `
        <div class="audit-finding-item">
          <div class="audit-finding-header">
            <span class="badge sev-${escapeHtml(f.severity || "info")}">${escapeHtml(SEVERITY_LABELS[f.severity] || f.severity || "info")}</span>
            <strong>${escapeHtml(f.title)}</strong>
            <code>${escapeHtml(f.code)}</code>
          </div>
          <p>${escapeHtml(f.description)}</p>
        </div>
      `).join("");
    }
    card.classList.remove("hidden");
  }

  function renderAuditTaskRuns() {
    const tbody = $("audit-task-runs-table") && $("audit-task-runs-table").querySelector("tbody");
    if (!tbody) return;
    if (!state.auditTaskRuns.length) {
      tbody.innerHTML = `<tr><td colspan="7" class="empty-state">${t("尚无审计任务执行记录。")}</td></tr>`;
      return;
    }
    tbody.innerHTML = state.auditTaskRuns.map((run) => {
      const def = state.auditTaskDefs.find((d) => d.task_type === run.task_type);
      const label = def ? t(def.name) : run.task_type;
      return `<tr>
        <td>${escapeHtml(formatDate(run.started_at))}</td>
        <td><strong>${escapeHtml(label)}</strong></td>
        <td><span class="badge ${schedStatusBadgeClass(run.status)}">${escapeHtml(SCHED_STATUSES[run.status] || run.status)}</span></td>
        <td>${run.findings_count || 0}</td>
        <td>${run.duration_ms || 0} ms</td>
        <td>${escapeHtml(run.summary || run.error_message || "—")}</td>
        <td><button type="button" class="secondary small btn-view-audit-run" data-id="${run.id}">${t("查看发现")}</button></td>
      </tr>`;
    }).join("");
  }

  async function loadAuditCenter() {
    await Promise.all([loadAuditStats(), loadAuditLogs(), loadAuditTasks()]);
  }

  async function loadSchedulerCenter() {
    await Promise.all([loadSchedulerTasks(), loadSchedulerRuns()]);
  }

  async function loadSchedulerTasks() {
    if (!$("scheduler-tasks-table")) return;
    const data = await api("/api/admin/scheduler/tasks");
    state.schedulerTasks = data.items || [];
    renderSchedulerTasks();
    const filterSel = $("scheduler-runs-filter");
    if (filterSel) {
      fillSelect("scheduler-runs-filter", state.schedulerTasks.map((task) => [task.name, t(task.display_name) || task.name]));
    }
  }

  function renderSchedulerTasks() {
    const tbody = $("scheduler-tasks-table") && $("scheduler-tasks-table").querySelector("tbody");
    if (!tbody) return;
    if (!state.schedulerTasks.length) {
      tbody.innerHTML = `<tr><td colspan="7" class="empty-state">${t("暂无已注册的定时任务。")}</td></tr>`;
      return;
    }
    tbody.innerHTML = state.schedulerTasks.map((task) => {
      const cfg = task.config || {};
      const cfgPills = [];
      if (cfg.retention) cfgPills.push(`<span class="badge">${t`保留 ${escapeHtml(String(cfg.retention))}`}</span>`);
      if (cfg.batch_size) cfgPills.push(`<span class="badge">${t`每批 ${escapeHtml(String(cfg.batch_size))} 条`}</span>`);
      if (cfg.migration_mode) cfgPills.push(`<span class="badge">${escapeHtml(cfg.migration_mode === "knowledge" ? t("仅知识库") : t("全量系统"))}</span>`);
      if (cfg.upload_mode) cfgPills.push(`<span class="badge">${escapeHtml(String(cfg.upload_mode))}</span>`);
      if (cfg.remote_path) cfgPills.push(`<span class="badge"><code>${escapeHtml(String(cfg.remote_path))}</code></span>`);
      if (cfg.targets) cfgPills.push(`<span class="badge">${escapeHtml(String(cfg.targets))}</span>`);
      const lastAt = task.last_run_at ? formatDate(task.last_run_at) : t("尚未执行");
      const nextAt = task.enabled && task.next_run_at ? formatDate(task.next_run_at) : t("已暂停");
      const lastOutcome = task.last_status && task.last_status !== "idle"
        ? `<span class="badge ${schedStatusBadgeClass(task.last_status)}">${escapeHtml(SCHED_STATUSES[task.last_status] || task.last_status)}</span>`
        : `<span class="badge">${t("待运行")}</span>`;
      const toggleLabel = task.enabled ? t("停用") : t("启用");
      const toggleClass = task.enabled ? "danger-outline small btn-sched-toggle" : "secondary small btn-sched-toggle";
      return `<tr>
        <td>
          <strong>${escapeHtml(t(task.display_name) || task.name)}</strong>
          <div class="muted" style="font-size:12px;"><code>${escapeHtml(task.name)}</code> · ${escapeHtml(t(task.description))}</div>
        </td>
        <td>
          <span class="badge ${task.enabled ? "success" : "danger"}">${task.enabled ? t("已启用") : t("已停用")}</span>
          <div style="margin-top:4px;">${lastOutcome}</div>
        </td>
        <td>${escapeHtml(formatDurationSeconds(task.interval_seconds))}</td>
        <td><div class="sched-config-pills">${cfgPills.join("") || "—"}</div></td>
        <td>
          <div style="font-size:12.5px;">${t("上次")}: ${escapeHtml(lastAt)}</div>
          <div class="muted" style="font-size:12px;">${t("下次")}: ${escapeHtml(nextAt)}</div>
        </td>
        <td>
          <div style="font-size:12.5px;">${escapeHtml(task.last_summary || task.last_error || "—")}</div>
          <div class="muted" style="font-size:12px;">${t`累计 ${task.run_count || 0} 次`}</div>
        </td>
        <td>
          <div class="row-actions">
            <button type="button" class="${toggleClass}" data-name="${escapeHtml(task.name)}" data-enabled="${task.enabled ? "true" : "false"}">${toggleLabel}</button>
            <button type="button" class="secondary small btn-sched-config" data-name="${escapeHtml(task.name)}">${t("配置")}</button>
            <button type="button" class="small btn-sched-run" data-name="${escapeHtml(task.name)}">${t("立即执行")}</button>
          </div>
        </td>
      </tr>`;
    }).join("");
  }

  async function loadSchedulerRuns() {
    if (!$("scheduler-runs-table")) return;
    const p = new URLSearchParams();
    p.set("page", state.schedulerRunsPage);
    p.set("page_size", state.schedulerRunsPageSize);
    const taskFilter = $("scheduler-runs-filter") ? $("scheduler-runs-filter").value : "";
    if (taskFilter) p.set("task_name", taskFilter);
    const data = await api("/api/admin/scheduler/runs?" + p.toString());
    state.schedulerRuns = data.items || [];
    state.schedulerRunsTotal = data.total || 0;
    state.schedulerRunsPage = data.page || 1;
    renderSchedulerRuns();
  }

  function renderSchedulerRuns() {
    const tbody = $("scheduler-runs-table") && $("scheduler-runs-table").querySelector("tbody");
    if (!tbody) return;
    const totalPages = Math.max(1, Math.ceil(state.schedulerRunsTotal / state.schedulerRunsPageSize));
    if ($("scheduler-runs-page-info")) $("scheduler-runs-page-info").textContent = t`第 ${state.schedulerRunsPage} / ${totalPages} 页`;
    if ($("btn-sched-runs-prev")) $("btn-sched-runs-prev").disabled = state.schedulerRunsPage <= 1;
    if ($("btn-sched-runs-next")) $("btn-sched-runs-next").disabled = state.schedulerRunsPage >= totalPages;

    if (!state.schedulerRuns.length) {
      tbody.innerHTML = `<tr><td colspan="7" class="empty-state">${t("暂无定时任务执行历史。")}</td></tr>`;
      return;
    }
    tbody.innerHTML = state.schedulerRuns.map((run) => {
      const task = state.schedulerTasks.find((item) => item.name === run.task_name);
      const taskTitle = task ? t(task.display_name) : run.task_name;
      const triggerLabel = run.trigger_type === "manual" ? t("手动触发") : t("自动调度");
      return `<tr>
        <td>${escapeHtml(formatDate(run.started_at))}</td>
        <td><strong>${escapeHtml(taskTitle)}</strong></td>
        <td><span class="badge">${escapeHtml(triggerLabel)}</span></td>
        <td><span class="badge ${schedStatusBadgeClass(run.status)}">${escapeHtml(SCHED_STATUSES[run.status] || run.status)}</span></td>
        <td>${run.duration_ms || 0} ms</td>
        <td>${run.affected_rows || 0}</td>
        <td>${escapeHtml(run.summary || run.error_message || "—")}</td>
      </tr>`;
    }).join("");
  }

  function openSchedulerConfigModal(taskName) {
    const task = state.schedulerTasks.find((item) => item.name === taskName);
    if (!task || !$("scheduler-config-modal")) return;
    $("sched-cfg-name").value = task.name;
    $("scheduler-config-title").textContent = `${t("配置定时任务")} · ${t(task.display_name) || task.name}`;
    $("sched-cfg-desc").textContent = t(task.description) || "";
    $("sched-cfg-enabled").checked = !!task.enabled;
    $("sched-cfg-interval").value = formatIntervalInput(task.interval_seconds);
    const cfg = task.config || {};
    const isCleanup = task.name === "audit_log_cleanup";
    const isSafekeep = task.name === "safekeep_migration_backup";
    $("sched-cfg-audit-cleanup-fields").classList.toggle("hidden", !isCleanup);
    $("sched-cfg-retention").required = isCleanup;
    $("sched-cfg-batch-size").required = isCleanup;
    if (isCleanup) {
      $("sched-cfg-retention").value = cfg.retention || "30d";
      $("sched-cfg-batch-size").value = cfg.batch_size || 1000;
    }
    if ($("sched-cfg-safekeep-fields")) {
      $("sched-cfg-safekeep-fields").classList.toggle("hidden", !isSafekeep);
      if (isSafekeep) {
        $("sched-cfg-mig-mode").value = cfg.migration_mode || "system";
        $("sched-cfg-include-deleted").checked = cfg.include_deleted !== false;
        $("sched-cfg-upload-mode").value = cfg.upload_mode || "";
        $("sched-cfg-remote-path").value = cfg.remote_path || "";
        $("sched-cfg-targets").value = cfg.targets || "";
        if ($("sched-cfg-include-deleted-group")) {
          $("sched-cfg-include-deleted-group").classList.toggle("hidden", $("sched-cfg-mig-mode").value !== "knowledge");
        }
      }
    }
    $("scheduler-config-modal").classList.remove("hidden");
  }

  function formatIntervalInput(seconds) {
    if (!Number.isFinite(seconds) || seconds <= 0) return "1h";
    if (seconds % 86400 === 0) return `${seconds / 86400}d`;
    if (seconds % 3600 === 0) return `${seconds / 3600}h`;
    if (seconds % 60 === 0) return `${seconds / 60}m`;
    return `${seconds}s`;
  }

  document.querySelectorAll(".preset-chips").forEach((group) => {
    group.addEventListener("click", (e) => {
      const chip = e.target.closest(".preset-chip");
      if (!chip) return;
      const input = $(group.dataset.target);
      if (input) input.value = chip.dataset.value || "";
    });
  });

  if ($("btn-audit-sub-logs")) $("btn-audit-sub-logs").addEventListener("click", () => switchAuditSubTab("logs"));
  if ($("btn-audit-sub-tasks")) $("btn-audit-sub-tasks").addEventListener("click", () => switchAuditSubTab("tasks"));

  if ($("filter-audit-form")) {
    $("filter-audit-form").addEventListener("submit", async (e) => {
      e.preventDefault();
      state.auditPage = 1;
      try { await loadAuditLogs(); } catch (err) { showToast(err.message, true); }
    });
  }
  ["audit-filter-source", "audit-filter-status", "audit-filter-tenant"].forEach((id) => {
    if ($(id)) {
      $(id).addEventListener("change", async () => {
        state.auditPage = 1;
        try { await loadAuditLogs(); } catch (err) { showToast(err.message, true); }
      });
    }
  });
  if ($("audit-page-size")) {
    $("audit-page-size").addEventListener("change", async () => {
      state.auditPageSize = Number($("audit-page-size").value) || 20;
      state.auditPage = 1;
      try { await loadAuditLogs(); } catch (err) { showToast(err.message, true); }
    });
  }
  if ($("btn-clear-audit-filters")) {
    $("btn-clear-audit-filters").addEventListener("click", async () => {
      ["audit-filter-q", "audit-filter-source", "audit-filter-status", "audit-filter-tenant", "audit-filter-action"].forEach((id) => {
        if ($(id)) $(id).value = "";
      });
      state.auditPage = 1;
      try { await loadAuditLogs(); } catch (err) { showToast(err.message, true); }
    });
  }
  if ($("btn-audit-prev")) {
    $("btn-audit-prev").addEventListener("click", async () => {
      if (state.auditPage <= 1) return;
      state.auditPage--;
      try { await loadAuditLogs(); } catch (err) { showToast(err.message, true); }
    });
  }
  if ($("btn-audit-next")) {
    $("btn-audit-next").addEventListener("click", async () => {
      state.auditPage++;
      try { await loadAuditLogs(); } catch (err) { showToast(err.message, true); }
    });
  }

  if ($("audit-logs-table")) {
    $("audit-logs-table").addEventListener("click", (e) => {
      const btn = e.target.closest(".btn-audit-detail");
      if (!btn) return;
      const entry = state.auditLogs.find((item) => item.id === btn.dataset.id);
      if (entry) openAuditDetail(entry);
    });
  }
  if ($("btn-close-audit-detail")) $("btn-close-audit-detail").addEventListener("click", () => $("audit-detail-modal").classList.add("hidden"));
  if ($("audit-detail-modal")) $("audit-detail-modal").addEventListener("click", (e) => { if (e.target === $("audit-detail-modal")) $("audit-detail-modal").classList.add("hidden"); });

  if ($("btn-audit-export")) {
    $("btn-audit-export").addEventListener("click", async () => {
      try {
        const resp = await fetch("/api/admin/audit/logs/export?" + buildAuditFilterParams(false).toString(), { credentials: "same-origin" });
        if (!resp.ok) throw new Error(t`请求失败（${resp.status}），请重试。`);
        const blob = await resp.blob();
        const filename = parseDownloadFilename(resp, "engram-audit-logs.json");
        if (await saveBlobToLocal(blob, filename)) {
          showToast(t`审计日志已导出为 ${filename}`);
        }
      } catch (err) {
        showToast(err.message, true);
      }
    });
  }

  if ($("btn-open-audit-cleanup")) {
    $("btn-open-audit-cleanup").addEventListener("click", () => {
      const cleanupTask = state.schedulerTasks.find((item) => item.name === "audit_log_cleanup");
      const defaultRet = (cleanupTask && cleanupTask.config && cleanupTask.config.retention) || (state.auditStats && state.auditStats.default_retention) || "30d";
      $("audit-cleanup-retention").value = defaultRet;
      $("audit-cleanup-modal").classList.remove("hidden");
    });
  }
  if ($("btn-close-audit-cleanup")) $("btn-close-audit-cleanup").addEventListener("click", () => $("audit-cleanup-modal").classList.add("hidden"));
  if ($("btn-cancel-audit-cleanup")) $("btn-cancel-audit-cleanup").addEventListener("click", () => $("audit-cleanup-modal").classList.add("hidden"));
  if ($("audit-cleanup-modal")) $("audit-cleanup-modal").addEventListener("click", (e) => { if (e.target === $("audit-cleanup-modal")) $("audit-cleanup-modal").classList.add("hidden"); });

  if ($("audit-cleanup-form")) {
    onSubmit("audit-cleanup-form", async (e) => {
      e.preventDefault();
      const retention = $("audit-cleanup-retention").value.trim();
      const res = await api("/api/admin/audit/cleanup", {
        method: "POST",
        body: JSON.stringify({ retention }),
      });
      $("audit-cleanup-modal").classList.add("hidden");
      showToast(t`已清理 ${res.deleted_count || 0} 条过期审计日志`);
      await loadAuditCenter();
    });
  }

  if ($("audit-task-definitions")) {
    $("audit-task-definitions").addEventListener("click", async (e) => {
      const btn = e.target.closest(".btn-run-audit-task");
      if (!btn) return;
      const taskType = btn.dataset.type;
      const tenantID = $("audit-task-tenant") ? $("audit-task-tenant").value : "";
      btn.disabled = true;
      btn.setAttribute("aria-busy", "true");
      try {
        const run = await api("/api/admin/audit/tasks/run", {
          method: "POST",
          body: JSON.stringify({ task_type: taskType, tenant_id: tenantID }),
        });
        renderAuditTaskFindings(run);
        showToast(run.summary || t("审计任务执行完成"));
        await Promise.all([loadAuditTasks(), loadAuditStats(), loadAuditLogs()]);
      } catch (err) {
        showToast(err.message, true);
      } finally {
        btn.disabled = false;
        btn.removeAttribute("aria-busy");
      }
    });
  }

  if ($("audit-task-runs-table")) {
    $("audit-task-runs-table").addEventListener("click", (e) => {
      const btn = e.target.closest(".btn-view-audit-run");
      if (!btn) return;
      const run = state.auditTaskRuns.find((item) => item.id === btn.dataset.id);
      if (run) renderAuditTaskFindings(run);
    });
  }
  if ($("btn-refresh-audit-tasks")) {
    $("btn-refresh-audit-tasks").addEventListener("click", async () => {
      try { await loadAuditTasks(); } catch (err) { showToast(err.message, true); }
    });
  }

  if ($("btn-refresh-scheduler")) {
    $("btn-refresh-scheduler").addEventListener("click", async () => {
      try { await loadSchedulerCenter(); } catch (err) { showToast(err.message, true); }
    });
  }
  if ($("scheduler-tasks-table")) {
    $("scheduler-tasks-table").addEventListener("click", async (e) => {
      const toggleBtn = e.target.closest(".btn-sched-toggle");
      if (toggleBtn) {
        const taskName = toggleBtn.dataset.name;
        const currentlyEnabled = toggleBtn.dataset.enabled === "true";
        toggleBtn.disabled = true;
        try {
          await api("/api/admin/scheduler/tasks", {
            method: "PATCH",
            body: JSON.stringify({ name: taskName, enabled: !currentlyEnabled }),
          });
          showToast(currentlyEnabled ? t("定时任务已停用") : t("定时任务已启用"));
          await loadSchedulerCenter();
        } catch (err) {
          showToast(err.message, true);
        } finally {
          toggleBtn.disabled = false;
        }
        return;
      }
      const cfgBtn = e.target.closest(".btn-sched-config");
      if (cfgBtn) {
        openSchedulerConfigModal(cfgBtn.dataset.name);
        return;
      }
      const runBtn = e.target.closest(".btn-sched-run");
      if (!runBtn) return;
      const taskName = runBtn.dataset.name;
      runBtn.disabled = true;
      runBtn.setAttribute("aria-busy", "true");
      try {
        const res = await api("/api/admin/scheduler/tasks/run", {
          method: "POST",
          body: JSON.stringify({ name: taskName }),
        });
        showToast((res.run && res.run.summary) || t("定时任务已执行完成"));
        await loadSchedulerCenter();
        if (activeTab() === "tab-audit") await loadAuditCenter();
      } catch (err) {
        showToast(err.message, true);
      } finally {
        runBtn.disabled = false;
        runBtn.removeAttribute("aria-busy");
      }
    });
  }

  if ($("sched-cfg-mig-mode")) {
    $("sched-cfg-mig-mode").addEventListener("change", () => {
      if ($("sched-cfg-include-deleted-group")) {
        $("sched-cfg-include-deleted-group").classList.toggle("hidden", $("sched-cfg-mig-mode").value !== "knowledge");
      }
    });
  }

  if ($("btn-close-scheduler-config")) $("btn-close-scheduler-config").addEventListener("click", () => $("scheduler-config-modal").classList.add("hidden"));
  if ($("btn-cancel-scheduler-config")) $("btn-cancel-scheduler-config").addEventListener("click", () => $("scheduler-config-modal").classList.add("hidden"));
  if ($("scheduler-config-modal")) $("scheduler-config-modal").addEventListener("click", (e) => { if (e.target === $("scheduler-config-modal")) $("scheduler-config-modal").classList.add("hidden"); });

  if ($("scheduler-config-form")) {
    onSubmit("scheduler-config-form", async (e) => {
      e.preventDefault();
      const name = $("sched-cfg-name").value;
      const payload = {
        name,
        enabled: $("sched-cfg-enabled").checked,
        interval: $("sched-cfg-interval").value.trim(),
      };
      if (name === "audit_log_cleanup") {
        payload.config = {
          retention: $("sched-cfg-retention").value.trim(),
          batch_size: Number($("sched-cfg-batch-size").value) || 1000,
        };
      } else if (name === "safekeep_migration_backup") {
        payload.config = {
          migration_mode: $("sched-cfg-mig-mode").value,
          include_deleted: $("sched-cfg-include-deleted").checked,
          upload_mode: $("sched-cfg-upload-mode").value,
          remote_path: $("sched-cfg-remote-path").value.trim(),
          targets: $("sched-cfg-targets").value.trim(),
        };
      }
      await api("/api/admin/scheduler/tasks", {
        method: "PATCH",
        body: JSON.stringify(payload),
      });
      $("scheduler-config-modal").classList.add("hidden");
      showToast(t("定时任务配置已保存"));
      await loadSchedulerCenter();
    });
  }

  // SafeKeep Integration Settings & Backup
  async function loadSafekeepSettings() {
    if (!$("safekeep-settings-form")) return;
    const data = await api("/api/admin/integrations/safekeep");
    state.safekeepSettings = data;
    renderSafekeepSettings(data);
  }

  function renderSafekeepSettings(data) {
    if (!data || !$("safekeep-settings-form")) return;
    $("sk-base-url").value = data.base_url || "";
    $("sk-api-key").value = "";
    $("sk-backup-passphrase").value = "";
    $("sk-default-mode").value = data.default_mode || "sync";
    $("sk-default-path").value = data.default_path || "engram";
    $("sk-default-targets").value = data.default_targets || "";

    const badge = $("sk-status-badge");
    if (badge) {
      const configured = !!data.base_url && !!data.has_api_key;
      badge.textContent = configured ? t("已配置") : t("未配置");
      badge.className = "badge " + (configured ? "success" : "");
    }
    if ($("sk-api-key-hint")) {
      $("sk-api-key-hint").textContent = data.has_api_key
        ? t`已保存 API Key（${data.api_key_masked || "****"}），留空则保持不变。`
        : t("尚未配置 API Key。");
    }
    if ($("btn-sk-clear-key")) $("btn-sk-clear-key").classList.toggle("hidden", !data.has_api_key);
    if ($("sk-passphrase-hint")) {
      $("sk-passphrase-hint").textContent = data.has_backup_passphrase
        ? t("已设置定时备份加密口令，留空则保持不变。")
        : t("未设置加密口令（全量系统快照建议设置口令以保留客户端密钥明文）。");
    }
    if ($("btn-sk-clear-passphrase")) $("btn-sk-clear-passphrase").classList.toggle("hidden", !data.has_backup_passphrase);

    const testResult = $("sk-test-result");
    if (testResult && data.last_tested_at) {
      if (data.last_test_status === "ok") {
        testResult.className = "check-result ok";
        testResult.textContent = t`最近连通测试成功（${formatDate(data.last_tested_at)}）`;
      } else if (data.last_test_status === "failed") {
        testResult.className = "check-result error";
        testResult.textContent = t`最近连通测试失败：${data.last_test_error || ""}`;
      }
    }
  }

  if ($("safekeep-settings-form")) {
    onSubmit("safekeep-settings-form", async (e) => {
      e.preventDefault();
      const payload = {
        base_url: $("sk-base-url").value.trim(),
        default_mode: $("sk-default-mode").value,
        default_path: $("sk-default-path").value.trim(),
        default_targets: $("sk-default-targets").value.trim(),
      };
      const apiKey = $("sk-api-key").value.trim();
      if (apiKey) payload.api_key = apiKey;
      const passphrase = $("sk-backup-passphrase").value;
      if (passphrase) payload.backup_passphrase = passphrase;

      const updated = await api("/api/admin/integrations/safekeep", {
        method: "PUT",
        body: JSON.stringify(payload),
      });
      state.safekeepSettings = updated;
      renderSafekeepSettings(updated);
      showToast(t("SafeKeep 配置已保存"));
    });
  }

  if ($("btn-sk-clear-key")) {
    $("btn-sk-clear-key").addEventListener("click", async () => {
      try {
        const updated = await api("/api/admin/integrations/safekeep", {
          method: "PUT",
          body: JSON.stringify({ clear_api_key: true }),
        });
        state.safekeepSettings = updated;
        renderSafekeepSettings(updated);
        showToast(t("已清除 SafeKeep API Key"));
      } catch (err) {
        showToast(err.message, true);
      }
    });
  }

  if ($("btn-sk-clear-passphrase")) {
    $("btn-sk-clear-passphrase").addEventListener("click", async () => {
      try {
        const updated = await api("/api/admin/integrations/safekeep", {
          method: "PUT",
          body: JSON.stringify({ clear_backup_passphrase: true }),
        });
        state.safekeepSettings = updated;
        renderSafekeepSettings(updated);
        showToast(t("已清除定时备份加密口令"));
      } catch (err) {
        showToast(err.message, true);
      }
    });
  }

  if ($("btn-sk-test")) {
    $("btn-sk-test").addEventListener("click", async () => {
      const resultEl = $("sk-test-result");
      const btn = $("btn-sk-test");
      btn.disabled = true;
      btn.setAttribute("aria-busy", "true");
      if (resultEl) {
        resultEl.className = "check-result";
        resultEl.textContent = t("正在测试连接…");
      }
      try {
        const res = await api("/api/admin/integrations/safekeep/test", {
          method: "POST",
          body: JSON.stringify({
            base_url: $("sk-base-url").value.trim(),
            api_key: $("sk-api-key").value.trim(),
          }),
        });
        if (resultEl) {
          resultEl.className = "check-result ok";
          const names = (res.upstreams || []).map((u) => u.name).filter(Boolean).join(", ");
          resultEl.textContent = names ? `${res.message} (${names})` : res.message;
        }
        showToast(res.message || t("SafeKeep 连接成功"));
      } catch (err) {
        if (resultEl) {
          resultEl.className = "check-result error";
          resultEl.textContent = err.message;
        }
        showToast(err.message, true);
      } finally {
        btn.disabled = false;
        btn.removeAttribute("aria-busy");
      }
    });
  }

  async function runManualSafekeepBackup(opts = {}) {
    const res = await api("/api/admin/integrations/safekeep/backup", {
      method: "POST",
      body: JSON.stringify(opts),
    });
    return res;
  }

  if ($("safekeep-backup-now-form")) {
    onSubmit("safekeep-backup-now-form", async (e) => {
      e.preventDefault();
      const res = await runManualSafekeepBackup({
        mode: $("sk-now-mode").value,
        path: $("sk-now-path").value.trim(),
        include_deleted: true,
      });
      const box = $("sk-backup-result");
      if (box) {
        const sk = res.safekeep || {};
        box.innerHTML = `<div class="mig-inspect-header"><span class="badge success">${escapeHtml(sk.status || "ok")}</span><code>${escapeHtml(res.filename)}</code></div><p class="desc" style="margin-bottom:0;">${escapeHtml(t`已上传 ${res.memories || 0} 条知识（${res.file_size || 0} 字节）至 SafeKeep 任务 ${sk.task_id || ""}`)}</p>`;
        box.classList.remove("hidden");
      }
      showToast(t("已成功导出迁移包并备份至 SafeKeep"));
    });
  }

  if ($("btn-mig-export-safekeep")) {
    $("btn-mig-export-safekeep").addEventListener("click", async () => {
      const btn = $("btn-mig-export-safekeep");
      btn.disabled = true;
      btn.setAttribute("aria-busy", "true");
      try {
        const mode = selectedMigrationExportMode();
        const passphrase = $("mig-export-passphrase") ? $("mig-export-passphrase").value : "";
        const includeDeleted = $("mig-export-include-deleted") ? $("mig-export-include-deleted").checked : true;
        const res = await runManualSafekeepBackup({
          mode,
          passphrase,
          include_deleted: includeDeleted,
        });
        const sk = res.safekeep || {};
        showToast(t`已将迁移包 ${res.filename} 上传至 SafeKeep（任务 ${sk.task_id || ""}）`);
      } catch (err) {
        showToast(err.message, true);
      } finally {
        btn.disabled = false;
        btn.removeAttribute("aria-busy");
      }
    });
  }

  if ($("scheduler-runs-filter")) {
    $("scheduler-runs-filter").addEventListener("change", async () => {
      state.schedulerRunsPage = 1;
      try { await loadSchedulerRuns(); } catch (err) { showToast(err.message, true); }
    });
  }
  if ($("btn-sched-runs-prev")) {
    $("btn-sched-runs-prev").addEventListener("click", async () => {
      if (state.schedulerRunsPage <= 1) return;
      state.schedulerRunsPage--;
      try { await loadSchedulerRuns(); } catch (err) { showToast(err.message, true); }
    });
  }
  if ($("btn-sched-runs-next")) {
    $("btn-sched-runs-next").addEventListener("click", async () => {
      state.schedulerRunsPage++;
      try { await loadSchedulerRuns(); } catch (err) { showToast(err.message, true); }
    });
  }

  if ($("btn-mobile-menu")) $("btn-mobile-menu").addEventListener("click", () => setSidebarOpen(true));
  if ($("btn-close-sidebar")) $("btn-close-sidebar").addEventListener("click", () => setSidebarOpen(false));
  if ($("sidebar-backdrop")) $("sidebar-backdrop").addEventListener("click", () => setSidebarOpen(false));
  if ($("btn-sidebar-logout") && $("btn-logout")) {
    $("btn-sidebar-logout").addEventListener("click", () => $("btn-logout").click());
  }

  renderClientGuides();
  checkSession();
})();
