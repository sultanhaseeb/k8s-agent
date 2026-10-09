// Frontend for k8sagnet. All API calls are same-origin and served by go-svc,
// which nginx proxies at /api/.

const $ = (sel) => document.querySelector(sel);

const params = new URLSearchParams(location.search);
// "" means all namespaces. go-svc does the filtering and paging server-side.
let scope = params.get("namespace") || "";
let kind = params.get("kind") || "pods";
let page = Number(params.get("page")) || 1;
let pageSize = Number(params.get("pageSize")) || 20;

function query(path, extra = {}) {
  const q = new URLSearchParams();
  if (scope) q.set("namespace", scope);
  for (const [k, v] of Object.entries(extra)) q.set(k, v);
  const s = q.toString();
  return s ? `${path}?${s}` : path;
}

function syncURL() {
  const url = new URL(location.href);
  if (scope) url.searchParams.set("namespace", scope);
  else url.searchParams.delete("namespace");
  url.searchParams.set("kind", kind);
  url.searchParams.set("page", String(page));
  url.searchParams.set("pageSize", String(pageSize));
  history.replaceState(null, "", url);
}

async function getJSON(path) {
  const res = await fetch(path, { headers: { Accept: "application/json" } });
  if (!res.ok) {
    let detail = `${res.status} ${res.statusText}`;
    try {
      const body = await res.json();
      if (body.error || body.detail) detail = body.error || body.detail;
    } catch {
      /* non-JSON error body */
    }
    throw new Error(detail);
  }
  return res.json();
}

function showError(sel, message) {
  const el = $(sel);
  el.hidden = !message;
  el.textContent = message || "";
}

// ---- table helpers -------------------------------------------------------

function text(value) {
  const td = document.createElement("td");
  td.textContent = value === undefined || value === null || value === "" ? "—" : String(value);
  return td;
}

function pill(value) {
  const ok = ["Ready", "Active", "Running", "Succeeded", "Bound", "True"];
  const warn = ["Pending", "Terminating", "ContainerCreating", "Unknown"];
  const tone = ok.includes(value) ? "ok" : warn.includes(value) ? "warn" : "err";
  const td = document.createElement("td");
  const span = document.createElement("span");
  span.className = `pill pill-${tone}`;
  span.textContent = value ?? "—";
  td.appendChild(span);
  return td;
}

function emptyRow(table, colspan, message) {
  const tr = document.createElement("tr");
  const td = document.createElement("td");
  td.colSpan = colspan;
  td.className = "empty";
  td.textContent = message;
  tr.appendChild(td);
  table.querySelector("tbody").replaceChildren(tr);
}

// ---- cluster panel (go-svc) ---------------------------------------------

function renderStats(info) {
  const stats = [
    ["Server version", info.serverVersion],
    ["Platform", info.platform],
    ["Nodes", info.nodeCount],
    ["Namespaces", (info.allNamespaces || []).length],
    [scope ? `Pods in ${scope}` : "Pods", info.podCount],
  ];
  const container = $("#stats");
  container.replaceChildren();
  for (const [label, value] of stats) {
    const box = document.createElement("div");
    box.className = "stat";
    const l = document.createElement("div");
    l.className = "stat-label";
    l.textContent = label;
    const v = document.createElement("div");
    v.className = "stat-value";
    v.textContent = value ?? "—";
    box.append(l, v);
    container.appendChild(box);
  }
}

// Rebuild the picker from the full namespace list, keeping the current choice.
function populateNamespaces(names) {
  const select = $("#ns-select");
  const wanted = ["", ...(names || [])];
  const current = [...select.options].map((o) => o.value);
  if (current.join(" ") !== wanted.join(" ")) {
    select.replaceChildren();
    for (const name of wanted) {
      const opt = document.createElement("option");
      opt.value = name;
      opt.textContent = name === "" ? "All namespaces" : name;
      select.appendChild(opt);
    }
  }
  // A namespace can disappear between refreshes; fall back to all.
  if (scope && !wanted.includes(scope)) scope = "";
  select.value = scope;
  $("#scope-note").textContent = scope
    ? `showing resources in ${scope}`
    : "showing resources across all namespaces";
}

