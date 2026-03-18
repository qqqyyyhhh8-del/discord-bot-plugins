const VIEW_TITLES = {
  overview: "总览",
  learning: "学习",
  persona: "人格",
  graph: "图谱",
  settings: "配置",
};

const GRAPH_COLORS = ["#111111", "#374151", "#4b5563", "#6b7280", "#9ca3af", "#b45309", "#164e63"];

const state = {
  token: "",
  bootstrap: null,
  scopeState: null,
  currentScope: "",
  currentView: "overview",
  graphType: "__all",
  graphFocus: "__all",
  selectedNode: "",
  toastTimer: null,
};

const els = {};

window.addEventListener("DOMContentLoaded", () => {
  bindElements();
  bindEvents();
  restoreToken();
  boot();
});

function bindElements() {
  [
    "nav-toggle",
    "sidebar-mask",
    "scope-select",
    "scope-caption",
    "web-status-dot",
    "web-status-text",
    "web-access-text",
    "view-title",
    "topbar-tag",
    "refresh-button",
    "learn-button",
    "auth-card",
    "auth-form",
    "token-input",
    "auth-error",
    "empty-state",
    "views",
    "scope-title",
    "scope-description",
    "metric-last-learned",
    "metric-web-url",
    "metric-grid",
    "highlights-list",
    "affinity-list",
    "recent-feed",
    "summary-scene",
    "summary-summary",
    "summary-style",
    "summary-slang",
    "summary-intent",
    "summary-mood",
    "summary-relationship",
    "episodes-list",
    "cards-list",
    "persona-active-name",
    "persona-active-origin",
    "persona-active-prompt",
    "persona-auto-prompt",
    "persona-saved-list",
    "graph-type-filter",
    "graph-focus-filter",
    "graph-canvas",
    "graph-empty",
    "graph-legend",
    "graph-detail",
    "config-form",
    "config-enabled",
    "config-batch-size",
    "config-target-ids",
    "settings-web-info",
    "settings-budget-info",
    "settings-web-hints",
    "copy-access-url",
    "rotate-token",
    "toast",
  ].forEach((id) => {
    els[id] = document.getElementById(id);
  });

  els.shell = document.querySelector(".app-shell");
  els.navItems = Array.from(document.querySelectorAll(".nav-item"));
  els.viewPanels = Array.from(document.querySelectorAll(".view"));
}

function bindEvents() {
  els["nav-toggle"].addEventListener("click", () => toggleSidebar(true));
  els["sidebar-mask"].addEventListener("click", () => toggleSidebar(false));
  els["scope-select"].addEventListener("change", async (event) => {
    state.currentScope = event.target.value;
    await loadScope(state.currentScope);
    toggleSidebar(false);
  });
  els["refresh-button"].addEventListener("click", async () => {
    await refreshAll();
  });
  els["learn-button"].addEventListener("click", async () => {
    if (!state.currentScope) {
      showToast("当前没有可学习的作用域。");
      return;
    }
    try {
      const nextState = await api(`/api/learn?scope=${encodeURIComponent(state.currentScope)}`, { method: "POST" });
      applyScopeState(nextState);
      showToast("已完成一次强制学习。");
    } catch (error) {
      handleError(error);
    }
  });
  els["auth-form"].addEventListener("submit", async (event) => {
    event.preventDefault();
    state.token = els["token-input"].value.trim();
    localStorage.setItem("self_learning_console_token", state.token);
    await boot();
  });
  els["config-form"].addEventListener("submit", async (event) => {
    event.preventDefault();
    if (!state.currentScope) {
      showToast("当前没有可保存的作用域。");
      return;
    }
    try {
      const payload = {
        enabled: els["config-enabled"].checked,
        batch_size: Number(els["config-batch-size"].value || 0),
        target_user_ids: splitLines(els["config-target-ids"].value),
      };
      const nextState = await api(`/api/config?scope=${encodeURIComponent(state.currentScope)}`, {
        method: "POST",
        body: payload,
      });
      applyScopeState(nextState);
      syncScopeItem(nextState.scope);
      renderBootstrap();
      showToast("学习配置已保存。");
    } catch (error) {
      handleError(error);
    }
  });
  els["copy-access-url"].addEventListener("click", async () => {
    const accessURL = state.scopeState?.web?.access_url;
    if (!accessURL) {
      showToast("当前没有可复制的完整入口。");
      return;
    }
    try {
      await navigator.clipboard.writeText(accessURL);
      showToast("完整入口已复制。");
    } catch (error) {
      showToast("复制失败，请手动复制。");
    }
  });
  els["rotate-token"].addEventListener("click", async () => {
    try {
      const web = await api("/api/token/rotate", { method: "POST" });
      syncTokenFromWeb(web);
      if (state.scopeState) {
        state.scopeState.web = web;
        renderWebStatus(web);
        renderSettings();
      }
      showToast("访问令牌已重置，后续请求会自动切换到新令牌。");
    } catch (error) {
      handleError(error);
    }
  });
  els["graph-type-filter"].addEventListener("change", () => {
    state.graphType = els["graph-type-filter"].value;
    renderGraph();
  });
  els["graph-focus-filter"].addEventListener("change", () => {
    state.graphFocus = els["graph-focus-filter"].value;
    state.selectedNode = els["graph-focus-filter"].value === "__all" ? "" : els["graph-focus-filter"].value;
    renderGraph();
  });
  els.navItems.forEach((button) => {
    button.addEventListener("click", () => {
      setView(button.dataset.view);
      toggleSidebar(false);
    });
  });
}

