/* TinyOps 前端逻辑 */
"use strict";

const $ = (s) => document.querySelector(s);
const $$ = (s) => Array.from(document.querySelectorAll(s));

const TABS = ["overview", "projects", "nginx", "files", "terminal"];

let projects = [];
let currentTab = "overview";
let overviewTimer = null;
let overviewBusy = false;
let logES = null;

let topSort = "cpu";
let lastOverview = null;

const TERM_THEMES = {
  dark: {
    background: "#070a0e",
    foreground: "#dbe6f0",
    cursor: "#4c8dff",
    selectionBackground: "#2a3c52",
    black: "#0b0f14",
    red: "#f85149",
    green: "#3fb950",
    yellow: "#d29922",
    blue: "#4c8dff",
    magenta: "#a371f7",
    cyan: "#39c5cf",
    white: "#dbe6f0",
  },
  light: {
    background: "#f7f9fc",
    foreground: "#1b2530",
    cursor: "#2f6fe4",
    selectionBackground: "#c9dcfa",
    black: "#1b2530",
    red: "#cf222e",
    green: "#1a7f37",
    yellow: "#9a6700",
    blue: "#2f6fe4",
    magenta: "#8250df",
    cyan: "#1b7c83",
    white: "#1b2530",
  },
};

let filesInited = false;
let filesProject = "";
let filesPath = "";
let pendingFilesProject = "";

let term = null;
let fitAddon = null;
let ws = null;
let termReady = false;

/* ---------------- 工具 ---------------- */