async function loadCluster() {
  try {
    const info = await getJSON(query("/api/cluster"));
    showError("#cluster-error", null);
    populateNamespaces(info.allNamespaces);
    renderStats(info);

    const tbody = $("#nodes-table").querySelector("tbody");
    tbody.replaceChildren();
    for (const n of info.nodes || []) {
      const tr = document.createElement("tr");
      tr.append(
        text(n.name), pill(n.status), text(n.roles), text(n.version),
        text(n.internalIP), text(n.cpu), text(n.memory), text(n.os), text(n.age),
      );
      tbody.appendChild(tr);
    }
  } catch (err) {
    showError("#cluster-error", `go-svc unreachable: ${err.message}`);
  }
}

// ---- resource tabs (fastapi-svc) ----------------------------------------

async function loadKinds() {
  const tabs = $("#kind-tabs");
  try {
    const { kinds } = await getJSON("/api/kinds");
    if (!kinds.some((k) => k.key === kind)) kind = kinds[0].key;
    tabs.replaceChildren();
    for (const k of kinds) {
      const btn = document.createElement("button");
      btn.className = "kind-tab" + (k.key === kind ? " active" : "");
      btn.textContent = k.label;
      btn.dataset.kind = k.key;
      btn.setAttribute("role", "tab");
      btn.setAttribute("aria-selected", String(k.key === kind));
      btn.addEventListener("click", () => selectKind(k.key));
      tabs.appendChild(btn);
    }
  } catch (err) {
    showError("#resource-error", `could not load resource kinds: ${err.message}`);
  }
}

function markActiveTab() {
  for (const btn of document.querySelectorAll(".kind-tab")) {
    const active = btn.dataset.kind === kind;
    btn.classList.toggle("active", active);
    btn.setAttribute("aria-selected", String(active));
  }
}

function selectKind(next) {
  kind = next;
  page = 1; // a new kind has its own row count
  markActiveTab();
  syncURL();
  loadResources();
}

function renderPager(data) {
  $("#page-info").textContent = `Page ${data.page} of ${data.totalPages}`;
  $("#page-prev").disabled = data.page <= 1;
  $("#page-next").disabled = data.page >= data.totalPages;
  $("#page-size").value = String(data.pageSize);
}

// One renderer for every kind: columns and rows both come from the server.
async function loadResources() {
  const table = $("#resource-table");
  const headRow = table.querySelector("thead tr");
  try {
    const data = await getJSON(
      query(`/api/resources/${encodeURIComponent(kind)}`, { page, pageSize }),
    );
    showError("#resource-error", null);
    // The server clamps an out-of-range page; follow it so the URL stays true.
    page = data.page;
    syncURL();
    renderPager(data);
    $("#kind-heading").textContent = data.label + (scope ? ` in ${scope}` : "");
    $("#kind-count").textContent =
      data.total === 0
        ? "0 items"
        : `showing ${(data.page - 1) * data.pageSize + 1}–${
            (data.page - 1) * data.pageSize + data.items.length
          } of ${data.total}`;

    // Pods get one extra column the server doesn't know about.
    const withActions = kind === "pods";

    headRow.replaceChildren();
    for (const c of data.columns) {
      const th = document.createElement("th");
      th.textContent = c.label;
      headRow.appendChild(th);
    }
    if (withActions) {
      const th = document.createElement("th");
      th.textContent = "Actions";
      headRow.appendChild(th);
    }

    if (data.items.length === 0) {
      emptyRow(table, data.columns.length + (withActions ? 1 : 0),
        scope ? `No ${data.label} in ${scope}` : `No ${data.label}`);
      return;
    }

    const tbody = table.querySelector("tbody");
    tbody.replaceChildren();
    for (const item of data.items) {
      const tr = document.createElement("tr");
      for (const c of data.columns) {
        tr.appendChild(c.key === data.statusKey ? pill(item[c.key]) : text(item[c.key]));
      }
      if (withActions) {
        // Scoped views omit the namespace column, so fall back to the filter.
        tr.appendChild(deleteCell(item.namespace || scope, item.name));
      }
      // Read by the delegated contextmenu handler.
      tr.dataset.namespace = item.namespace || scope;
      tr.dataset.name = item.name;
      tbody.appendChild(tr);
    }
  } catch (err) {
    showError("#resource-error", `${kind}: ${err.message}`);
    $("#kind-count").textContent = "";
    emptyRow(table, Math.max(headRow.children.length, 1), "—");
  }
}