function restoreToken() {
  const url = new URL(window.location.href);
  const queryToken = url.searchParams.get("token");
  const savedToken = localStorage.getItem("self_learning_console_token");
  state.token = (queryToken || savedToken || "").trim();
  if (queryToken) {
    localStorage.setItem("self_learning_console_token", queryToken.trim());
    url.searchParams.delete("token");
    window.history.replaceState({}, "", `${url.pathname}${url.search}${url.hash}`);
  }
  els["token-input"].value = state.token;
}

async function boot() {
  if (!state.token) {
    showAuth("请输入访问令牌。");
    return;
  }
  try {
    await refreshAll();
  } catch (error) {
    if (String(error.message || error) === "UNAUTHORIZED") {
      localStorage.removeItem("self_learning_console_token");
      showAuth("令牌无效或已失效，请重新输入。");
      return;
    }
    handleError(error);
  }
}

async function refreshAll() {
  const bootstrap = await api("/api/bootstrap");
  state.bootstrap = bootstrap;
  if (!bootstrap.scopes || bootstrap.scopes.length === 0) {
    renderBootstrap();
    showEmpty();
    return;
  }
  const currentExists = bootstrap.scopes.some((item) => item.key === state.currentScope);
  state.currentScope = currentExists ? state.currentScope : bootstrap.scopes[0].key;
  renderBootstrap();
  await loadScope(state.currentScope);
  hideAuth();
}

async function loadScope(scopeKey) {
  if (!scopeKey) {
    showEmpty();
    return;
  }
  try {
    const scopeState = await api(`/api/state?scope=${encodeURIComponent(scopeKey)}`);
    applyScopeState(scopeState);
  } catch (error) {
    handleError(error);
  }
}

function applyScopeState(scopeState) {
  state.scopeState = scopeState;
  state.currentScope = scopeState.scope.key;
  syncScopeItem(scopeState.scope);
  renderBootstrap();
  renderScope();
  hideAuth();
  showViews();
}

function renderBootstrap() {
  const scopes = state.bootstrap?.scopes || [];
  els["scope-select"].innerHTML = scopes
    .map((scope) => `<option value="${escapeHTML(scope.key)}">${escapeHTML(scope.label)}</option>`)
    .join("");
  if (state.currentScope) {
    els["scope-select"].value = state.currentScope;
  }
  const activeScope = scopes.find((item) => item.key === state.currentScope);
  els["scope-caption"].textContent = activeScope
    ? `${activeScope.enabled ? "已启用" : "未启用"} · ${formatMeta(activeScope.event_count || activeScope.eventCount)} 条事件`
    : "尚未选择作用域。";
  renderWebStatus(state.scopeState?.web || state.bootstrap?.web);
}

function renderScope() {
  if (!state.scopeState) {
    showEmpty();
    return;
  }
  setView(state.currentView);
  showViews();
  renderOverview();
  renderLearning();
  renderPersona();
  renderGraphFilters();
  renderGraph();
  renderSettings();
}