function esc(v) {
  return String(v == null ? "" : v)
    .replace(/&/g, "&amp;")
    .replace(/</g, "&lt;")
    .replace(/>/g, "&gt;")
    .replace(/"/g, "&quot;")
    .replace(/'/g, "&#39;");
}

function formatBytes(n) {
  n = Number(n) || 0;
  if (n < 1024) return n + " B";
  const units = ["KB", "MB", "GB", "TB", "PB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return v.toFixed(v >= 100 ? 0 : v >= 10 ? 1 : 2) + " " + units[i];
}

function formatRate(n) {
  return formatBytes(n) + "/s";
}

function formatUptime(sec) {
  sec = Math.max(0, Math.floor(Number(sec) || 0));
  const d = Math.floor(sec / 86400);
  const h = Math.floor((sec % 86400) / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (d > 0) return `${d} 天 ${h} 小时`;
  if (h > 0) return `${h} 小时 ${m} 分`;
  return `${m} 分钟`;
}

function fmtTime(v) {
  if (!v) return "-";
  const d = new Date(v);
  if (isNaN(d.getTime())) return String(v);
  return d.toLocaleString("zh-CN", { hour12: false });
}

function barClass(pct) {
  if (pct >= 90) return "bad";
  if (pct >= 75) return "warn";
  return "";
}

/* ---------------- 主题 ---------------- */

function currentTheme() {
  return document.documentElement.dataset.theme === "light" ? "light" : "dark";
}

function applyTheme(theme, persist = true) {
  theme = theme === "light" ? "light" : "dark";
  document.documentElement.dataset.theme = theme;
  if (persist) {
    try {
      localStorage.setItem("pm_theme", theme);
    } catch (e) {}
  }
  const btn = $("#theme-toggle");
  if (btn) {
    const label = theme === "light" ? "切换到深色主题" : "切换到浅色主题";
    btn.title = label;
    btn.setAttribute("aria-label", label);
  }
  if (term) {
    try {
      term.options.theme = TERM_THEMES[theme];
    } catch (e) {}
  }
}

let toastTimer = null;
function toast(msg, isErr) {
  const el = $("#toast");
  el.textContent = msg;
  el.classList.toggle("bad", !!isErr);
  el.classList.remove("hidden");
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => el.classList.add("hidden"), 4500);
}

async function api(url, opts) {
  const options = Object.assign({}, opts || {});
  options.headers = Object.assign({}, (opts && opts.headers) || {});
  if (options.body && !(options.body instanceof FormData) && typeof options.body !== "string") {
    options.body = JSON.stringify(options.body);
    options.headers["Content-Type"] = "application/json";
  }

  const res = await fetch(url, options);
  if (res.status === 401) {
    showLogin();
    const err = new Error("未登录或会话已过期");
    err.status = 401;
    throw err;
  }

  const ct = res.headers.get("content-type") || "";
  let data;
  if (ct.includes("application/json")) {
    data = await res.json().catch(() => ({}));
  } else {
    data = await res.text();
  }

  if (!res.ok) {
    const msg = (data && data.error) || (typeof data === "string" ? data : res.statusText);
    const err = new Error(msg || `HTTP ${res.status}`);
    err.status = res.status;
    err.data = data;
    throw err;
  }
  return data;
}

/* ---------------- 登录 ---------------- */

let loggedIn = false;

function showLogin() {
  loggedIn = false;
  $("#login-mask").classList.remove("hidden");
  stopOverviewTimer();
  setTimeout(() => $("#login-password").focus(), 50);
}

function hideLogin() {
  loggedIn = true;
  $("#login-mask").classList.add("hidden");
}

async function boot() {
  try {
    await api("/api/me");
  } catch (e) {
    if (e.status !== 401) toast("连接后端失败: " + e.message, true);
    showLogin();
    return;
  }
  hideLogin();
  const hashTab = location.hash.slice(1);
  const initial = TABS.includes(hashTab) ? hashTab : "overview";
  switchTab(initial);
}

/* ---------------- Tab ---------------- */

function switchTab(name) {
  if (!TABS.includes(name)) name = "overview";
  currentTab = name;
  $$(".tab").forEach((t) => t.classList.toggle("active", t.dataset.tab === name));
  $$(".tab-pane").forEach((p) => p.classList.toggle("active", p.id === "tab-" + name));
  if (location.hash.slice(1) !== name) {
    history.replaceState(null, "", "#" + name);
  }

  if (name === "overview") {
    refreshOverview();
    startOverviewTimer();
  } else {
    stopOverviewTimer();
  }
  if (name === "projects") loadProjects().catch((e) => toast(e.message, true));
  if (name === "nginx") loadNginxList();
  if (name === "files") initFiles();
  if (name === "terminal") initTerminal();
}

function startOverviewTimer() {
  stopOverviewTimer();
  overviewTimer = setInterval(() => {
    if (document.hidden || currentTab !== "overview") return;
    refreshOverview();
  }, 2000);
}

function stopOverviewTimer() {
  if (overviewTimer) {
    clearInterval(overviewTimer);
    overviewTimer = null;
  }
}

/* ---------------- 概览 ---------------- */

async function refreshOverview() {
  if (overviewBusy || !loggedIn) return;
  overviewBusy = true;
  try {
    const d = await api("/api/overview");
    renderOverview(d);
  } catch (e) {
    if (e.status !== 401) console.warn("overview:", e.message);
  } finally {
    overviewBusy = false;
  }
}

function renderOverview(d) {
  const cpu = d.cpu || {};
  const mem = d.mem || {};
  const host = d.host || {};
  const net = d.net || {};

  $("#ov-cpu").textContent = (Number(cpu.percent) || 0).toFixed(1) + "%";
  setBar($("#ov-cpu-bar"), cpu.percent);
  $("#ov-load").textContent =
    `负载 ${num(cpu.load1)} / ${num(cpu.load5)} / ${num(cpu.load15)} · ${cpu.cores || "-"} 核` +
    (cpu.model ? ` · ${cpu.model}` : "");

  $("#ov-mem").textContent = (Number(mem.used_percent) || 0).toFixed(1) + "%";
  setBar($("#ov-mem-bar"), mem.used_percent);
  let memDetail = `${formatBytes(mem.used)} / ${formatBytes(mem.total)}`;
  if (mem.swap_total > 0) {
    memDetail += ` · Swap ${formatBytes(mem.swap_used)} / ${formatBytes(mem.swap_total)}`;
  }
  $("#ov-mem-detail").textContent = memDetail;

  $("#ov-net").textContent = `↓ ${formatRate(net.rx_per_sec)}  ↑ ${formatRate(net.tx_per_sec)}`;
  $("#ov-net-total").textContent =
    `累计 ↓ ${formatBytes(net.rx_total)}  ↑ ${formatBytes(net.tx_total)}`;

  $("#host-label").textContent = host.hostname || "";
  $("#ov-host").textContent = host.hostname || "-";
  $("#ov-host-detail").textContent =
    `${host.os || "-"} · ${host.kernel || "-"} · ${host.arch || ""} · 运行 ${formatUptime(host.uptime)}`;

  const disks = d.disks || [];
  $("#ov-disks").innerHTML = disks.length
    ? disks
        .map(
          (x) => `
        <div class="disk-row">
          <div class="mono small">${esc(x.mount)} <span class="muted">${esc(x.fstype || "")}</span></div>
          <div><div class="bar"><span class="${barClass(x.used_percent)}" style="width:${pct(x.used_percent)}%"></span></div></div>
          <div class="muted small mono">${formatBytes(x.used)} / ${formatBytes(x.total)} (${(Number(x.used_percent) || 0).toFixed(0)}%)</div>
        </div>`
        )
        .join("")
    : '<div class="muted small">无数据</div>';

  lastOverview = d;
  renderTop();
}

function renderTop() {
  const el = $("#ov-top");
  if (!el || !lastOverview) return;
  const seg = $("#top-sort");
  if (seg) seg.querySelectorAll("button").forEach((b) => b.classList.toggle("active", b.dataset.sort === topSort));
  const list = topSort === "mem" ? lastOverview.top_mem || [] : lastOverview.top || [];
  el.innerHTML = list.length
    ? list
        .map(
          (p) => `<tr>
            <td class="mono">${esc(p.pid)}</td>
            <td class="muted">${esc(p.user)}</td>
            <td>${num(p.cpu)}</td>
            <td>${num(p.mem)}</td>
            <td class="mono small">${esc(p.cmd)}</td>
          </tr>`
        )
        .join("")
    : '<tr><td colspan="5" class="muted">无数据</td></tr>';
}

function num(v) {
  const n = Number(v);
  if (!isFinite(n)) return "-";
  return n.toFixed(1);
}

function pct(v) {
  const n = Number(v) || 0;
  return Math.max(0, Math.min(100, n)).toFixed(1);
}

function setBar(el, v) {
  if (!el) return;
  el.style.width = pct(v) + "%";
  el.className = barClass(Number(v) || 0);
}

/* ---------------- 项目 ---------------- */

async function loadProjects() {
  const list = await api("/api/projects");
  projects = list || [];
  renderProjects(projects);
  if (currentTab === "files") fillProjectSelect();
  return projects;
}

function renderProjects(list) {
  const el = $("#projects-list");
  if (!list.length) {
    el.innerHTML = '<div class="muted">还没有项目，点右上角「添加项目」，或检查自动发现配置。</div>';
    return;
  }
  el.innerHTML = list
    .map((p) => {
      const isStatic = p.type === "static";
      const running = !!p.active;

      const badge = isStatic
        ? '<span class="badge static">静态</span>'
        : `<span class="badge ${p.enabled === "enabled" ? "ok" : "off"}">${esc(p.enabled || "unknown")}</span>`;

      const subtitle = isStatic
        ? `静态项目 · ${esc(p.site || "未关联站点")}`
        : `${esc(p.unit)} · ${esc(p.state || p.err || "未知")}`;

      const pid = p.main_pid && p.main_pid !== "0" ? "PID " + p.main_pid : "";
      const info = isStatic
        ? p.state || (running ? "在线" : "已下线")
        : (pid || "未运行") + (running && p.since ? " · 启动于 " + p.since : "");

      const folderBtn = `<button class="ghost small" data-act="files" ${
        p.dir ? "" : "disabled"
      } title="${p.dir ? "打开项目目录" : "未配置项目目录"}">文件夹</button>`;

      const actions = isStatic
        ? `${folderBtn}
           ${
             running
               ? '<button class="danger small" data-act="offline">下线</button>'
               : '<button class="primary small" data-act="online">上线</button>'
           }`
        : `<button class="primary small" data-act="start" ${running ? "disabled" : ""}>启动</button>
           <button class="danger small" data-act="stop" ${running ? "" : "disabled"}>停止</button>
           <button class="ghost small" data-act="restart" ${running ? "" : "disabled"}>重启</button>
           ${folderBtn}
           <button class="ghost small" data-act="logs">日志</button>`;

      return `
      <div class="proj-card ${running ? "is-active" : ""}" data-name="${esc(p.name)}">
        <div class="proj-head">
          <span class="dot ${running ? "on" : ""}" title="${running ? "在线" : "未运行"}"></span>
          <div class="proj-title">
            <div class="proj-name">${esc(p.name)}</div>
            <div class="muted small mono">${subtitle}</div>
          </div>
          ${badge}
        </div>
        ${p.desc ? `<div class="proj-desc">${esc(p.desc)}</div>` : ""}
        ${p.dir ? `<div class="muted small mono">${esc(p.dir)}</div>` : ""}
        <div class="muted small">${esc(info)}</div>
        <div class="proj-actions">${actions}</div>
      </div>`;
    })
    .join("");

  el.querySelectorAll("button[data-act]").forEach((btn) => {
    btn.addEventListener("click", async () => {
      const card = btn.closest(".proj-card");
      const name = card.dataset.name;
      const act = btn.dataset.act;
      if (act === "logs") {
        openLogs(name);
        return;
      }
      if (act === "files") {
        openProjectFiles(name);
        return;
      }
      await doProjectAction(name, act, btn);
    });
  });
}

async function doProjectAction(name, act, btn) {
  const label = { start: "启动", stop: "停止", restart: "重启", online: "上线", offline: "下线" }[act] || act;
  btn.disabled = true;
  try {
    const r = await api(`/api/projects/${encodeURIComponent(name)}/${act}`, { method: "POST" });
    if (r.test_ok === false) {
      toast(`${name} ${label}完成，但 nginx -t 未通过${r.test_error ? "：" + r.test_error : ""}`, true);
    } else {
      toast(`${name} ${label}成功${r.output && r.output.trim() ? "：" + r.output.trim() : ""}`);
    }
  } catch (e) {
    toast(`${name} ${label}失败：${e.message}`, true);
  } finally {
    loadProjects().catch(() => {});
  }
}

/* ---------------- 日志弹窗 ---------------- */

function openLogs(name) {
  closeLogs();
  $("#log-modal").classList.remove("hidden");
  $("#log-title").textContent = name + " 日志";
  $("#log-status").textContent = "连接中…";
  const pre = $("#log-content");
  pre.textContent = "";

  const es = new EventSource(
    `/api/projects/${encodeURIComponent(name)}/logs/stream?lines=200`
  );
  logES = es;

  es.onmessage = (ev) => {
    const atBottom = pre.scrollTop + pre.clientHeight >= pre.scrollHeight - 60;
    pre.textContent += ev.data + "\n";
    if (pre.textContent.length > 400000) {
      const lines = pre.textContent.split("\n");
      pre.textContent = lines.slice(-2000).join("\n");
    }
    if (atBottom) pre.scrollTop = pre.scrollHeight;
  };
  es.addEventListener("end", (ev) => {
    const text = (ev.data || "").trim();
    $("#log-status").textContent = text || "日志流已结束";
    if (text && !text.includes("已退出")) pre.textContent += "\n[stream] " + text + "\n";
  });
  es.onopen = () => {
    $("#log-status").textContent = "实时跟随中";
  };
  es.onerror = () => {
    $("#log-status").textContent = "连接中断，正在重试…";
  };
}

function closeLogs() {
  if (logES) {
    logES.close();
    logES = null;
  }
  $("#log-modal").classList.add("hidden");
  $("#log-status").textContent = "";
}

/* ---------------- 添加项目 ---------------- */

function syncAddType() {
  const isStatic = $("#add-type").value === "static";
  $("#add-unit-wrap").classList.toggle("hidden", isStatic);
  $("#add-site-wrap").classList.toggle("hidden", !isStatic);
  $("#add-unit").required = !isStatic;
  $("#add-dir").placeholder = isStatic
    ? "/var/www/site（目录根部需有 index.html）"
    : "/opt/apps/api";
  $("#add-name").placeholder = isStatic ? "默认使用目录名" : "默认使用 service 名去掉 .service";
  $("#add-desc").placeholder = isStatic ? "可选描述" : "默认使用 unit 里的 Description";
}

function openAddProject() {
  ["#add-unit", "#add-dir", "#add-site", "#add-name", "#add-desc"].forEach((s) => {
    $(s).value = "";
  });
  $("#add-type").value = "service";
  syncAddType();
  $("#add-project-error").textContent = "";
  $("#add-project-modal").classList.remove("hidden");
  setTimeout(() => {
    ($("#add-type").value === "static" ? $("#add-dir") : $("#add-unit")).focus();
  }, 30);
}

function closeAddProject() {
  $("#add-project-modal").classList.add("hidden");
}

async function submitAddProject(e) {
  e.preventDefault();
  const errEl = $("#add-project-error");
  errEl.textContent = "";
  const btn = e.target.querySelector('button[type="submit"]');

  btn.disabled = true;
  try {
    const d = await api("/api/projects", {
      method: "POST",
      body: {
        type: $("#add-type").value,
        unit: $("#add-unit").value.trim(),
        dir: $("#add-dir").value.trim(),
        site: $("#add-site").value.trim(),
        name: $("#add-name").value.trim(),
        desc: $("#add-desc").value.trim(),
      },
    });
    closeAddProject();
    const p = d.project;
    toast(
      p.type === "static"
        ? `已添加静态项目 ${p.name}（${p.site}）`
        : `已添加项目 ${p.name}（${p.unit}）`
    );
    await loadProjects();
  } catch (err) {
    errEl.textContent = err.message;
  } finally {
    btn.disabled = false;
  }
}

/* ---------------- Nginx ---------------- */

let nginxGroups = [];
let nginxGroupId = "";
let nginxGroupsLoaded = false;
let nginxFile = "";
let nginxOriginal = "";
let nginxDirty = false;
let nginxSaving = false;

async function loadNginxList() {
  try {
    if (!nginxGroupsLoaded) await loadNginxGroups();
  } catch (e) {
    if (e.status !== 401) toast("读取 nginx 配置失败：" + e.message, true);
  }
}

async function loadNginxGroups() {
  const d = await api("/api/nginx/groups");
  nginxGroups = d.groups || [];
  nginxGroupsLoaded = true;

  const sel = $("#nginx-group");
  const fileList = $("#nginx-files");
  if (!nginxGroups.length) {
    sel.innerHTML = '<option value="">（未找到配置目录）</option>';
    sel.disabled = true;
    fileList.innerHTML =
      '<div class="muted small pad">没有可用的 nginx 配置目录，请检查 config.yaml 的 nginx.config_dir / nginx.dirs</div>';
    nginxGroupId = "";
    return;
  }
  sel.disabled = false;

  const savedGroup = localStorage.getItem("pm_nginx_group");
  let pick = nginxGroups.find((g) => g.id === savedGroup);
  if (!pick) pick = nginxGroups.find((g) => g.id === "conf.d") || nginxGroups[0];

  sel.innerHTML = nginxGroups
    .map((g) => `<option value="${esc(g.id)}">${esc(g.name)}（${g.count}）</option>`)
    .join("");
  sel.value = pick.id;
  nginxGroupId = pick.id;
  await loadNginxFiles();
}

async function loadNginxFiles() {
  const el = $("#nginx-files");
  if (!nginxGroupId) return;
  el.innerHTML = '<div class="muted small pad">加载中…</div>';
  try {
    const d = await api(`/api/nginx/files?group=${encodeURIComponent(nginxGroupId)}`);
    const files = d.files || [];
    if (!files.length) {
      el.innerHTML = '<div class="muted small pad">该目录为空</div>';
      return;
    }
    el.innerHTML = files
      .map((f) => {
        const toggle = f.can_toggle
          ? `<button class="file-toggle ${f.enabled ? "on" : ""}" data-toggle="${esc(f.path)}" title="${
              f.enabled ? "停用：删除 sites-enabled 中的软链" : "启用：在 sites-enabled 中创建软链"
            }">${f.enabled ? "停用" : "启用"}</button>`
          : "";
        return `<div class="file-row">
          <button class="file-item ${f.path === nginxFile ? "active" : ""}" data-path="${esc(
            f.path
          )}" title="${esc(f.target ? "软链 → " + f.target : f.path)}">${f.symlink ? "↗ " : ""}${esc(
            f.path
          )}</button>${toggle}
        </div>`;
      })
      .join("");
    el.querySelectorAll(".file-item").forEach((btn) => {
      btn.addEventListener("click", () => openNginxFile(btn.dataset.path, btn));
    });
    el.querySelectorAll(".file-toggle").forEach((btn) => {
      const f = files.find((x) => x.path === btn.dataset.toggle);
      btn.addEventListener("click", (ev) => {
        ev.stopPropagation();
        toggleNginxSite(f, btn);
      });
    });
  } catch (e) {
    el.innerHTML = `<div class="error small pad">${esc(e.message)}</div>`;
  }
}

function confirmDiscardNginx() {
  return !nginxDirty || confirm("当前配置有未保存的修改，确定放弃吗？");
}

function resetNginxEditor() {
  nginxFile = "";
  nginxOriginal = "";
  const editor = $("#nginx-editor");
  editor.value = "";
  $("#nginx-path").textContent = "选择左侧文件";
  $("#nginx-target").textContent = "";
  $("#nginx-result").classList.add("hidden");
  setNginxDirty(false);
}

function setNginxDirty(v) {
  nginxDirty = !!v;
  $("#nginx-dirty").classList.toggle("hidden", !nginxDirty);
  $("#nginx-save").disabled = !nginxDirty || nginxSaving;
  $("#nginx-revert").disabled = !nginxDirty;
}

async function openNginxFile(path, btn) {
  if (path === nginxFile && !nginxDirty) return;
  if (!confirmDiscardNginx()) return;
  try {
    const d = await api(
      `/api/nginx/file?group=${encodeURIComponent(nginxGroupId)}&path=${encodeURIComponent(path)}`
    );
    nginxFile = path;
    nginxOriginal = d.content || "";
    const editor = $("#nginx-editor");
    editor.value = nginxOriginal;
    $("#nginx-path").textContent = `${nginxGroupId} / ${d.path}`;
    $("#nginx-target").textContent = d.symlink && d.target ? `→ ${d.target}` : "";
    $("#nginx-result").classList.add("hidden");
    setNginxDirty(false);
    $$("#nginx-files .file-item").forEach((b) => b.classList.toggle("active", b === btn));
    editor.focus();
  } catch (e) {
    toast("读取配置失败：" + e.message, true);
  }
}

function showNginxResult(ok, output, backup, testError) {
  const out = $("#nginx-result");
  out.classList.remove("hidden");
  out.classList.toggle("bad", !ok);
  let text = output || "";
  if (ok) {
    text += (text ? "\n" : "") + "✅ nginx -t 通过（尚未重载，确认无误后点“重载”）";
  } else {
    text += (text ? "\n" : "") + "❌ " + (testError || "nginx -t 未通过");
  }
  if (backup) text += "\n备份：" + backup;
  out.textContent = text;
}

async function saveNginxFile() {
  if (!nginxFile || nginxSaving) return;
  const content = $("#nginx-editor").value;
  const btn = $("#nginx-save");
  const label = btn.textContent;

  nginxSaving = true;
  btn.textContent = "保存中…";
  btn.disabled = true;
  try {
    const d = await api("/api/nginx/file", {
      method: "PUT",
      body: { group: nginxGroupId, path: nginxFile, content },
    });
    nginxOriginal = content;
    setNginxDirty(false);
    showNginxResult(d.test_ok, d.test_output, d.backup, d.test_error);
    toast(d.test_ok ? "已保存，nginx -t 通过" : "已保存，但 nginx -t 未通过，请检查", !d.test_ok);
    loadNginxFiles();
  } catch (e) {
    toast("保存失败：" + e.message, true);
  } finally {
    nginxSaving = false;
    btn.textContent = label;
    setNginxDirty($("#nginx-editor").value !== nginxOriginal);
  }
}

async function toggleNginxSite(f, btn) {
  if (!f) return;
  const enabling = !f.enabled;
  const action = enabling ? "enable" : "disable";
  btn.disabled = true;
  try {
    const d = await api(`/api/nginx/${action}`, {
      method: "POST",
      body: { group: nginxGroupId, path: f.path },
    });
    showNginxResult(d.test_ok, d.test_output, null, d.test_error);
    toast(
      `${enabling ? "已启用" : "已停用"} ${f.path}${
        d.test_ok ? "，nginx -t 通过" : "，但 nginx -t 未通过，请检查"
      }`,
      !d.test_ok
    );
    if (!enabling && nginxGroupId === "sites-enabled" && nginxFile === f.path && !nginxDirty) {
      resetNginxEditor();
    }
    await loadNginxGroups();
  } catch (e) {
    toast(`${enabling ? "启用" : "停用"}失败：${e.message}`, true);
    btn.disabled = false;
  }
}

async function nginxAction(kind) {
  const url = kind === "test" ? "/api/nginx/test" : "/api/nginx/reload";
  const out = $("#nginx-result");
  out.classList.remove("hidden");
  out.textContent = "执行中…";
  try {
    const d = await api(url, { method: "POST" });
    out.textContent = (d.output || "") + (d.ok ? "\n✅ 执行成功" : "\n❌ " + (d.error || "失败"));
    out.classList.toggle("bad", !d.ok);
    toast(kind === "test" ? "nginx 配置检查完成" : "nginx 已重载", !d.ok);
  } catch (e) {
    out.textContent = e.message;
    out.classList.add("bad");
    toast(e.message, true);
  }
}

/* ---------------- 文件 ---------------- */

function initFiles() {
  if (!filesInited) {
    filesInited = true;
    $("#files-project").addEventListener("change", () => {
      filesProject = $("#files-project").value;
      filesPath = "";
      loadFiles();
    });
    $("#files-input").addEventListener("change", (e) => {
      const files = Array.from(e.target.files || []);
      e.target.value = "";
      if (files.length) uploadAll(files, filesPath, null);
    });
    setupDrop();
  }

  if (!projects.length) {
    loadProjects()
      .catch(() => {})
      .finally(() => {
        fillProjectSelect();
        loadFiles();
      });
    return;
  }
  fillProjectSelect();
  loadFiles();
}

function fillProjectSelect() {
  const sel = $("#files-project");
  const withDir = projects.filter((p) => p.dir);
  const prev = pendingFilesProject || sel.value || filesProject;
  sel.innerHTML = withDir.length
    ? withDir.map((p) => `<option value="${esc(p.name)}">${esc(p.name)} — ${esc(p.dir)}</option>`).join("")
    : '<option value="">（没有配置目录的项目）</option>';
  if (prev && withDir.some((p) => p.name === prev)) sel.value = prev;
  pendingFilesProject = "";
  filesProject = sel.value;
}

function openProjectFiles(name) {
  pendingFilesProject = name;
  filesPath = "";
  switchTab("files");
}

async function loadFiles() {
  const tbody = $("#files-list");
  renderBreadcrumb(filesPath);

  if (!filesProject) {
    tbody.innerHTML = '<tr><td colspan="5" class="muted" style="padding:16px">请先在 config.yaml 中给项目配置 dir 字段</td></tr>';
    return;
  }

  tbody.innerHTML = '<tr><td colspan="5" class="muted" style="padding:16px">加载中…</td></tr>';
  try {
    const d = await api(
      `/api/files/list?project=${encodeURIComponent(filesProject)}&path=${encodeURIComponent(filesPath)}`
    );
    const rows = [];
    if (filesPath) {
      rows.push(`<tr class="dir" data-nav=".."><td>⬆ 上一级</td><td></td><td></td><td></td><td></td></tr>`);
    }
    for (const e of d.entries || []) {
      const base = `/api/files/download?project=${encodeURIComponent(filesProject)}&path=${encodeURIComponent(e.path)}`;
      rows.push(`
        <tr class="${e.is_dir ? "dir" : "file"}" data-path="${esc(e.path)}" data-dir="${e.is_dir ? "true" : "false"}">
          <td>${e.is_dir ? "📁" : "📄"} ${esc(e.name)}</td>
          <td class="muted">${e.is_dir ? "" : formatBytes(e.size)}</td>
          <td class="muted mono small">${esc(e.mode || "")}</td>
          <td class="muted small">${fmtTime(e.mod_time)}</td>
          <td class="actions">${e.is_dir ? "" : `<a href="${esc(base)}" target="_blank">下载</a>`}</td>
        </tr>`);
    }
    tbody.innerHTML = rows.join("") || '<tr><td colspan="5" class="muted" style="padding:16px">空目录</td></tr>';

    tbody.querySelectorAll("tr[data-nav], tr[data-dir]").forEach((tr) => {
      tr.addEventListener("click", (ev) => {
        if (ev.target.closest("a")) return;
        if (tr.dataset.nav === "..") {
          filesPath = parentPath(filesPath);
          loadFiles();
        } else if (tr.dataset.dir === "true") {
          filesPath = tr.dataset.path;
          loadFiles();
        }
      });
    });
  } catch (e) {
    tbody.innerHTML = `<tr><td colspan="5" class="error" style="padding:16px">${esc(e.message)}</td></tr>`;
  }
}

function renderBreadcrumb(p) {
  const bc = $("#files-breadcrumb");
  const parts = p ? p.split("/").filter(Boolean) : [];
  let html = `<a href="#" data-path="">${esc(filesProject || "项目")}</a>`;
  let acc = "";
  for (const seg of parts) {
    acc = acc ? acc + "/" + seg : seg;
    html += ` / <a href="#" data-path="${esc(acc)}">${esc(seg)}</a>`;
  }
  bc.innerHTML = html;
  bc.querySelectorAll("a").forEach((a) => {
    a.addEventListener("click", (ev) => {
      ev.preventDefault();
      filesPath = a.dataset.path;
      loadFiles();
    });
  });
}

function parentPath(p) {
  const parts = p.split("/").filter(Boolean);
  parts.pop();
  return parts.join("/");
}

function joinPath(base, name) {
  return base ? base + "/" + name : name;
}

function setupDrop() {
  const dz = $("#files-drop");
  dz.addEventListener("dragover", (e) => {
    e.preventDefault();
    e.dataTransfer.dropEffect = "copy";
    dz.classList.add("dragover");
  });
  dz.addEventListener("dragleave", (e) => {
    if (!dz.contains(e.relatedTarget)) dz.classList.remove("dragover");
  });
  dz.addEventListener("drop", (e) => {
    e.preventDefault();
    dz.classList.remove("dragover");
    const files = Array.from(e.dataTransfer.files || []);
    if (!files.length || !filesProject) return;

    let basePath = filesPath;
    let replacePath = null;
    const row = e.target.closest("tr");
    if (row && row.dataset.path) {
      if (row.dataset.dir === "true") basePath = row.dataset.path;
      else replacePath = row.dataset.path;
    }
    uploadAll(files, basePath, replacePath);
  });
}

async function uploadAll(files, basePath, replacePath) {
  if (!filesProject) {
    toast("请先选择项目", true);
    return;
  }
  const jobs = files.map((file, i) => {
    const target = replacePath && i === 0 ? replacePath : joinPath(basePath, file.name);
    return uploadOne(target, file);
  });
  await Promise.all(jobs);
  loadFiles();
}

async function uploadOne(path, file) {
  const fd = new FormData();
  fd.append("project", filesProject);
  fd.append("path", path);
  fd.append("file", file);
  try {
    const d = await api("/api/files/upload", { method: "POST", body: fd });
    toast(`已上传 ${path}（${formatBytes(d.size)}）${d.backup ? "，旧文件已备份为 .bak" : ""}`);
  } catch (e) {
    toast(`上传 ${path} 失败：${e.message}`, true);
  }
}

/* ---------------- 终端 ---------------- */

function initTerminal() {
  if (!termReady) {
    termReady = true;
    term = new Terminal({
      fontFamily: 'ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace',
      fontSize: 13,
      lineHeight: 1.25,
      cursorBlink: true,
      scrollback: 8000,
      theme: TERM_THEMES[currentTheme()],
    });
    fitAddon = new FitAddon.FitAddon();
    term.loadAddon(fitAddon);
    term.open($("#terminal"));
    term.onData((data) => {
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify({ t: "input", d: data }));
      }
    });
    term.onResize(() => sendResize());
    connectTerm();

    if (typeof ResizeObserver !== "undefined") {
      const ro = new ResizeObserver(() => {
        if (currentTab === "terminal") {
          try {
            fitAddon.fit();
          } catch (e) {}
        }
      });
      ro.observe($("#terminal"));
    }
    window.addEventListener("resize", () => {
      if (currentTab === "terminal") {
        try {
          fitAddon.fit();
        } catch (e) {}
      }
    });
  }

  requestAnimationFrame(() => {
    try {
      fitAddon.fit();
      term.focus();
    } catch (e) {}
  });
}

function connectTerm() {
  if (!term) return;
  if (ws) {
    try {
      ws.onclose = null;
      ws.close();
    } catch (e) {}
  }
  const proto = location.protocol === "https:" ? "wss" : "ws";
  ws = new WebSocket(`${proto}://${location.host}/ws/terminal`);
  ws.binaryType = "arraybuffer";

  ws.onopen = () => {
    try {
      fitAddon.fit();
    } catch (e) {}
    sendResize();
    term.focus();
  };
  ws.onmessage = (ev) => {
    if (ev.data instanceof ArrayBuffer) {
      term.write(new Uint8Array(ev.data));
    } else if (typeof ev.data === "string") {
      term.write(ev.data);
    }
  };
  ws.onclose = () => {
    term.write("\r\n\x1b[33m[连接已断开，可点击“重连”重新连接]\x1b[0m\r\n");
  };
  ws.onerror = () => {};
}

function sendResize() {
  if (ws && ws.readyState === WebSocket.OPEN && term) {
    ws.send(JSON.stringify({ t: "resize", cols: term.cols, rows: term.rows }));
  }
}

/* ---------------- 初始化 ---------------- */

document.addEventListener("DOMContentLoaded", () => {
  $$(".tab").forEach((t) => {
    t.addEventListener("click", () => switchTab(t.dataset.tab));
  });

  applyTheme(currentTheme(), false);
  $("#theme-toggle").addEventListener("click", () => {
    applyTheme(currentTheme() === "light" ? "dark" : "light");
  });

  $("#top-sort").addEventListener("click", (e) => {
    const btn = e.target.closest("button[data-sort]");
    if (!btn) return;
    topSort = btn.dataset.sort;
    renderTop();
  });

  $("#login-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const pw = $("#login-password").value;
    const errEl = $("#login-error");
    errEl.textContent = "";
    try {
      const res = await fetch("/api/login", {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ password: pw }),
      });
      const data = await res.json().catch(() => ({}));
      if (!res.ok) {
        errEl.textContent = data.error || "登录失败";
        return;
      }
      $("#login-password").value = "";
      hideLogin();
      switchTab(TABS.includes(location.hash.slice(1)) ? location.hash.slice(1) : "overview");
    } catch (err) {
      errEl.textContent = "网络错误：" + err.message;
    }
  });

  $("#logout").addEventListener("click", async () => {
    try {
      await fetch("/api/logout", { method: "POST" });
    } catch (e) {}
    location.reload();
  });

  $("#projects-refresh").addEventListener("click", () => {
    loadProjects().catch((e) => toast(e.message, true));
  });

  $("#add-project").addEventListener("click", openAddProject);
  $("#add-type").addEventListener("change", syncAddType);
  $("#add-project-close").addEventListener("click", closeAddProject);
  $("#add-project-cancel").addEventListener("click", closeAddProject);
  $("#add-project-modal").addEventListener("click", (e) => {
    if (e.target === $("#add-project-modal")) closeAddProject();
  });
  $("#add-project-form").addEventListener("submit", submitAddProject);

  $("#nginx-test").addEventListener("click", () => nginxAction("test"));
  $("#nginx-reload").addEventListener("click", () => nginxAction("reload"));

  $("#nginx-group").addEventListener("change", async () => {
    if (!confirmDiscardNginx()) {
      $("#nginx-group").value = nginxGroupId;
      return;
    }
    nginxGroupId = $("#nginx-group").value;
    localStorage.setItem("pm_nginx_group", nginxGroupId);
    resetNginxEditor();
    await loadNginxFiles();
  });

  $("#nginx-save").addEventListener("click", saveNginxFile);
  $("#nginx-revert").addEventListener("click", () => {
    $("#nginx-editor").value = nginxOriginal;
    setNginxDirty(false);
  });

  const nginxEditor = $("#nginx-editor");
  nginxEditor.addEventListener("input", () => {
    setNginxDirty(nginxEditor.value !== nginxOriginal);
  });
  nginxEditor.addEventListener("keydown", (e) => {
    if (e.key === "Tab") {
      e.preventDefault();
      const start = nginxEditor.selectionStart;
      const end = nginxEditor.selectionEnd;
      nginxEditor.setRangeText("    ", start, end, "end");
      setNginxDirty(nginxEditor.value !== nginxOriginal);
    }
  });

  window.addEventListener("beforeunload", (e) => {
    if (nginxDirty) {
      e.preventDefault();
      e.returnValue = "";
    }
  });

  $("#log-close").addEventListener("click", closeLogs);
  $("#log-modal").addEventListener("click", (e) => {
    if (e.target === $("#log-modal")) closeLogs();
  });

  $("#term-reconnect").addEventListener("click", () => {
    term && term.write("\r\n\x1b[36m[重新连接…]\x1b[0m\r\n");
    connectTerm();
  });
  $("#term-clear").addEventListener("click", () => {
    term && term.clear();
  });

  document.addEventListener("keydown", (e) => {
    if (e.key === "Escape" && !$("#log-modal").classList.contains("hidden")) closeLogs();
    if (e.key === "Escape" && !$("#add-project-modal").classList.contains("hidden")) closeAddProject();
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "s" && currentTab === "nginx") {
      e.preventDefault();
      saveNginxFile();
    }
  });

  window.addEventListener("hashchange", () => {
    const t = location.hash.slice(1);
    if (TABS.includes(t) && t !== currentTab) switchTab(t);
  });

  boot();
});