// ---- pod deletion via the agent -----------------------------------------

// Which namespaces the agent may act in, from GET /api/agent/status. Until it
// loads, buttons stay disabled — the real enforcement is server-side, this is
// only so the UI doesn't offer an action that will certainly be refused.
let agentPolicy = { allowedNamespaces: [], authenticated: false, reachable: false, detail: "" };

async function loadAgentStatus() {
  try {
    const s = await getJSON("/api/agent/status");
    agentPolicy = {
      allowedNamespaces: s.allowedNamespaces || [],
      authenticated: !!s.authenticated,
      reachable: true,
      detail: s.authenticated ? "" : "agent has no Claude CLI login yet",
    };
  } catch (err) {
    agentPolicy = { allowedNamespaces: [], authenticated: false, reachable: false, detail: err.message };
  }
}

function deleteCell(namespace, name) {
  const td = document.createElement("td");
  const btn = document.createElement("button");
  btn.className = "danger-btn small";
  btn.textContent = "Delete";

  const allowed = agentPolicy.allowedNamespaces.includes(namespace);
  if (!agentPolicy.reachable) {
    btn.disabled = true;
    btn.title = `Agent unreachable: ${agentPolicy.detail}`;
  } else if (!allowed) {
    btn.disabled = true;
    btn.title = `Agent may not act in namespace "${namespace}"`;
  } else if (!agentPolicy.authenticated) {
    // Advisory: the login check is a heuristic, so warn rather than block.
    btn.title = "Agent may not be logged in yet — the task will say so";
  }

  btn.addEventListener("click", () => askDelete(namespace, name));
  td.appendChild(btn);
  return td;
}

// The pending target lives here rather than in a dialog `close` handler:
// button clicks are the same everywhere, dialog close semantics are not.
let pendingDelete = null;

function closeConfirm() {
  pendingDelete = null;
  const dialog = $("#confirm-dialog");
  if (dialog.open) dialog.close();
}

function askDelete(namespace, name) {
  const dialog = $("#confirm-dialog");
  const input = $("#confirm-input");
  const go = $("#confirm-go");

  pendingDelete = { namespace, name };
  $("#confirm-target").textContent = `${namespace}/${name}`;
  input.value = "";
  go.disabled = true;

  if (!dialog.open) dialog.showModal();
  input.focus();
}

$("#confirm-input").addEventListener("input", (e) => {
  $("#confirm-go").disabled = !pendingDelete || e.target.value.trim() !== pendingDelete.name;
});

$("#confirm-cancel").addEventListener("click", closeConfirm);

$("#confirm-go").addEventListener("click", () => {
  if (!pendingDelete) return;
  if ($("#confirm-input").value.trim() !== pendingDelete.name) return;
  const { namespace, name } = pendingDelete;
  closeConfirm();
  startDelete(namespace, name);
});

// Esc closes the dialog; keep our state in step with it.
$("#confirm-dialog").addEventListener("cancel", () => {
  pendingDelete = null;
});

let pollTimer = null;

function showTask(status, target, transcript, asMarkdown) {
  $("#task-panel").hidden = false;
  $("#task-status").textContent = status;
  $("#task-status").className = `task-status task-${status.toLowerCase().split(" ")[0]}`;
  $("#task-target").textContent = target;
  const body = $("#task-transcript");
  if (asMarkdown) {
    body.replaceChildren(renderMarkdown(transcript || ""));
  } else {
    body.textContent = transcript || "";
  }
}