function renderOverview() {
  const scope = state.scopeState.scope;
  const metrics = state.scopeState.metrics;
  const profile = state.scopeState.profile;
  els["scope-title"].textContent = scope.label;
  els["scope-description"].textContent = metrics.enabled
    ? "当前作用域已接管上下文与长期记忆。"
    : "当前作用域仍可查看历史学习数据，但自学习未启用。";
  els["metric-last-learned"].textContent = orFallback(metrics.last_learned_at, "尚未学习");
  els["metric-web-url"].textContent = compactURL(state.scopeState.web?.public_url || state.scopeState.web?.local_url);

  const metricCards = [
    ["状态", metrics.enabled ? "已启用" : "未启用"],
    ["事件数", formatMeta(metrics.event_count)],
    ["Episodes", formatMeta(metrics.episode_count)],
    ["Cards", formatMeta(metrics.card_count)],
    ["参与成员", formatMeta(metrics.participant_count)],
    ["关系边", formatMeta(metrics.relation_count)],
    ["目标用户", formatMeta(metrics.target_count)]
  ];
  els["metric-grid"].innerHTML = metricCards
    .map(([label, value]) => `<article class="metric-card"><span>${escapeHTML(label)}</span><strong>${escapeHTML(value)}</strong></article>`)
    .join("");

  els["highlights-list"].innerHTML = listOrEmpty(profile.highlights, "当前还没有学习重点。", (item) => {
    return `<div class="bullet-item">${escapeHTML(item)}</div>`;
  });
  els["affinity-list"].innerHTML = listOrEmpty(metrics.top_affinity, "当前还没有好感度数据。", (item) => {
    return `
      <div class="rank-item">
        <div>
          <strong>${escapeHTML(item.label || item.user_id)}</strong>
          <div class="tiny-copy">${escapeHTML(item.user_id)}</div>
        </div>
        <strong>${escapeHTML(String(item.score))}/100</strong>
      </div>
    `;
  });

  const recent = [...(state.scopeState.recent_events || [])].reverse();
  els["recent-feed"].innerHTML = listOrEmpty(recent, "当前还没有最近事件。", (event) => {
    const flags = [];
    if (event.mentioned_bot) flags.push("提及 Bot");
    if (event.replied_to_bot) flags.push("回复 Bot");
    if (event.image_count) flags.push(`${event.image_count} 张图片`);
    return `
      <div class="timeline-item">
        <small>${escapeHTML(orFallback(event.time, "未知时间"))} · ${escapeHTML(event.author_label)}</small>
        <div>${escapeHTML(orFallback(event.content, "[空消息]"))}</div>
        ${event.reply_label ? `<div class="tiny-copy">回复 ${escapeHTML(event.reply_label)}: ${escapeHTML(orFallback(event.reply_content, "[空消息]"))}</div>` : ""}
        ${flags.length ? `<div class="tiny-copy">${escapeHTML(flags.join(" · "))}</div>` : ""}
      </div>
    `;
  });
}

function renderLearning() {
  const profile = state.scopeState.profile;
  setProse("summary-scene", profile.scene_summary, "当前还没有场景摘要。");
  setProse("summary-summary", profile.summary, "当前还没有长期摘要。");
  setProse("summary-style", profile.style_summary, "当前还没有风格总结。");
  setProse("summary-slang", profile.slang_summary, "当前还没有黑话总结。");
  setProse("summary-intent", profile.intent_summary, "当前还没有意图总结。");
  setProse("summary-mood", profile.mood_summary, "当前还没有情绪总结。");
  setProse("summary-relationship", profile.relationship_summary, "当前还没有关系综述。");
  els["episodes-list"].innerHTML = listOrEmpty(state.scopeState.episodes, "当前还没有历史片段。", (episode) => {
    const highlights = (episode.highlights || []).slice(0, 2).join("；");
    return `
      <div class="bullet-item">
        <strong>${escapeHTML(orFallback(episode.scene_summary, episode.ended_at || "未命名片段"))}</strong>
        <div class="tiny-copy">${escapeHTML(orFallback(episode.started_at, "未知开始"))} → ${escapeHTML(orFallback(episode.ended_at, "未知结束"))}</div>
        <div>${escapeHTML(orFallback(episode.summary, "当前没有片段摘要。"))}</div>
        ${highlights ? `<div class="tiny-copy">重点: ${escapeHTML(highlights)}</div>` : ""}
      </div>
    `;
  });
  els["cards-list"].innerHTML = listOrEmpty(state.scopeState.cards, "当前还没有结构化记忆卡片。", (card) => {
    const evidence = (card.evidence || []).slice(0, 1)[0];
    const subject = [card.subject_name, card.subject_id].filter(Boolean).join(" / ");
    return `
      <div class="bullet-item">
        <strong>[${escapeHTML(orFallback(card.kind, "fact"))}] ${escapeHTML(card.title)}</strong>
        ${subject ? `<div class="tiny-copy">${escapeHTML(subject)}</div>` : ""}
        <div>${escapeHTML(card.content)}</div>
        <div class="tiny-copy">置信度 ${escapeHTML(formatConfidence(card.confidence))}</div>
        ${evidence ? `<div class="tiny-copy">证据: ${escapeHTML(evidence)}</div>` : ""}
      </div>
    `;
  });
}

