(function () {
  const state = {
    tenants: [],
    principals: [],
    projects: [],
    tokens: [],
    memories: [],
    latestToken: "",
    revealedTokens: {},
  };

  function $(id) {
    return document.getElementById(id);
  }

  function showToast(msg, isError) {
    const el = $("toast");
    el.textContent = msg;
    el.className = isError ? "toast error" : "toast";
    setTimeout(() => {
      el.classList.add("hidden");
    }, 3800);
  }

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
      const msg = (data && (data.message || data.error)) || `HTTP ${resp.status}`;
      throw new Error(msg);
    }
    return data;
  }

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

  async function refreshAllData() {
    try {
      const [overview, tenants, principals, projects, tokens] = await Promise.all([
        api("/api/admin/overview"),
        api("/api/admin/tenants"),
        api("/api/admin/principals"),
        api("/api/admin/projects"),
        api("/api/admin/tokens"),
      ]);
      state.tenants = tenants.items || [];
      state.principals = principals.items || [];
      state.projects = projects.items || [];
      state.tokens = tokens.items || [];

      $("stat-tenants").textContent = overview.tenants_count || 0;
      $("stat-principals").textContent = overview.principals_count || 0;
      $("stat-projects").textContent = overview.projects_count || 0;
      $("stat-memories").textContent = overview.active_memories_count || 0;
      $("stat-deleted").textContent = overview.deleted_memories_count || 0;
      $("stat-tokens").textContent = overview.active_tokens_count || 0;
      $("stat-events").textContent = overview.events_count || 0;

      renderSelectors();
      renderTenantsTable();
      renderPrincipalsTable();
      renderProjectsTable();
      renderTokensTable();
      await loadMemories();
    } catch (err) {
      showToast(err.message, true);
    }
  }

  function renderSelectors() {
    const tenantSelects = ["prin-tenant", "proj-tenant", "tok-tenant"];
    tenantSelects.forEach((id) => {
      const sel = $(id);
      if (!sel) return;
      const prev = sel.value;
      sel.innerHTML = state.tenants
        .map((t) => `<option value="${t.id}">${escapeHtml(t.name)} (${t.id.slice(0, 8)})</option>`)
        .join("");
      if (prev) sel.value = prev;
    });

    const memTenantSel = $("mem-filter-tenant");
    const prevMemTenant = memTenantSel.value;
    memTenantSel.innerHTML =
      `<option value="">全部租户</option>` +
      state.tenants.map((t) => `<option value="${t.id}">${escapeHtml(t.name)} (${t.id.slice(0, 8)})</option>`).join("");
    memTenantSel.value = prevMemTenant;

    const prinSelects = ["pm-principal", "tok-principal"];
    prinSelects.forEach((id) => {
      const sel = $(id);
      if (!sel) return;
      const prev = sel.value;
      sel.innerHTML = state.principals
        .map((p) => `<option value="${p.id}">${escapeHtml(p.display_name)} [${escapeHtml(p.subject)}]</option>`)
        .join("");
      if (prev) sel.value = prev;
    });

    const projSelects = ["pm-project", "pr-project"];
    projSelects.forEach((id) => {
      const sel = $(id);
      if (!sel) return;
      const prev = sel.value;
      sel.innerHTML = state.projects
        .map((p) => `<option value="${p.id}">${escapeHtml(p.name)} (${escapeHtml(p.tenant_name)})</option>`)
        .join("");
      if (prev) sel.value = prev;
    });
  }

  function renderTenantsTable() {
    const tbody = $("tenants-table").querySelector("tbody");
    tbody.innerHTML = state.tenants
      .map(
        (t) => `
      <tr>
        <td><code>${t.id}</code></td>
        <td><strong>${escapeHtml(t.name)}</strong></td>
        <td><span class="badge ${t.status === "active" ? "success" : "danger"}">${t.status}</span></td>
        <td>${new Date(t.created_at).toLocaleString()}</td>
      </tr>
    `
      )
      .join("");
  }

  function renderPrincipalsTable() {
    const tbody = $("principals-table").querySelector("tbody");
    tbody.innerHTML = state.principals
      .map((p) => {
        const roles = (p.tenant_roles || [])
          .map((r) => `<span class="badge">${escapeHtml(r.tenant_name)}: ${escapeHtml(r.role)}</span>`)
          .join(" ");
        return `
      <tr>
        <td><code>${p.id}</code></td>
        <td><strong>${escapeHtml(p.display_name)}</strong></td>
        <td><code>${escapeHtml(p.identity_provider)} / ${escapeHtml(p.subject)}</code></td>
        <td><span class="badge ${p.status === "active" ? "success" : "danger"}">${p.status}</span></td>
        <td>${roles || '<span style="color:var(--text-muted)">无</span>'}</td>
      </tr>`;
      })
      .join("");
  }

  function renderProjectsTable() {
    const tbody = $("projects-table").querySelector("tbody");
    tbody.innerHTML = state.projects
      .map((p) => {
        const markerHtml = p.marker
          ? `<code>${p.marker}</code>`
          : `<span style="color:var(--text-muted)">未设置</span>`;
        const reposHtml = (p.repositories || [])
          .map(
            (r) =>
              `<div><code>${escapeHtml(r.canonical_remote)}</code> ${
                r.workspace_path ? `<span class="badge">ws: ${escapeHtml(r.workspace_path)}</span>` : ""
              }</div>`
          )
          .join("");
        const membersHtml = (p.members || [])
          .map((m) => `<span class="badge">${escapeHtml(m.display_name)}: ${escapeHtml(m.role)}</span>`)
          .join(" ");
        return `
      <tr>
        <td><strong>${escapeHtml(p.name)}</strong><br/><code>${p.id}</code></td>
        <td>${escapeHtml(p.tenant_name)}</td>
        <td>${markerHtml}</td>
        <td>${reposHtml || '<span style="color:var(--text-muted)">无 Remote</span>'}</td>
        <td>${membersHtml || '<span style="color:var(--text-muted)">仅 Tenant Admin</span>'}</td>
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
    throw new Error("未获取到 Token 明文");
  }

  function renderTokensTable() {
    const tbody = $("tokens-table").querySelector("tbody");
    tbody.innerHTML = state.tokens
      .map((tk) => {
        const active = !tk.revoked_at;
        const revealed = state.revealedTokens[tk.id];
        const tokenCell = revealed
          ? `<code>${escapeHtml(revealed)}</code>`
          : `<code>${escapeHtml(tk.token_prefix)}...</code>${
              tk.can_reveal ? "" : ' <small style="color:var(--text-muted)">(不可还原)</small>'
            }`;
        const revealBtns = tk.can_reveal
          ? `
            <button type="button" class="secondary small btn-toggle-token" data-id="${tk.id}">${revealed ? "隐藏" : "查看"}</button>
            <button type="button" class="secondary small btn-copy-token" data-id="${tk.id}">复制</button>
            <button type="button" class="secondary small btn-apply-token" data-id="${tk.id}">填入配置</button>
          `
          : "";
        return `
      <tr>
        <td><strong>${escapeHtml(tk.name)}</strong></td>
        <td>${tokenCell}</td>
        <td>${escapeHtml(tk.tenant_name)}</td>
        <td>${escapeHtml(tk.principal_name)}</td>
        <td><span class="badge ${active ? "success" : "danger"}">${active ? "active" : "revoked"}</span></td>
        <td>${new Date(tk.created_at).toLocaleString()}</td>
        <td>
          <div style="display:flex; gap:6px; flex-wrap:wrap;">
            ${revealBtns}
            ${
              active
                ? `<button type="button" class="danger small btn-revoke-token" data-id="${tk.id}">吊销</button>`
                : `<span style="color:var(--text-muted)">已吊销</span>`
            }
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
          if (card) card.scrollIntoView({ behavior: "smooth", block: "start" });
          showToast("已将 Token 填入客户端接入配置");
        } catch (err) {
          showToast(err.message, true);
        }
      });
    });

    tbody.querySelectorAll(".btn-revoke-token").forEach((btn) => {
      btn.addEventListener("click", async () => {
        try {
          await api("/api/admin/tokens/revoke", {
            method: "POST",
            body: JSON.stringify({ id: btn.dataset.id }),
          });
          showToast("API Token 已吊销");
          refreshAllData();
        } catch (err) {
          showToast(err.message, true);
        }
      });
    });
  }

  async function loadMemories() {
    const tenantId = $("mem-filter-tenant").value;
    const scope = $("mem-filter-scope").value;
    const deletedMode = $("mem-filter-deleted").value;
    const q = $("mem-filter-q").value;

    const params = new URLSearchParams();
    if (tenantId) params.set("tenant_id", tenantId);
    if (scope) params.set("scope", scope);
    if (deletedMode) params.set("deleted", deletedMode);
    if (q) params.set("q", q);

    const res = await api("/api/admin/memories?" + params.toString());
    state.memories = res.items || [];
    const tbody = $("memories-table").querySelector("tbody");
    tbody.innerHTML = state.memories
      .map((m) => {
        const isDeleted = !!m.deleted_at;
        return `
      <tr>
        <td>
          <strong>${escapeHtml(m.title)}</strong>
          ${m.is_pinned ? `<span class="badge warning">置顶</span>` : ""}
          <br/><code>${m.id}</code>
        </td>
        <td>
          <span class="badge">${escapeHtml(m.scope)}</span>
          ${m.project_name ? `<div>${escapeHtml(m.project_name)}</div>` : ""}
        </td>
        <td>
          <code>${escapeHtml(m.category)}</code><br/>
          <small style="color:var(--text-muted)">${(m.tags || []).map(escapeHtml).join(", ")}</small>
        </td>
        <td>
          v${m.version}
          <span class="badge ${isDeleted ? "danger" : "success"}">${isDeleted ? "deleted" : m.status}</span>
        </td>
        <td>${new Date(m.updated_at).toLocaleString()}</td>
        <td>
          <button class="secondary small btn-view-mem" data-id="${m.id}">查看/历史</button>
          ${
            isDeleted
              ? `<button class="small btn-restore-mem" data-id="${m.id}" data-tenant="${m.tenant_id}" data-ver="${m.version}">恢复</button>`
              : ""
          }
        </td>
      </tr>`;
      })
      .join("");

    tbody.querySelectorAll(".btn-view-mem").forEach((btn) => {
      btn.addEventListener("click", () => viewMemoryDetail(btn.dataset.id));
    });
    tbody.querySelectorAll(".btn-restore-mem").forEach((btn) => {
      btn.addEventListener("click", () =>
        restoreMemory(btn.dataset.id, btn.dataset.tenant, parseInt(btn.dataset.ver, 10))
      );
    });
  }

  async function viewMemoryDetail(memId) {
    const mem = state.memories.find((x) => x.id === memId);
    if (!mem) return;
    try {
      const revRes = await api(`/api/admin/memories/revisions?memory_id=${encodeURIComponent(memId)}`);
      $("detail-title").textContent = `${mem.title} (v${mem.version})`;
      $("detail-content").textContent = mem.content_md || "";
      $("detail-revisions").textContent = JSON.stringify(revRes.items || [], null, 2);
      $("memory-detail-card").classList.remove("hidden");
    } catch (err) {
      showToast(err.message, true);
    }
  }

  async function restoreMemory(memId, tenantId, expectedVersion) {
    try {
      await api("/api/admin/memories/restore", {
        method: "POST",
        body: JSON.stringify({
          tenant_id: tenantId,
          memory_id: memId,
          expected_version: expectedVersion,
        }),
      });
      showToast("知识条目已成功恢复！");
      await refreshAllData();
    } catch (err) {
      showToast(err.message, true);
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
      "## Engram 共享记忆使用规范",
      "",
      "如果当前会话已接入 `engram` MCP 工具，请按以下流程使用：",
      "",
      "1. **会话开始先识别项目**：",
      "   - 获取当前仓库的 `git remote -v`（以及根目录或子目录 `.engram-project` 文件中的 UUID，如有），调用 `memory_discover` 获取 `project_context` 与范围索引。",
      "   - 注意：`workspace_path` 仅用于 Monorepo 子目录（相对于 Git 根目录），**不要传入本地绝对路径**；若当前不在 Git 仓库中且仅需全局记忆，传入 `allow_global_only: true`。",
      "   - 同一会话内复用返回的 `project_context`，无需重复调用 `memory_discover`。",
      "2. **先查索引，再按需检索与读取**：",
      "   - 先浏览 `memory_discover` 返回的 `pinned`、`category_stats` 和 `recent`，若已命中目标可直接调用 `memory_read`。",
      "   - 需要按关键词、分类或标签检索时调用 `memory_query`，定位到 `memory_id` 后再调用 `memory_read` 读取完整 Markdown 正文。",
      "3. **任务过程中及时沉淀与更新**：",
      "   - 遇到有长期价值的架构约定、环境配置、排障结论或跨项目规范时，调用 `memory_create` / `memory_update` 沉淀或更新记忆（更新需传 `expected_version` 与唯一 `idempotency_key`），**严禁写入密码、Token、私钥等敏感凭据**。",
    ].join("\n");
  }

  function renderClientGuides() {
    const tokenInput = $("guide-token-input");
    const rawToken = ((tokenInput && tokenInput.value.trim()) || state.latestToken || "<YOUR_API_TOKEN>").trim();
    const mcpUrl = `${window.location.origin}/mcp`;

    const codexToml = [
      "[mcp_servers.engram]",
      "enabled = true",
      `url = "${mcpUrl}"`,
      `http_headers = { Authorization = "Bearer ${rawToken}" }`,
      "",
      "# 若仅在命令行 CLI 使用，也可改用环境变量方式：",
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

    const claudeCli = `claude mcp add --transport http --scope user engram "${mcpUrl}" --header "Authorization: Bearer ${rawToken}"`;

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

  function switchMainTab(tabId) {
    document.querySelectorAll(".tab-btn").forEach((b) => {
      b.classList.toggle("active", b.dataset.tab === tabId);
    });
    document.querySelectorAll(".tab-pane").forEach((p) => {
      p.classList.toggle("hidden", p.id !== tabId);
    });
  }

  async function copyText(text) {
    try {
      if (navigator.clipboard && navigator.clipboard.writeText) {
        await navigator.clipboard.writeText(text);
        showToast("已复制到剪贴板");
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
      showToast("已复制到剪贴板");
    } catch (_) {
      showToast("复制失败，请手动选中复制", true);
    } finally {
      document.body.removeChild(ta);
    }
  }

  function escapeHtml(s) {
    return String(s || "")
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  // Event Listeners
  document.querySelectorAll(".tab-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      switchMainTab(btn.dataset.tab);
    });
  });

  document.querySelectorAll(".client-tab-btn").forEach((btn) => {
    btn.addEventListener("click", () => {
      const target = btn.dataset.client;
      document.querySelectorAll(".client-tab-btn").forEach((b) => b.classList.toggle("active", b === btn));
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

  if ($("btn-go-client-guides")) {
    $("btn-go-client-guides").addEventListener("click", () => {
      switchMainTab("tab-tokens");
      const card = $("client-guides-card");
      if (card) card.scrollIntoView({ behavior: "smooth", block: "start" });
    });
  }

  $("login-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      const s = await api("/api/admin/login", {
        method: "POST",
        body: JSON.stringify({
          username: $("login-username").value,
          password: $("login-password").value,
        }),
      });
      $("login-password").value = "";
      renderView(s);
    } catch (err) {
      showToast(err.message, true);
    }
  });

  $("force-password-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const curPwd = $("current-admin-password").value;
    const p1 = $("new-admin-password").value;
    const p2 = $("confirm-admin-password").value;
    if (p1 !== p2) {
      showToast("两次输入的密码不一致", true);
      return;
    }
    try {
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
      showToast("管理员密码已更新！");
      renderView(s);
    } catch (err) {
      showToast(err.message, true);
    }
  });

  $("btn-open-change-pwd").addEventListener("click", () => {
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
    renderView({ authenticated: false });
  });

  $("quickstart-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
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
      $("qs-result").classList.remove("hidden");
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
      showToast("一键初始化完成！");
      refreshAllData();
    } catch (err) {
      showToast(err.message, true);
    }
  });

  $("create-tenant-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      await api("/api/admin/tenants", {
        method: "POST",
        body: JSON.stringify({ name: $("tenant-name").value }),
      });
      $("tenant-name").value = "";
      showToast("Tenant 创建成功");
      refreshAllData();
    } catch (err) {
      showToast(err.message, true);
    }
  });

  $("create-principal-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      await api("/api/admin/principals", {
        method: "POST",
        body: JSON.stringify({
          tenant_id: $("prin-tenant").value,
          identity_provider: $("prin-provider").value,
          subject: $("prin-subject").value,
          display_name: $("prin-name").value,
          tenant_role: $("prin-role").value,
        }),
      });
      $("prin-subject").value = "";
      $("prin-name").value = "";
      showToast("Principal 创建并绑定成功");
      refreshAllData();
    } catch (err) {
      showToast(err.message, true);
    }
  });

  $("create-project-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
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
      showToast("项目注册成功");
      refreshAllData();
    } catch (err) {
      showToast(err.message, true);
    }
  });

  $("add-project-member-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
      await api("/api/admin/projects/members", {
        method: "POST",
        body: JSON.stringify({
          project_id: $("pm-project").value,
          principal_id: $("pm-principal").value,
          role: $("pm-role").value,
        }),
      });
      showToast("项目成员角色已更新");
      refreshAllData();
    } catch (err) {
      showToast(err.message, true);
    }
  });

  $("add-project-remote-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
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
      showToast("仓库 Remote 映射已添加");
      refreshAllData();
    } catch (err) {
      showToast(err.message, true);
    }
  });

  $("create-token-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    try {
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
      showToast("API Token 已签发");
      refreshAllData();
    } catch (err) {
      showToast(err.message, true);
    }
  });

  $("filter-memories-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    await loadMemories();
  });

  $("btn-close-detail").addEventListener("click", () => {
    $("memory-detail-card").classList.add("hidden");
  });

  function toggleGlossaryModal(open) {
    const modal = $("glossary-modal");
    if (!modal) return;
    if (open) {
      modal.classList.remove("hidden");
    } else {
      modal.classList.add("hidden");
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
    if (e.key === "Escape" && !$("glossary-modal").classList.contains("hidden")) {
      toggleGlossaryModal(false);
    }
  });

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

        if (typeof window.showSaveFilePicker === "function") {
          try {
            const handle = await window.showSaveFilePicker({
              suggestedName: "engram-markdown-export.zip",
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
            showToast(cnt ? `Markdown 导出已保存（共 ${cnt} 条记录）` : "Markdown 导出已保存");
            return;
          } catch (pickerErr) {
            if (pickerErr && pickerErr.name === "AbortError") {
              return;
            }
            // Fall back to standard browser download if picker cannot open.
          }
        }

        const url = URL.createObjectURL(blob);
        const a = document.createElement("a");
        a.href = url;
        a.download = "engram-markdown-export.zip";
        document.body.appendChild(a);
        a.click();
        document.body.removeChild(a);
        setTimeout(() => URL.revokeObjectURL(url), 1000);
        showToast(cnt ? `Markdown 压缩包下载已开始（共 ${cnt} 条记录）` : "Markdown 压缩包下载已开始");
      } catch (err) {
        showToast(err.message, true);
      }
    });
  }

  renderClientGuides();
  checkSession();
})();