async function startDelete(namespace, name) {
  clearInterval(pollTimer);
  const target = `${namespace}/${name}`;
  showTask("queued", target, "Handing the task to the agent…");

  let task;
  try {
    const res = await fetch("/api/actions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ action: "delete-pod", namespace, name }),
    });
    task = await res.json();
    if (!res.ok) {
      showTask("refused", target, task.error || task.detail || `HTTP ${res.status}`);
      return;
    }
  } catch (err) {
    showTask("failed", target, `Could not reach the API: ${err.message}`);
    return;
  }

  showTask("running", target, "The agent is working. This can take up to a minute…");
  pollTimer = setInterval(async () => {
    try {
      const t = await getJSON(`/api/tasks/${encodeURIComponent(task.id)}`);
      if (["succeeded", "failed", "timeout"].includes(t.status)) {
        clearInterval(pollTimer);
        showTask(t.status, target, t.transcript || t.error || "(no output)", t.status === "succeeded");
        refreshAll();
      } else {
        showTask(t.status, target, "The agent is working. This can take up to a minute…");
      }
    } catch (err) {
      clearInterval(pollTimer);
      showTask("failed", target, `Lost track of the task: ${err.message}`);
    }
  }, 2000);
}

$("#task-close").addEventListener("click", () => {
  clearInterval(pollTimer);
  $("#task-panel").hidden = true;
});

// ---- ask-the-agent chat --------------------------------------------------

// One conversation per resource, keyed kind/namespace/name. The value is the
// CLI session id, so follow-up questions keep their context.
const chatSessions = new Map();
let chatTarget = null;
let chatBusy = false;

function chatKey(t) {
  return `${t.kind}/${t.namespace}/${t.name}`;
}

// Agent replies are markdown; everything else is shown verbatim.
function setBubble(el, content, asMarkdown) {
  if (asMarkdown) {
    el.classList.add("md");
    el.replaceChildren(renderMarkdown(content));
  } else {
    el.classList.remove("md");
    el.textContent = content;
  }
}

function appendMessage(role, content, cssState) {
  const log = $("#chat-log");
  const wrap = document.createElement("div");
  wrap.className = `chat-msg chat-${role}${cssState ? " " + cssState : ""}`;
  const body = document.createElement("div");
  body.className = "chat-bubble";
  body.textContent = content;
  wrap.appendChild(body);
  log.appendChild(wrap);
  log.scrollTop = log.scrollHeight;
  return body;
}

function openChat(kind, namespace, name) {
  chatTarget = { kind, namespace, name };
  $("#chat-target").textContent = `${kind}/${name}  ·  ns: ${namespace}`;
  $("#chat-log").replaceChildren();
  $("#chat-input").value = "";
  setChatBusy(false);

  if (!agentPolicy.reachable) {
    appendMessage("system", `Agent unreachable: ${agentPolicy.detail}`, "chat-error");
  } else if (!agentPolicy.allowedNamespaces.includes(namespace)) {
    appendMessage(
      "system",
      `The agent may not act in namespace "${namespace}". Allowed: ${
        agentPolicy.allowedNamespaces.join(", ") || "(none)"
      }`,
      "chat-error",
    );
  } else {
    appendMessage(
      "system",
      "Ask anything about this resource. The agent has read-only kubectl here — " +
        "it cannot read Secrets or change anything.",
    );
    if (!agentPolicy.authenticated) {
      appendMessage("system", "Note: the agent may not be logged in yet.", "chat-error");
    }
  }

  const dialog = $("#chat-dialog");
  if (!dialog.open) dialog.showModal();
  $("#chat-input").focus();
}

function setChatBusy(busy) {
  chatBusy = busy;
  $("#chat-send").disabled = busy;
  $("#chat-input").disabled = busy;
  $("#chat-hint").textContent = busy
    ? "The agent is working…"
    : "Enter to send · Shift+Enter for a new line";
}