function renderPersona() {
  const persona = state.scopeState.persona || {};
  const profile = state.scopeState.profile || {};
  els["persona-active-name"].textContent = orFallback(persona.active_name, "未启用人格");
  els["persona-active-origin"].textContent = persona.auto_active ? "自动人格生效中" : orFallback(persona.active_origin, "未标注来源");
  setProse("persona-active-prompt", persona.active_prompt, "当前没有启用中的人格 Prompt。");
  setProse("persona-auto-prompt", profile.persona_prompt, "当前还没有自动生成人格 Prompt。");
  els["persona-saved-list"].innerHTML = listOrEmpty(persona.saved, "当前作用域还没有保存的人格。", (entry) => {
    return `
      <div class="persona-item">
        <div>
          <strong>${escapeHTML(entry.name)}</strong>
          <div class="tiny-copy">${escapeHTML(orFallback(entry.origin, "unknown"))}</div>
        </div>
        <div class="tiny-copy">${escapeHTML(orFallback(entry.updated_at, "未记录时间"))}</div>
      </div>
    `;
  });
}

function renderGraphFilters() {
  const graph = state.scopeState.graph || { legend: [], nodes: [] };
  const typeOptions = ['<option value="__all">全部关系</option>']
    .concat((graph.legend || []).map((item) => `<option value="${escapeHTML(item)}">${escapeHTML(item)}</option>`));
  els["graph-type-filter"].innerHTML = typeOptions.join("");
  if (![...els["graph-type-filter"].options].some((option) => option.value === state.graphType)) {
    state.graphType = "__all";
  }
  els["graph-type-filter"].value = state.graphType;

  const focusOptions = ['<option value="__all">全部成员</option>']
    .concat((graph.nodes || []).map((node) => `<option value="${escapeHTML(node.id)}">${escapeHTML(node.label)}</option>`));
  els["graph-focus-filter"].innerHTML = focusOptions.join("");
  if (![...els["graph-focus-filter"].options].some((option) => option.value === state.graphFocus)) {
    state.graphFocus = "__all";
  }
  els["graph-focus-filter"].value = state.graphFocus;
}

function renderGraph() {
  const graph = state.scopeState.graph || { nodes: [], edges: [] };
  const nodesById = new Map((graph.nodes || []).map((node) => [node.id, node]));
  let edges = [...(graph.edges || [])];
  if (state.graphType !== "__all") {
    edges = edges.filter((edge) => edge.type === state.graphType);
  }
  if (state.graphFocus !== "__all") {
    edges = edges.filter((edge) => edge.source === state.graphFocus || edge.target === state.graphFocus);
  }

  const visibleIds = new Set();
  edges.forEach((edge) => {
    visibleIds.add(edge.source);
    visibleIds.add(edge.target);
  });

  if (state.graphFocus !== "__all") {
    visibleIds.add(state.graphFocus);
  }
  if (visibleIds.size === 0) {
    (graph.nodes || []).forEach((node) => visibleIds.add(node.id));
  }

  const nodes = [...visibleIds]
    .map((id) => nodesById.get(id))
    .filter(Boolean)
    .sort((left, right) => (right.recent_count || 0) - (left.recent_count || 0));

  els["graph-empty"].classList.toggle("hidden", nodes.length > 0);
  els["graph-legend"].innerHTML = (graph.legend || [])
    .map((item, index) => `<span class="legend-chip"><span class="status-dot" style="background:${colorForType(item, index)}"></span>${escapeHTML(item)}</span>`)
    .join("");

  if (nodes.length === 0) {
    els["graph-canvas"].innerHTML = "";
    els["graph-detail"].textContent = "当前还没有可视化关系数据。";
    return;
  }

  if (!state.selectedNode || !nodesById.has(state.selectedNode)) {
    state.selectedNode = (state.graphFocus !== "__all" ? state.graphFocus : nodes[0].id) || "";
  }

  drawGraph(nodes, edges, graph.edges || []);
}

function drawGraph(nodes, edges, allEdges) {
  const svg = els["graph-canvas"];
  const width = 960;
  const height = 560;
  svg.innerHTML = "";

  const simulation = nodes.map((node, index) => {
    const angle = (Math.PI * 2 * index) / Math.max(nodes.length, 1);
    const radius = Math.min(width, height) * 0.28;
    return {
      ...node,
      x: width / 2 + Math.cos(angle) * radius,
      y: height / 2 + Math.sin(angle) * radius,
      vx: 0,
      vy: 0,
    };
  });
  const points = new Map(simulation.map((node) => [node.id, node]));

  for (let iteration = 0; iteration < 150; iteration += 1) {
    for (let i = 0; i < simulation.length; i += 1) {
      const node = simulation[i];
      for (let j = i + 1; j < simulation.length; j += 1) {
        const other = simulation[j];
        let dx = node.x - other.x;
        let dy = node.y - other.y;
        const distSq = Math.max(dx * dx + dy * dy, 1);
        const force = 2200 / distSq;
        dx /= Math.sqrt(distSq);
        dy /= Math.sqrt(distSq);
        node.vx += dx * force;
        node.vy += dy * force;
        other.vx -= dx * force;
        other.vy -= dy * force;
      }
    }

    edges.forEach((edge) => {
      const source = points.get(edge.source);
      const target = points.get(edge.target);
      if (!source || !target) return;
      const dx = target.x - source.x;
      const dy = target.y - source.y;
      const distance = Math.max(Math.sqrt(dx * dx + dy * dy), 1);
      const desired = 120 - Math.min(edge.strength || 0, 8) * 5;
      const spring = (distance - desired) * 0.0035;
      const fx = (dx / distance) * spring;
      const fy = (dy / distance) * spring;
      source.vx += fx;
      source.vy += fy;
      target.vx -= fx;
      target.vy -= fy;
    });

    simulation.forEach((node) => {
      node.vx += (width / 2 - node.x) * 0.0008;
      node.vy += (height / 2 - node.y) * 0.0008;
      node.x += node.vx;
      node.y += node.vy;
      node.vx *= 0.86;
      node.vy *= 0.86;
      node.x = Math.max(72, Math.min(width - 72, node.x));
      node.y = Math.max(60, Math.min(height - 60, node.y));
    });
  }

  edges.forEach((edge, index) => {
    const source = points.get(edge.source);
    const target = points.get(edge.target);
    if (!source || !target) return;
    const line = document.createElementNS("http://www.w3.org/2000/svg", "line");
    line.setAttribute("x1", source.x);
    line.setAttribute("y1", source.y);
    line.setAttribute("x2", target.x);
    line.setAttribute("y2", target.y);
    line.setAttribute("stroke", colorForType(edge.type, index));
    line.setAttribute("stroke-width", String(Math.max(1.2, (edge.strength || 1) * 0.45)));
    line.setAttribute("stroke-linecap", "round");
    line.setAttribute("opacity", "0.75");
    svg.appendChild(line);
  });

  simulation.forEach((node, index) => {
    const group = document.createElementNS("http://www.w3.org/2000/svg", "g");
    group.style.cursor = "pointer";
    group.addEventListener("click", () => {
      state.selectedNode = node.id;
      renderGraphDetails(node.id, allEdges, nodes);
      drawGraph(nodes, edges, allEdges);
    });

    const ring = document.createElementNS("http://www.w3.org/2000/svg", "circle");
    ring.setAttribute("cx", node.x);
    ring.setAttribute("cy", node.y);
    ring.setAttribute("r", node.target ? "21" : "18");
    ring.setAttribute("fill", "rgba(255,255,255,0.88)");
    ring.setAttribute("stroke", node.id === state.selectedNode ? "#111111" : "rgba(17,17,17,0.28)");
    ring.setAttribute("stroke-width", node.id === state.selectedNode ? "3" : "1.4");
    group.appendChild(ring);

    const dot = document.createElementNS("http://www.w3.org/2000/svg", "circle");
    dot.setAttribute("cx", node.x);
    dot.setAttribute("cy", node.y);
    dot.setAttribute("r", String(10 + Math.min(node.recent_count || 0, 8)));
    dot.setAttribute("fill", node.id === state.selectedNode ? "#111111" : colorForType(node.label, index));
    group.appendChild(dot);

    const label = document.createElementNS("http://www.w3.org/2000/svg", "text");
    label.setAttribute("x", node.x);
    label.setAttribute("y", node.y + 34);
    label.setAttribute("text-anchor", "middle");
    label.setAttribute("font-size", "13");
    label.setAttribute("fill", "#111111");
    label.textContent = trimText(node.label, 12);
    group.appendChild(label);

    svg.appendChild(group);
  });

  renderGraphDetails(state.selectedNode, allEdges, nodes);
}