async function sendChat(prompt) {
  if (!chatTarget || chatBusy) return;
  const target = chatTarget;
  const key = chatKey(target);

  appendMessage("user", prompt);
  const pending = appendMessage("agent", "…thinking", "chat-pending");
  setChatBusy(true);

  let task;
  try {
    const res = await fetch("/api/actions", {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        action: "ask",
        kind: target.kind,
        namespace: target.namespace,
        name: target.name,
        prompt,
        sessionId: chatSessions.get(key) || "",
      }),
    });
    task = await res.json();
    if (!res.ok) {
      pending.textContent = task.error || task.detail || `HTTP ${res.status}`;
      pending.parentElement.classList.add("chat-error");
      setChatBusy(false);
      return;
    }
  } catch (err) {
    pending.textContent = `Could not reach the API: ${err.message}`;
    pending.parentElement.classList.add("chat-error");
    setChatBusy(false);
    return;
  }

  // Poll until the task reaches a terminal state.
  const started = Date.now();
  const timer = setInterval(async () => {
    try {
      const t = await getJSON(`/api/tasks/${encodeURIComponent(task.id)}`);
      if (!["succeeded", "failed", "timeout"].includes(t.status)) {
        const secs = Math.round((Date.now() - started) / 1000);
        pending.textContent = `…thinking (${secs}s)`;
        return;
      }
      clearInterval(timer);
      pending.parentElement.classList.remove("chat-pending");
      setBubble(pending, t.transcript || t.error || "(no output)", t.status === "succeeded");
      if (t.status !== "succeeded") pending.parentElement.classList.add("chat-error");
      $("#chat-log").scrollTop = $("#chat-log").scrollHeight;
      // Remember the session so the next question continues the thread.
      if (t.sessionId) chatSessions.set(key, t.sessionId);
      if ((t.permissionDenials || []).length) {
        appendMessage("system", `Tool calls refused: ${t.permissionDenials.length}`, "chat-error");
      }
      setChatBusy(false);
      $("#chat-input").focus();
    } catch (err) {
      clearInterval(timer);
      pending.textContent = `Lost track of the task: ${err.message}`;
      pending.parentElement.classList.add("chat-error");
      setChatBusy(false);
    }
  }, 2000);
}

// Right-click any row in the resource table.
$("#resource-table").addEventListener("contextmenu", (e) => {
  const tr = e.target.closest("tbody tr");
  if (!tr || !tr.dataset.name) return;
  e.preventDefault();
  openChat(kind, tr.dataset.namespace, tr.dataset.name);
});

$("#chat-form").addEventListener("submit", (e) => {
  e.preventDefault();
  const input = $("#chat-input");
  const prompt = input.value.trim();
  if (!prompt) return;
  input.value = "";
  sendChat(prompt);
});

$("#chat-input").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey) {
    e.preventDefault();
    $("#chat-form").requestSubmit();
  }
});

$("#chat-close").addEventListener("click", () => {
  const dialog = $("#chat-dialog");
  if (dialog.open) dialog.close();
});

// ---- wiring --------------------------------------------------------------

async function refreshAll() {
  await Promise.all([loadCluster(), loadResources()]);
  $("#stamp").textContent = `Updated ${new Date().toLocaleTimeString()}`;
}

$("#ns-select").addEventListener("change", (e) => {
  scope = e.target.value;
  page = 1; // row counts differ per namespace
  syncURL();
  refreshAll();
});

$("#page-prev").addEventListener("click", () => {
  if (page > 1) {
    page -= 1;
    loadResources();
  }
});

$("#page-next").addEventListener("click", () => {
  page += 1;
  loadResources();
});

$("#page-size").addEventListener("change", (e) => {
  pageSize = Number(e.target.value);
  page = 1;
  loadResources();
});

$("#refresh").addEventListener("click", refreshAll);

(async function init() {
  // Agent policy first: the table renders Delete buttons from it.
  await Promise.all([loadKinds(), loadAgentStatus()]);
  syncURL();
  await refreshAll();
  setInterval(async () => {
    await loadAgentStatus();
    refreshAll();
  }, 30000);
})();