function renderGraphDetails(nodeID, allEdges, nodes) {
  const node = (nodes || []).find((item) => item.id === nodeID);
  if (!node) {
    els["graph-detail"].textContent = "点击图中的成员节点后，这里会显示详情。";
    return;
  }
  const relatedEdges = (allEdges || []).filter((edge) => edge.source === nodeID || edge.target === nodeID);
  const lines = [
    `成员: ${node.label}`,
    `ID: ${node.id}`,
    `最近活跃: ${node.recent_count || 0}`,
    `当前好感: ${node.affinity || 0}/100`,
    node.target ? "目标用户: 是" : "目标用户: 否",
    "",
    "关联关系:",
  ];
  if (relatedEdges.length === 0) {
    lines.push("暂无关联边。");
  } else {
    relatedEdges.slice(0, 8).forEach((edge) => {
      const peerID = edge.source === nodeID ? edge.target : edge.source;
      const peer = (nodes || []).find((item) => item.id === peerID);
      lines.push(`- ${peer?.label || peerID} · ${edge.type || "未标注"} · 强度 ${edge.strength || 0}`);
      if (edge.evidence) {
        lines.push(`  证据: ${edge.evidence}`);
      }
    });
  }
  els["graph-detail"].textContent = lines.join("\n");
}

function renderSettings() {
  const config = state.scopeState.config || {};
  const web = state.scopeState.web || {};
  const budget = state.scopeState.budget || {};
  els["config-enabled"].checked = Boolean(config.enabled);
  els["config-batch-size"].value = String(config.batch_size || 60);
  els["config-target-ids"].value = (config.target_user_ids || []).join("\n");

  const infoLines = [
    `监听地址: ${orFallback(web.listen_addr, "未提供")}`,
    `本地入口: ${orFallback(web.local_url, "不可用")}`,
    `公网入口: ${orFallback(web.public_url, web.local_url || "不可用")}`,
    `访问令牌: ${orFallback(web.token_preview, "未生成")}`,
    `完整入口: ${orFallback(web.access_url, "不可用")}`,
    web.error ? `错误: ${web.error}` : "",
  ].filter(Boolean);
  els["settings-web-info"].textContent = infoLines.join("\n");
  els["settings-budget-info"].textContent = [
    `场景摘要预算: ${formatMeta(budget.scene_summary)} 字`,
    `卡片预算: ${formatMeta(budget.cards)} 字`,
    `片段预算: ${formatMeta(budget.episodes)} 字`,
    `证据预算: ${formatMeta(budget.evidence)} 字`,
    `最近消息预算: ${formatMeta(budget.recent)} 字`,
  ].join("\n");
  els["settings-web-hints"].textContent = (state.bootstrap?.hints || []).join("\n");
}

function renderWebStatus(web) {
  const online = Boolean(web?.running);
  els["web-status-dot"].classList.toggle("is-online", online);
  els["web-status-text"].textContent = online ? "Web 服务运行中" : "Web 服务不可用";
  els["web-access-text"].textContent = online
    ? compactURL(web.public_url || web.local_url)
    : orFallback(web?.error, "等待加载。");
}

function setView(view) {
  state.currentView = view in VIEW_TITLES ? view : "overview";
  els.navItems.forEach((button) => {
    button.classList.toggle("is-active", button.dataset.view === state.currentView);
  });
  els.viewPanels.forEach((panel) => {
    panel.classList.toggle("is-active", panel.dataset.viewPanel === state.currentView);
    panel.classList.toggle("hidden", panel.dataset.viewPanel !== state.currentView);
  });
  els["view-title"].textContent = VIEW_TITLES[state.currentView];
}

function toggleSidebar(open) {
  els.shell.dataset.sidebarOpen = String(Boolean(open));
}

function showAuth(message) {
  els["auth-card"].classList.remove("hidden");
  els["auth-error"].textContent = message || "";
  els["empty-state"].classList.add("hidden");
  els["views"].classList.add("hidden");
}

function hideAuth() {
  els["auth-card"].classList.add("hidden");
  els["auth-error"].textContent = "";
}

function showEmpty() {
  hideAuth();
  els["views"].classList.add("hidden");
  els["empty-state"].classList.remove("hidden");
}

function showViews() {
  els["empty-state"].classList.add("hidden");
  els["views"].classList.remove("hidden");
}

async function api(path, options = {}) {
  const headers = new Headers(options.headers || {});
  headers.set("Authorization", `Bearer ${state.token}`);
  if (options.body && !headers.has("Content-Type")) {
    headers.set("Content-Type", "application/json");
  }
  const response = await fetch(path, {
    method: options.method || "GET",
    headers,
    body: options.body ? JSON.stringify(options.body) : undefined,
  });
  if (response.status === 401) {
    throw new Error("UNAUTHORIZED");
  }
  const data = await response.json().catch(() => ({}));
  if (!response.ok) {
    throw new Error(data.error || `HTTP ${response.status}`);
  }
  return data;
}

function handleError(error) {
  showToast(error.message || String(error));
}

function showToast(message) {
  clearTimeout(state.toastTimer);
  els.toast.textContent = message;
  els.toast.classList.add("is-visible");
  state.toastTimer = setTimeout(() => {
    els.toast.classList.remove("is-visible");
  }, 2800);
}

function syncScopeItem(scope) {
  if (!state.bootstrap?.scopes || !scope?.key) return;
  const index = state.bootstrap.scopes.findIndex((item) => item.key === scope.key);
  if (index >= 0) {
    state.bootstrap.scopes[index] = scope;
  }
}

function syncTokenFromWeb(web) {
  const token = extractToken(web?.access_url);
  if (!token) return;
  state.token = token;
  localStorage.setItem("self_learning_console_token", token);
}

function extractToken(accessURL) {
  if (!accessURL) return "";
  try {
    const url = new URL(accessURL, window.location.origin);
    return url.searchParams.get("token") || "";
  } catch (error) {
    return "";
  }
}

function setProse(id, value, fallback) {
  els[id].textContent = orFallback(value, fallback);
}

function listOrEmpty(items, fallback, renderer) {
  if (!items || items.length === 0) {
    return `<div class="bullet-item">${escapeHTML(fallback)}</div>`;
  }
  return items.map(renderer).join("");
}

function splitLines(value) {
  return value
    .split(/\r?\n/g)
    .map((item) => item.trim())
    .filter(Boolean);
}

function colorForType(value, index) {
  if (!value) return GRAPH_COLORS[index % GRAPH_COLORS.length];
  let hash = 0;
  for (let i = 0; i < value.length; i += 1) {
    hash = (hash << 5) - hash + value.charCodeAt(i);
    hash |= 0;
  }
  return GRAPH_COLORS[Math.abs(hash) % GRAPH_COLORS.length];
}

function compactURL(value) {
  if (!value) return "不可用";
  return trimText(value.replace(/^https?:\/\//, ""), 34);
}

function formatMeta(value) {
  return value === null || value === undefined || value === "" ? "0" : String(value);
}

function formatConfidence(value) {
  const number = Number(value || 0);
  if (!Number.isFinite(number) || number <= 0) return "0.00";
  return number.toFixed(2);
}

function trimText(value, limit) {
  const text = String(value || "");
  return text.length > limit ? `${text.slice(0, limit - 1)}…` : text;
}

function orFallback(value, fallback) {
  const text = String(value || "").trim();
  return text || fallback;
}

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;")
    .replaceAll("'", "&#39;");
}
