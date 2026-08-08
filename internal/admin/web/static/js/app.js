// app.js —— QQBot 管理后台前端（原生 ES Modules，hash 路由）。
import { api, initSession, idemKey, esc, fmtTime, fmtDur, badge } from "./api.js";

const app = document.getElementById("app");
let session = null;

// ---------- 基础设施 ----------
function toast(msg, isErr = false) {
  const el = document.createElement("div");
  el.className = "toast";
  el.style.background = isErr ? "rgba(248,113,113,.16)" : undefined;
  el.style.borderColor = isErr ? "rgba(248,113,113,.5)" : undefined;
  el.textContent = msg;
  document.body.appendChild(el);
  setTimeout(() => el.remove(), 3000);
}

function statusBadge(s) {
  const map = {
    connected: ["ok", "已连接"], connecting: ["warn", "连接中"],
    reconnecting: ["warn", "重连中"], disconnected: ["warn", "未连接"],
    error: ["bad", "异常"],
  };
  const [cls, label] = map[s] || ["warn", s];
  return `<span class="badge ${cls}">${label}</span>`;
}

function on(html, cb) {
  app.innerHTML = html;
  app.querySelectorAll("[data-action]").forEach((el) => {
    el.addEventListener("click", (e) => cb(el.dataset.action, el, e));
  });
  app.querySelectorAll("form[data-submit]").forEach((f) => {
    f.addEventListener("submit", (e) => {
      e.preventDefault();
      cb(f.dataset.submit, f, e);
    });
  });
}

// ---------- 登录 / Setup ----------
async function viewLogin() {
  let s;
  try { s = await api("GET", "/api/v1/auth/session"); } catch { s = null; }
  if (s && s.authenticated) { location.hash = "#/overview"; return; }
  const needSetup = s && s.setup_required;
  on(`
    <div class="auth-screen card">
      <h2>${needSetup ? "首次初始化" : "登录管理后台"}</h2>
      ${needSetup ? `
        <p class="muted">从启动日志中复制 setup token（仅显示一次）。</p>
        <form data-submit="setup">
          <label>Setup Token</label>
          <input type="text" name="setup_token" required autocomplete="off">
          <label>管理员密码</label>
          <input type="password" name="password" required autocomplete="new-password">
          <div class="row" style="margin-top:14px">
            <button type="submit">完成初始化</button>
          </div>
        </form>` : `
        <form data-submit="login" id="login-form">
          <label>密码</label>
          <input type="password" name="password" required autocomplete="current-password">
          <div class="row" style="margin-top:14px">
            <button type="submit">登录</button>
          </div>
        </form>
        <div id="verify-box" style="display:none">
          <p class="muted" id="verify-tip"></p>
          <form data-submit="verify" id="verify-form">
            <label>邮箱验证码</label>
            <input type="text" name="code" required autocomplete="one-time-code" inputmode="numeric" maxlength="6" placeholder="6 位数字">
            <div class="row" style="margin-top:14px">
              <button type="submit">验证并登录</button>
              <button type="button" class="secondary" id="resend-btn">重新发送</button>
              <button type="button" class="secondary" id="back-btn">← 返回</button>
            </div>
          </form>
        </div>`}
    </div>`, async (action, form) => {
    const body = Object.fromEntries(new FormData(form));
    try {
      if (action === "setup") {
        await api("POST", "/api/v1/auth/setup", body);
        location.hash = "#/overview";
        return;
      }
      if (action === "login") {
        const res = await api("POST", "/api/v1/auth/login", body);
        if (res.step === "verify") {
          // 进入第二步：邮箱验证码
          window.__mfaTicket = res.ticket;
          document.getElementById("login-form").style.display = "none";
          const vb = document.getElementById("verify-box");
          vb.style.display = "block";
          document.getElementById("verify-tip").textContent =
            `✅ 密码正确，验证码已发送至 ${res.email}（10 分钟内有效）`;
          const rb = document.getElementById("resend-btn");
          let left = res.resend_after || 60;
          const tick = setInterval(() => {
            left--;
            if (left <= 0) {
              clearInterval(tick);
              rb.disabled = false;
              rb.textContent = "重新发送";
            } else {
              rb.disabled = true;
              rb.textContent = `重新发送（${left}s）`;
            }
          }, 1000);
          return;
        }
        location.hash = "#/overview";
      }
      if (action === "verify") {
        await api("POST", "/api/v1/auth/verify", {
          ticket: window.__mfaTicket, code: String(body.code || "").trim(),
        });
        location.hash = "#/overview";
      }
    } catch (e) { toast(e.message, true); }
  });
  // 重发 / 返回按钮（独立绑定，避免 data-submit 表单逻辑干扰）
  document.getElementById("resend-btn")?.addEventListener("click", async () => {
    try {
      const res = await api("POST", "/api/v1/auth/login", { password: "" });
      // 密码必填校验走后端；改用专用重发语义：直接再走一次密码？——
      // 简化：重发 = 重新提交密码（用户已输过，用表单值）
      const pw = document.querySelector("#login-form input[name=password]").value;
      const r2 = await api("POST", "/api/v1/auth/login", { password: pw });
      window.__mfaTicket = r2.ticket;
      document.getElementById("verify-tip").textContent =
        `✅ 验证码已重新发送至 ${r2.email}`;
    } catch (e) { toast(e.message, true); }
  });
  document.getElementById("back-btn")?.addEventListener("click", () => {
    document.getElementById("verify-box").style.display = "none";
    document.getElementById("login-form").style.display = "";
  });
}

// ---------- 布局 ----------
function shell(content, active) {
  const nav = [
    ["#/overview", "总览", "overview"],
    ["#/groups", "群管理", "groups"],
    ["#/actions", "人工群管", "actions"],
    ["#/audit", "审计", "audit"],
    ["#/settings", "系统设置", "settings"],
    ["#/civgo", "civgo 问答", "civgo"],
  ].map(([href, label, key]) =>
    `<a href="${href}" class="${active === key ? "active" : ""}">${label}</a>`).join("");
  return `
    <div class="topbar">
      <h1>QQBot 管理后台</h1>
      <div class="spacer"></div>
      ${nav}
      <button class="secondary" data-action="logout">退出</button>
    </div>
    ${content}`;
}

// ---------- 总览 ----------
async function viewOverview() {
  let st;
  try { st = await api("GET", "/api/v1/status"); } catch (e) { toast(e.message, true); return; }
  on(shell(`
    <div class="content">
      <div class="card">
        <h2>连接状态</h2>
        <table>
          <tr><th>OneBot</th><td>${statusBadge(st.onebot.status)}
            ${st.onebot.last_error ? `<span class="muted">${esc(st.onebot.last_error)}</span>` : ""}
            <span class="muted">${esc(st.onebot.ws_url)}</span></td></tr>
          <tr><th>NapCat</th><td>${st.napcat.configured ? "已配置" : "未配置"}</td></tr>
          <tr><th>运行时间</th><td>${fmtDur(st.uptime_seconds)}</td></tr>
          <tr><th>配置版本</th><td>revision ${st.config_revision}</td></tr>
          <tr><th>群数量</th><td>已启用 ${st.groups.enabled} / 共 ${st.groups.total}</td></tr>
        </table>
      </div>
      <div class="card"><h2>NapCat 登录</h2><div id="napcat-box"></div></div>
      <div class="card">
        <h2>最近失败 / 结果未知动作</h2>
        ${st.recent_action_failures?.length ? `<table>
          <tr><th>时间</th><th>群</th><th>动作</th><th>结果</th><th>详情</th></tr>
          ${st.recent_action_failures.map((f) => `<tr>
            <td>${fmtTime(f.time)}</td><td>${f.group_id}</td><td>${esc(f.action)}</td>
            <td>${badge(f.result)}</td><td class="muted">${esc(f.detail)}</td></tr>`).join("")}
        </table>` : `<div class="empty">暂无失败记录</div>`}
      </div>
    </div>`, "overview"), async (action, el) => {
    if (action === "logout") { await logout(); }
  });
  renderNapcat();
}

async function renderNapcat() {
  const box = document.getElementById("napcat-box");
  if (!box) return;
  try {
    const st = await api("GET", "/api/v1/napcat/status");
    if (st.is_login) {
      box.innerHTML = `<p>✅ 已登录 <button class="secondary" data-action="refresh-qr" style="display:none"></button></p>`;
      return;
    }
    const qr = await api("GET", "/api/v1/napcat/qrcode.png");
    // 二维码直接以 <img> 引用接口（no-store，带 cookie）
    box.innerHTML = `
      <div class="qrcode"><img src="/api/v1/napcat/qrcode.png" alt="登录二维码"></div>
      <div class="row" style="margin-top:10px">
        <button class="secondary" data-action="refresh-qr">刷新二维码</button>
        <span class="muted">用手机 QQ 扫码登录；二维码约 1~2 分钟过期</span>
      </div>`;
    box.querySelector("[data-action=refresh-qr]").addEventListener("click", async () => {
      try {
        await api("POST", "/api/v1/napcat/qrcode/refresh");
        const img = box.querySelector("img");
        img.src = "/api/v1/napcat/qrcode.png?t=" + Date.now();
        toast("二维码已刷新");
      } catch (e) { toast(e.message, true); }
    });
  } catch (e) {
    box.innerHTML = `<p class="muted">${esc(e.message)}</p>`;
  }
}

// ---------- 群管理 ----------

// ruleMeta 缓存（GET /api/v1/rule-meta，条件/动作类型注册表）。
let ruleMeta = null;
async function getRuleMeta() {
  if (!ruleMeta) ruleMeta = await api("GET", "/api/v1/rule-meta");
  return ruleMeta;
}

// 条件/动作类型查询。
const metaType = (list, type) => list.find((x) => x.type === type) || null;

// 条件摘要（规则列表展示）。
function condSummary(c, meta) {
  const spec = metaType(meta.conditions, c.type);
  const label = spec ? spec.label : c.type;
  const p = c.params || {};
  const parts = [];
  if (c.type === "text_contains" || c.type === "request_contains") parts.push(`"${p.text}"`);
  if (c.type === "text_regex" || c.type === "request_regex") parts.push(p.pattern);
  if (c.type === "flood") parts.push(`${p.window_sec}s/${p.max_count}条`);
  if (c.type === "text_repeat") parts.push(`${p.window_sec}s/${p.min_count}次`);
  if (c.type === "strike_count") parts.push(`${p.counter_id}≥${p.min_count}`);
  if (c.type === "time_between") parts.push(`${p.start}~${p.end}`);
  if (c.type === "user_role") parts.push((p.roles || []).join("/"));
  if (c.type === "user_id") parts.push(p.user_id);
  if (c.type === "user_id_in") parts.push((p.user_ids || []).length + " 个 QQ");
  if (c.type === "message_has_type") parts.push((p.types || []).join("/"));
  if (c.type === "subtype_is") parts.push(p.subtype);
  if (c.type === "is_exempt") parts.push("群主/管理/白名单");
  if (c.type === "always") parts.push("无条件");
  if (c.type === "text_length") {
    if (p.min) parts.push(`≥${p.min}字`);
    if (p.max) parts.push(`≤${p.max}字`);
  }
  if (c.type === "url_count" || c.type === "image_count" || c.type === "at_count") parts.push(`≥${p.min_count}`);
  if (c.type === "weekday") parts.push((p.days || []).map((d) => ["周日","周一","周二","周三","周四","周五","周六"][d]).join("/"));
  if (c.type === "probability") parts.push(`${p.percent}%`);
  if (c.type === "user_joined_within") parts.push(`${p.days}天`);
  if (c.type === "inviter_is") parts.push(p.user_id);
  return (c.negate ? "非" : "") + label + (parts.length ? `(${parts.join(" ")})` : "");
}

// 动作摘要（规则列表展示）。
function actSummary(a, meta) {
  const spec = metaType(meta.actions, a.type);
  const label = spec ? spec.label : a.type;
  const p = a.params || {};
  const parts = [];
  if (a.type === "warn") parts.push(`"${p.reason || ""}"`);
  if (a.type === "mute") parts.push(`${p.minutes}分` + (p.reason ? `(${p.reason})` : ""));
  if (a.type === "kick") parts.push(p.reason || "");
  if (a.type === "send_message") parts.push(`"${trunc(p.message || "", 24)}"` + (p.at ? "@" : ""));
  if (a.type === "reject_join") parts.push(p.reason || "");
  if (a.type === "increment_counter") parts.push(`${p.counter_id}+${p.step || 1}`);
  if (a.type === "decrement_counter") parts.push(`${p.counter_id}-${p.step || 1}`);
  if (a.type === "reset_counter" || a.type === "set_counter") parts.push(p.counter_id);
  if (a.type === "send_private") parts.push(`"${trunc(p.message || "", 24)}"`);
  if (a.type === "whole_ban") parts.push(p.enable ? "开" : "关");
  if (a.type === "card") parts.push(`"${trunc(p.card || "", 16)}"` || "清空");
  return label + (parts.length ? `(${parts.join(" ")})` : "");
}

const eventLabel = (ev) => ({
  message: "群消息", group_increase: "新人入群", group_request: "加群申请",
}[ev] || ev);

function trunc(s, n) {
  s = String(s || "");
  return s.length > n ? s.slice(0, n) + "…" : s;
}

async function viewGroups() {
  const data = await api("GET", "/api/v1/groups");
  // 群列表排序：启用的在前，按群号升序（展示稳定、便于查找）
  const groups = (data.groups || []).slice().sort((a, b) => {
    if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
    return a.group_id - b.group_id;
  });
  const hash = location.hash;
  const m = hash.match(/^#\/groups\/(\d+)$/);
  let gid = m ? Number(m[1]) : 0;
  if (!gid) {
    // 默认选中逻辑：URL 指定 > 上次查看的群 > 唯一群自动展开
    const last = Number(sessionStorage.getItem("group_last") || 0);
    if (last && groups.some((g) => String(g.group_id) === String(last))) gid = last;
    else if (groups.length === 1) gid = groups[0].group_id;
  }
  if (gid && groups.some((g) => String(g.group_id) === String(gid))) {
    return viewGroupDetail(gid, data);
  }
  on(shell(`
    <div class="layout">
      <div class="sidebar">
        <button class="group-item" data-action="new-group" style="color:var(--accent)">＋ 新增群</button>
        ${groups.map((g) => `
          <button class="group-item ${String(g.group_id) === m?.[1] ? "active" : ""}"
                  data-action="open-group" data-gid="${g.group_id}">
            ${esc(g.remark || g.group_id)} <span class="tag">${g.group_id}${g.enabled ? "" : " · 停用"}</span>
          </button>`).join("")}
      </div>
      <div class="content">
        ${groups.length ? `<div class="empty">选择左侧群查看/编辑配置</div>`
          : `<div class="empty">尚未配置任何群。点击「＋ 新增群」添加第一个群。</div>`}
      </div>
    </div>`, "groups"), async (action, el) => {
    if (action === "logout") { await logout(); return; }
    if (action === "open-group") { location.hash = "#/groups/" + el.dataset.gid; }
    if (action === "new-group") {
      const gid = prompt("输入群号");
      if (!gid || !/^\d+$/.test(gid)) return;
      try {
        await api("POST", "/api/v1/groups", {
          group: { group_id: Number(gid), enabled: true },
        });
        toast("已创建（含默认豁免/邀请规则）");
        location.hash = "#/groups/" + gid;
      } catch (e) { toast(e.message, true); }
    }
  });
}

// 规则行模板（data-i 为数组索引，删除/排序后重渲染保证索引正确）。
function ruleRowsHTML(rules, meta) {
  return (rules || []).map((r, i) => `
    <div class="rule-row" data-i="${i}">
      <div class="rule-head">
        <label class="switch"><input type="checkbox" data-f="enabled" ${r.enabled ? "checked" : ""}><span class="slider"></span></label>
        <strong>${esc(r.name || "（未命名）")}</strong>
        <span class="tag">${eventLabel(r.event)}</span>
        <span class="tag ${r.break ? "tag-warn" : "tag-ok"}">${r.break ? "停止" : "继续"}</span>
        <span class="spacer"></span>
        <button type="button" class="secondary" data-rule-action="up">↑</button>
        <button type="button" class="secondary" data-rule-action="down">↓</button>
        <button type="button" class="secondary" data-rule-action="edit">编辑</button>
        <button type="button" class="danger" data-rule-action="del">删</button>
      </div>
      <div class="rule-meta">
        <div class="muted">条件${r.when_mode === "any" ? "（任一满足 OR）" : "（全部满足 AND）"}：${(r.when || []).length ? (r.when || []).map((c) => `<span class="chip">${esc(condSummary(c, meta))}</span>`).join(" ") : `<span class="chip">无条件</span>`}</div>
        <div class="muted">动作：${(r.then || []).length ? (r.then || []).map((a) => `<span class="chip">${esc(actSummary(a, meta))}</span>`).join(" ") : `<span class="chip">无（过滤器）</span>`}</div>
      </div>
    </div>`).join("");
}

// 群配置表单：基础信息 + 规则列表。
function groupForm(g, meta) {
  const rules = ruleRowsHTML(g.rules, meta) || `<div class="empty">暂无规则</div>`;
  return `
  <form data-submit="save-group">
    <input type="hidden" name="revision" value="${g.revision}">
    <div class="card">
      <h2>群 ${esc(g.remark || g.group_id)}（${g.group_id}）
        <label class="switch" style="float:right"><input type="checkbox" name="enabled" ${g.enabled ? "checked" : ""}><span class="slider"></span></label>
      </h2>
      <div class="row">
        <label>群备注<input type="text" name="remark" value="${esc(g.remark || "")}" maxlength="32"></label>
        <label style="min-width:200px">白名单（逗号分隔 QQ）<input type="text" name="whitelist" value="${(g.whitelist || []).join(",")}"></label>
      </div>
    </div>
    <div class="card">
      <h2>管理规则（${(g.rules || []).length}）
        <button type="button" class="secondary" data-action="add-upgrade" style="float:right;margin-left:8px" title="自动生成「计数+1 → 累计升级处罚 → 清零」三条规则">⚡ 升级处罚模板</button>
        <button type="button" class="secondary" data-action="add-rule" style="float:right">＋ 添加规则</button>
      </h2>
      <p class="muted" style="margin-bottom:8px">规则按顺序匹配：全部条件满足后执行动作，
        「停止」表示不再匹配后续规则。条件可组合（AND/OR、可逐条取反），动作按顺序执行。
        模板变量：{nickname} {user_id} {group_id} {bot_name} {matched_text} {rule_name} {count}。
        升级处罚（如"累计 3 次才禁言"）用「⚡ 升级处罚模板」一键生成。</p>
      <div id="rule-list">${rules || `<div class="empty">暂无规则</div>`}</div>
      <details style="margin-top:12px">
        <summary class="muted">计数器实时状态（升级处罚排障）</summary>
        <div id="counters-box"><p class="muted">加载中…</p></div>
      </details>
    </div>
    <div class="row">
      <button type="submit">保存配置</button>
      <button type="button" class="danger" data-action="reset-group">重置为默认</button>
      <button type="button" class="danger" data-action="delete-group">删除群</button>
      <span class="muted">保存时将携带 revision=${g.revision}，冲突会提示重新加载</span>
    </div>
  </form>`;
}

// 收集本群规则中已使用的计数器 ID（编辑器下拉建议用）。
function counterCandidates(g) {
  const set = new Set();
  for (const r of (g.rules || [])) {
    for (const c of (r.when || [])) {
      if (c.type === "strike_count" && c.params?.counter_id) set.add(c.params.counter_id);
    }
    for (const a of (r.then || [])) {
      if (a.params?.counter_id && ["increment_counter", "reset_counter", "set_counter", "decrement_counter", "warn"].includes(a.type)) {
        set.add(a.params.counter_id);
      }
    }
  }
  return [...set];
}

// 规则编辑器模态（动态参数表单）。
function openRuleEditor(gid, rule, meta, onSave, counterIds) {
  const isNew = !rule;
  const state = isNew
    ? { id: "r" + Date.now().toString(36), name: "", enabled: true, event: "message", when_mode: "all", when: [], then: [], break: true }
    : JSON.parse(JSON.stringify(rule));
  if (!state.when_mode) state.when_mode = "all";

  const typesFor = (list, ev) => list.filter((t) => (t.events || []).includes(ev));
  const condTypes = () => typesFor(meta.conditions, state.event);
  const actTypes = () => typesFor(meta.actions, state.event);

  const paramField = (key, spec, value) => {
    const v = value !== undefined ? value : (spec.default !== undefined ? spec.default : "");
    const common = `data-param="${key}"`;
    // 计数器 ID：下拉建议（本群已用 ID + 常用名）
    if (key === "counter_id") {
      const dlId = "cid-list-" + Math.random().toString(36).slice(2, 8);
      const opts = [...new Set([...(counterIds || []), "kw", "flood", "warn"])]
        .map((c) => `<option value="${esc(c)}">`).join("");
      return `<label>${esc(spec.title || key)}
        <input type="text" ${common} list="${dlId}" value="${esc(v)}" maxlength="32" placeholder="计数器 ID，如 kw">
        <datalist id="${dlId}">${opts}</datalist>
        <span class="muted">建议：${opts ? [...new Set([...(counterIds || []), "kw"])].slice(0, 3).join(" / ") : "kw"}</span>
      </label>`;
    }
    switch (spec.type) {
      case "boolean":
        return `<label class="inline"><input type="checkbox" ${common} ${v ? "checked" : ""}> ${esc(spec.title || key)}</label>`;
      case "integer":
        return `<label>${esc(spec.title || key)}<input type="number" ${common} value="${v}" min="${spec.minimum ?? ""}" max="${spec.maximum ?? ""}" style="width:110px"></label>`;
      case "array": {
        const items = spec.items || {};
        if (items.enum) {
          const opts = items.enum.map((o) =>
            `<label class="inline"><input type="checkbox" value="${o}" ${(v || []).includes(o) ? "checked" : ""}> ${o}</label>`).join(" ");
          return `<div class="param-arr" data-param="${key}"><span class="muted">${esc(spec.title || key)}</span> ${opts}</div>`;
        }
        return `<label>${esc(spec.title || key)}<input type="text" data-param="${key}" data-mode="tags" data-num="${items.type === "integer" ? "1" : ""}" value="${esc((v || []).join(","))}" placeholder="逗号分隔"></label>`;
      }
      default:
        if (spec.enum) {
          return `<label>${esc(spec.title || key)}<select ${common}>${spec.enum.map((o) => `<option ${o === v ? "selected" : ""}>${o}</option>`).join("")}</select></label>`;
        }
        if (spec.multiline) {
          return `<label>${esc(spec.title || key)}<textarea ${common} rows="2">${esc(v)}</textarea></label>`;
        }
        return `<label>${esc(spec.title || key)}<input type="text" ${common} value="${esc(v)}" ${spec.maxLength ? `maxlength="${spec.maxLength}"` : ""}></label>`;
    }
  };

  const collectParams = (container) => {
    const params = {};
    container.querySelectorAll("[data-param]").forEach((el) => {
      const key = el.dataset.param;
      if (el.type === "checkbox") { params[key] = el.checked; return; }
      if (el.type === "number") {
        if (el.value !== "") params[key] = Number(el.value);
        return;
      }
      if (el.tagName === "INPUT" && el.dataset.mode === "tags") {
        const arr = el.value.split(/[,，\s]+/).filter(Boolean);
        if (arr.length) params[key] = el.dataset.num ? arr.map(Number) : arr;
        return;
      }
      if (el.classList.contains("param-arr")) { return; } // 容器
      params[key] = el.value; // 始终收集（含空串，由后端/必填校验兜底）
    });
    // 数组多选（checkbox 组）
    container.querySelectorAll(".param-arr").forEach((box) => {
      const key = box.dataset.param;
      if (!key) return;
      const arr = [...box.querySelectorAll("input:checked")].map((c) => c.value);
      if (arr.length) params[key] = arr;
    });
    return params;
  };

  const rowHTML = (kind, item, idx) => {
    const list = kind === "cond" ? meta.conditions : meta.actions;
    const types = kind === "cond" ? condTypes() : actTypes();
    const spec = metaType(list, item.type);
    return `
    <div class="rule-item" data-kind="${kind}" data-idx="${idx}">
      <div class="row" style="gap:8px;align-items:flex-end">
        <label style="min-width:130px">${kind === "cond" ? "条件类型" : "动作类型"}
          <select data-f="type">
            ${types.map((t) => `<option value="${t.type}" ${t.type === item.type ? "selected" : ""}>${esc(t.label)}</option>`).join("")}
          </select>
        </label>
        ${kind === "cond" ? `<button type="button" class="negate-btn ${item.negate ? "on" : ""}" data-f="negate" title="取反：满足该条件时视为不满足">非</button>` : ""}
        <div class="param-fields" style="flex:1;display:flex;gap:8px;flex-wrap:wrap;align-items:flex-end">
          ${spec && spec.params_schema ? Object.entries(spec.params_schema.properties || {}).map(([k, s]) => paramField(k, s, item.params?.[k])).join("") : ""}
        </div>
        <button type="button" class="danger" data-item-action="del">删</button>
      </div>
    </div>`;
  };

  const rerender = () => {
    const box = document.getElementById("rule-editor-items");
    if (!box) return;
    // 重建前先把当前 DOM 输入同步回 state（否则添加/删除条目会丢已填内容）
    for (const item of box.querySelectorAll(".rule-item")) {
      const kind = item.dataset.kind;
      const target = kind === "cond" ? state.when : state.then;
      const entry = target[Number(item.dataset.idx)];
      if (!entry) continue;
      const typeEl = item.querySelector("[data-f=type]");
      if (typeEl) entry.type = typeEl.value;
      const neg = item.querySelector("[data-f=negate]");
      if (neg) entry.negate = neg.classList.contains("on");
      const fields = item.querySelector(".param-fields");
      if (fields) entry.params = collectParams(fields);
    }
    // 切换事件时丢弃不适用类型（设计文档 §11.2）
    state.when = state.when.filter((c) => metaType(meta.conditions, c.type) && metaType(meta.conditions, c.type).events.includes(state.event));
    state.then = state.then.filter((a) => metaType(meta.actions, a.type) && metaType(meta.actions, a.type).events.includes(state.event));
    const allMode = state.when_mode !== "any";
    // 计数机制上下文提示
    const strikeIds = state.when.filter((c) => c.type === "strike_count").map((c) => c.params?.counter_id).filter(Boolean);
    const incIds = state.then.filter((a) => a.type === "increment_counter").map((a) => a.params?.counter_id).filter(Boolean);
    const hasReset = state.then.some((a) => a.type === "reset_counter");
    const hints = [];
    if (strikeIds.length && !incIds.length) {
      hints.push(`「累计计数」需要配合：在本规则之前放一条规则，用「计数+N」动作给 ${strikeIds.join("/")} 加分（每次命中 +1）`);
    }
    if (incIds.length && !strikeIds.length && !hasReset) {
      hints.push(`「计数+N」只负责加分；还需「累计计数≥N」条件检查达标，并在处罚后用「计数清零」重置`);
    }
    if (strikeIds.length && hasReset) {
      hints.push("升级处罚链：前置规则「计数+N」→ 本规则「累计计数≥N」→ 处罚后「计数清零」，循环生效");
    }
    box.innerHTML =
      `<div class="row" style="gap:10px;margin:6px 0">
        <span class="muted">条件组合：</span>
        <label class="inline"><input type="radio" name="when-mode" value="all" ${allMode ? "checked" : ""}> 全部满足（AND）</label>
        <label class="inline"><input type="radio" name="when-mode" value="any" ${!allMode ? "checked" : ""}> 任一满足（OR）</label>
        <span class="muted">· 空条件 = 恒真 · 每行「非」= 取反</span>
      </div>` +
      (hints.length ? `<div class="hint-box">${hints.map((h) => `💡 ${esc(h)}`).join("<br>")}</div>` : "") +
      (state.when.length ? state.when.map((c, i) => rowHTML("cond", c, i)).join("") : `<div class="empty" style="padding:8px">无条件</div>`) +
      `<button type="button" class="secondary" data-action="add-cond">＋ 添加条件</button>` +
      `<div class="muted" style="margin:14px 0 6px">动作（按顺序执行；空 = 过滤器规则）</div>` +
      (state.then.length ? state.then.map((a, i) => rowHTML("act", a, i)).join("") : `<div class="empty" style="padding:8px">无动作</div>`) +
      `<button type="button" class="secondary" data-action="add-act">＋ 添加动作</button>`;
  };

  const bd = document.createElement("div");
  bd.className = "modal-backdrop";
  bd.innerHTML = `
    <div class="modal">
      <h2>${isNew ? "添加规则" : "编辑规则"}</h2>
      <div class="row">
        <label style="flex:2">规则名称<input type="text" id="rule-name" value="${esc(state.name)}" maxlength="64" placeholder="如：广告关键词警告"></label>
        <label>触发事件
          <select id="rule-event">
            ${meta.events.map((e) => `<option value="${e.name}" ${e.name === state.event ? "selected" : ""}>${esc(e.label)}</option>`).join("")}
          </select>
        </label>
        <label>执行后
          <select id="rule-flow">
            <option value="break" ${state.break ? "selected" : ""}>停止匹配后续规则</option>
            <option value="continue" ${!state.break ? "selected" : ""}>继续匹配后续规则</option>
          </select>
        </label>
        <label class="inline" style="margin-bottom:10px"><input type="checkbox" id="rule-enabled" ${state.enabled ? "checked" : ""}> 启用</label>
      </div>
      <div id="rule-editor-items"></div>
      <p class="muted" style="margin-top:10px">💡 计数机制：同一 counter_id 的多条规则组成升级处罚链——
        前置规则用「计数+N」加分（执行后选"继续"），本规则用「累计计数≥N」检查，处罚后用「计数清零」重置循环。
        不想手拼？返回列表用「⚡ 升级处罚模板」一键生成。移出/撤回为危险动作。</p>
      <div class="row" style="margin-top:14px">
        <button id="rule-save">保存规则</button>
        <button type="button" class="secondary" id="rule-cancel">取消</button>
      </div>
    </div>`;
  document.body.appendChild(bd);

  rerender();

  // 事件切换：重渲染类型列表（并清理不适用条目）
  document.getElementById("rule-event").addEventListener("change", (e) => {
    state.event = e.target.value;
    rerender();
  });
  document.getElementById("rule-name").addEventListener("input", (e) => { state.name = e.target.value; });
  document.getElementById("rule-flow").addEventListener("change", (e) => { state.break = e.target.value === "break"; });
  document.getElementById("rule-enabled").addEventListener("change", (e) => { state.enabled = e.target.checked; });

  bd.querySelector("#rule-editor-items").addEventListener("click", (e) => {
    // 取反按钮（胶囊）：点击切换
    const negBtn = e.target.closest("[data-f=negate]");
    if (negBtn) {
      negBtn.classList.toggle("on");
      // 同步回 state（避免重渲染时丢失）
      const item = negBtn.closest(".rule-item");
      const target = item.dataset.kind === "cond" ? state.when : state.then;
      const entry = target[Number(item.dataset.idx)];
      if (entry) entry.negate = negBtn.classList.contains("on");
      return;
    }
    // 条件/动作行内的删除按钮（data-item-action）优先处理，
    // 避免被 data-action 过滤拦截导致“点不动”
    const delBtn = e.target.closest("[data-item-action=del]");
    if (delBtn) {
      const item = delBtn.closest(".rule-item");
      if (!item) return;
      const idx = Number(item.dataset.idx);
      if (idx < 0) return;
      if (item.dataset.kind === "cond") state.when.splice(idx, 1);
      else state.then.splice(idx, 1);
      rerender();
      return;
    }
    const btn = e.target.closest("[data-action]");
    if (!btn) return;
    const kind = btn.dataset.action;
    if (kind === "add-cond") {
      state.when.push({ type: condTypes()[0]?.type || "text_contains", negate: false, params: {} });
      rerender();
    }
    if (kind === "add-act") {
      const types = actTypes();
      const pick = types.find((t) => t.type === "send_message") || types[0];
      state.then.push({ type: pick?.type || "noop", params: {} });
      rerender();
    }
  });
  // 类型切换：重建参数表单
  bd.querySelector("#rule-editor-items").addEventListener("change", (e) => {
    // 条件组合模式（AND/OR）
    if (e.target.name === "when-mode") {
      state.when_mode = e.target.value;
      return;
    }
    const sel = e.target.closest("select[data-f=type]");
    if (!sel) return;
    const item = sel.closest(".rule-item");
    const kind = item.dataset.kind;
    const list = kind === "cond" ? meta.conditions : meta.actions;
    const spec = metaType(list, sel.value);
    // 危险动作二次确认
    if (kind === "act" && spec?.dangerous && !confirm(`动作「${spec.label}」为危险操作，确定添加？`)) {
      sel.value = item.dataset.prevType || (kind === "cond" ? "text_contains" : "noop");
      return;
    }
    item.dataset.prevType = sel.value;
    const target = kind === "cond" ? state.when : state.then;
    const idx = Number(item.dataset.idx);
    const entry = target[idx];
    if (!entry) return;
    entry.type = sel.value;
    entry.params = {};
    const fields = item.querySelector(".param-fields");
    fields.innerHTML = spec && spec.params_schema
      ? Object.entries(spec.params_schema.properties || {}).map(([k, s]) => paramField(k, s, undefined)).join("")
      : "";
  });

  bd.querySelector("#rule-cancel").addEventListener("click", () => bd.remove());
  bd.querySelector("#rule-save").addEventListener("click", () => {
    if (!state.name.trim()) { toast("请填写规则名称", true); return; }
    // 收集条件/动作参数（data-idx 按各自数组定位；必填缺失中断整个保存）
    for (const item of bd.querySelectorAll(".rule-item")) {
      const kind = item.dataset.kind;
      const list = kind === "cond" ? meta.conditions : meta.actions;
      const spec = metaType(list, item.querySelector("[data-f=type]").value);
      const target = kind === "cond" ? state.when : state.then;
      const idx = Number(item.dataset.idx);
      const entry = target[idx];
      if (!entry) { toast("规则数据异常，请重新打开编辑器", true); return; }
      const params = collectParams(item.querySelector(".param-fields"));
      if (spec?.params_schema?.required) {
        for (const k of spec.params_schema.required) {
          if (params[k] === undefined || params[k] === "") {
            toast(`请填写「${spec.params_schema.properties[k].title || k}」`, true);
            return;
          }
        }
      }
      entry.params = params;
      if (kind === "cond") entry.negate = item.querySelector("[data-f=negate]")?.classList.contains("on") || false;
    }
    // 条件组合模式
    state.when_mode = bd.querySelector('input[name="when-mode"]:checked')?.value || "all";
    // 危险动作再次确认
    for (const a of state.then) {
      if (metaType(meta.actions, a.type)?.dangerous && !confirm(`规则含危险动作「${metaType(meta.actions, a.type).label}」，确认保存？`)) return;
    }
    onSave(state);
    bd.remove();
  });
  bd.addEventListener("click", (e) => { if (e.target === bd) bd.remove(); });
}

async function viewGroupDetail(gid, data) {
  const g = data.groups.find((x) => String(x.group_id) === String(gid));
  if (!g) { location.hash = "#/groups"; return; }
  g.revision = data.revision;
  // 记住上次查看的群（下次进入群管理自动打开）
  sessionStorage.setItem("group_last", String(gid));
  // 左侧栏始终展示全部群（排序一致），可直接切换
  const allGroups = (data.groups || []).slice().sort((a, b) => {
    if (a.enabled !== b.enabled) return a.enabled ? -1 : 1;
    return a.group_id - b.group_id;
  });
  let meta;
  try { meta = await getRuleMeta(); } catch (e) { toast(e.message, true); return; }
  // 规则工作数组：所有增删改排序都同步到它，重渲染基于它（杜绝 DOM 与数据不同步）
  const rules = (g.rules || []).map((r) => JSON.parse(JSON.stringify(r)));
  const renderRuleList = () => {
    const list = document.getElementById("rule-list");
    if (!list) return;
    list.innerHTML = ruleRowsHTML(rules, meta) || `<div class="empty">暂无规则</div>`;
  };
  on(shell(`
    <div class="layout">
      <div class="sidebar">
        <button class="group-item" data-action="new-group" style="color:var(--accent)">＋ 新增群</button>
        ${allGroups.map((x) => `
          <button class="group-item ${String(x.group_id) === String(gid) ? "active" : ""}"
                  data-action="open-group" data-gid="${x.group_id}">
            ${esc(x.remark || x.group_id)} <span class="tag">${x.group_id}${x.enabled ? "" : " · 停用"}</span>
          </button>`).join("")}
      </div>
      <div class="content">${groupForm(g, meta)}</div>
    </div>`, "groups"), async (action, form) => {
    if (action === "logout") { await logout(); return; }
    if (action === "open-group") { location.hash = "#/groups/" + form.dataset.gid; return; }
    if (action === "new-group") {
      const ngid = prompt("输入群号");
      if (!ngid || !/^\d+$/.test(ngid)) return;
      try {
        await api("POST", "/api/v1/groups", {
          group: { group_id: Number(ngid), enabled: true },
        });
        toast("已创建（含默认豁免/邀请规则）");
        location.hash = "#/groups/" + ngid;
      } catch (e) { toast(e.message, true); }
      return;
    }
    if (action === "add-rule") {
      openRuleEditor(gid, null, meta, (rule) => {
        rules.push(rule);
        renderRuleList();
        toast("规则已添加，记得保存");
      }, counterCandidates(g));
    }
    if (action === "add-upgrade") {
      // 升级处罚模板：一键生成「计数+1 → 累计≥N 升级处罚+清零 → 普通警告」三条规则
      const kw = prompt("触发关键词（如：广告）");
      if (!kw || !kw.trim()) return;
      const times = prompt("累计几次后升级处罚？（如 3）", "3");
      if (!times || !/^\d+$/.test(times) || Number(times) < 2) return toast("累计次数需为 ≥2 的整数", true);
      const isKick = confirm("升级处罚用「移出群」？\n（取消 = 用禁言）");
      let minutes = 30;
      if (!isKick) {
        const m = prompt("升级禁言分钟数？", "30");
        if (m === null) return;
        minutes = Number(m) || 30;
      }
      const n = Number(times);
      const tpl = [
        {
          id: "r" + Date.now().toString(36) + "a", name: `计分-${kw.trim()}`, enabled: true,
          event: "message", when_mode: "all",
          when: [{ type: "text_contains", params: { text: kw.trim() } }],
          then: [{ type: "increment_counter", params: { counter_id: "kw", step: 1, window_hours: 24 } }],
          break: false,
        },
        {
          id: "r" + Date.now().toString(36) + "b", name: `${kw.trim()}升级${isKick ? "移出" : "禁言"}`, enabled: true,
          event: "message", when_mode: "all",
          when: [
            { type: "text_contains", params: { text: kw.trim() } },
            { type: "strike_count", params: { counter_id: "kw", min_count: n, window_hours: 24 } },
          ],
          then: isKick
            ? [{ type: "kick", params: { reason: `多次发布违规内容：${kw.trim()}` } },
               { type: "reset_counter", params: { counter_id: "kw" } }]
            : [{ type: "mute", params: { minutes, reason: `多次发布违规内容：${kw.trim()}` } },
               { type: "reset_counter", params: { counter_id: "kw" } }],
          break: true,
        },
        {
          id: "r" + Date.now().toString(36) + "c", name: `${kw.trim()}警告`, enabled: true,
          event: "message", when_mode: "all",
          when: [{ type: "text_contains", params: { text: kw.trim() } }],
          then: [{ type: "warn", params: { reason: `群内禁止发布内容：${kw.trim()}`, counter_id: "kw" } }],
          break: true,
        },
      ];
      rules.push(...tpl);
      renderRuleList();
      toast("已生成升级处罚模板（3 条规则），记得保存");
    }
    if (action === "reset-group") {
      if (!confirm("确定重置该群为默认配置（恢复豁免+邀请规则）？")) return;
      try {
        await api("POST", `/api/v1/groups/${gid}/reset`, {});
        toast("已重置"); location.hash = "#/groups";
      } catch (e) { toast(e.message, true); }
    }
    if (action === "delete-group") {
      if (!confirm("确定删除该群配置？")) return;
      try {
        await api("DELETE", `/api/v1/groups/${gid}`);
        toast("已删除"); location.hash = "#/groups";
      } catch (e) { toast(e.message, true); }
    }
    if (action === "save-group") {
      const fd = new FormData(form);
      // 把列表中的启停开关同步回工作数组
      document.querySelectorAll("#rule-list .rule-row").forEach((row) => {
        const i = Number(row.dataset.i);
        if (rules[i]) rules[i].enabled = row.querySelector("[data-f=enabled]").checked;
      });
      const group = {
        group_id: gid,
        enabled: fd.get("enabled") === "on",
        remark: fd.get("remark"),
        whitelist: String(fd.get("whitelist") || "").split(/[,，\s]+/).filter(Boolean).map(Number),
        rules,
      };
      try {
        await api("PUT", `/api/v1/groups/${gid}`, {
          revision: Number(fd.get("revision")),
          group,
        });
        toast("已保存（新 revision 生效）");
        location.hash = "#/groups/" + gid;
      } catch (e) {
        if (e.status === 409) {
          if (confirm("配置已被其他页面修改（revision 冲突）。重新加载最新配置？")) {
            location.hash = "#/groups/" + gid;
          }
        } else { toast(e.message, true); }
      }
    }
  });
  bindRuleRows(rules, renderRuleList);
  bindCounters(gid);
}

// 规则行事件：委托到 #rule-list 容器，按 data-i 操作工作数组并重渲染。
function bindRuleRows(rules, renderRuleList) {
  const list = document.getElementById("rule-list");
  if (!list || list.dataset.bound === "1") return;
  list.dataset.bound = "1";
  list.addEventListener("click", (e) => {
    const btn = e.target.closest("[data-rule-action]");
    if (!btn) return;
    const row = btn.closest(".rule-row");
    if (!row) return;
    const i = Number(row.dataset.i);
    const action = btn.dataset.ruleAction;
    if (action === "del") {
      if (i >= 0 && i < rules.length) rules.splice(i, 1);
      renderRuleList();
      return;
    }
    if (action === "up" && i > 0) {
      [rules[i - 1], rules[i]] = [rules[i], rules[i - 1]];
      renderRuleList();
      return;
    }
    if (action === "down" && i < rules.length - 1) {
      [rules[i + 1], rules[i]] = [rules[i], rules[i + 1]];
      renderRuleList();
      return;
    }
    if (action === "edit") {
      const meta = ruleMeta;
      const gid = Number((location.hash.match(/\d+/) || [])[0] || 0);
      openRuleEditor(gid, rules[i], meta, (updated) => {
        rules[i] = updated;
        renderRuleList();
        toast("规则已更新，记得保存");
      }, counterCandidates({ rules }));
    }
  });
}

// 计数器排障面板。
async function bindCounters(gid) {
  try {
    const res = await api("GET", `/api/v1/groups/${gid}/counters`);
    const counters = res.counters || {};
    const box = document.getElementById("counters-box");
    if (!box) return;
    const entries = Object.entries(counters);
    if (!entries.length) { box.innerHTML = `<p class="muted">暂无计数（规则中的「计数 +N」动作触发后显示）</p>`; return; }
    box.innerHTML = `<table><tr><th>计数器</th><th>QQ</th><th>次数</th></tr>` +
      entries.map(([cid, users]) => Object.entries(users)
        .map(([uid, n]) => `<tr><td>${esc(cid)}</td><td>${uid}</td><td>${n}</td></tr>`).join("")
      ).join("") + `</table>`;
  } catch { /* 忽略 */ }
}

// ---------- 人工群管 ----------
async function viewActions() {
  const data = await api("GET", "/api/v1/groups");
  const groups = data.groups || [];
  const gid = sessionStorage.getItem("action_gid") || groups[0]?.group_id || "";
  let members = [], msgs = [];
  if (gid) {
    try { members = (await api("GET", `/api/v1/groups/${gid}/members`)).members || []; }
    catch { /* OneBot 未连接 */ }
    try { msgs = (await api("GET", `/api/v1/groups/${gid}/messages?limit=20`)).messages || []; }
    catch { /* ignore */ }
  }
  on(shell(`
    <div class="content">
      <div class="card">
        <h2>人工群管</h2>
        <div class="row">
          <label>选择群
            <select id="act-group">
              ${groups.map((g) => `<option value="${g.group_id}" ${String(g.group_id) === String(gid) ? "selected" : ""}>${esc(g.remark || g.group_id)}</option>`).join("")}
            </select>
          </label>
          <label>目标成员
            <select id="act-user">
              <option value="">— 输入 QQ —</option>
              ${members.map((m) => {
                const name = m.card || m.nickname || m.user_id;
                return `<option value="${m.user_id}">${esc(name)} (${m.user_id})</option>`;
              }).join("")}
            </select>
          </label>
          <label>QQ 号<input type="number" id="act-userid" placeholder="${gid ? "直接输入" : ""}" style="width:140px"></label>
        </div>
        <div class="row" style="margin-top:12px">
          <button data-action="mute">禁言 30 分钟</button>
          <button class="secondary" data-action="unmute">解除禁言</button>
          <button class="danger" data-action="kick">移出群</button>
          <button class="secondary" data-action="whole-ban">全员禁言</button>
          <button class="secondary" data-action="unban-all">解除全员</button>
        </div>
        <div class="row" style="margin-top:12px">
          <label>设置名片<input type="text" id="act-card" placeholder="留空=清空" style="width:180px"></label>
          <button class="secondary" data-action="card">设置名片</button>
        </div>
      </div>
      <div class="card">
        <h2>撤回消息（最近 ${msgs.length} 条）</h2>
        ${msgs.length ? `<table>
          <tr><th></th><th>时间</th><th>成员</th><th>内容</th></tr>
          ${msgs.map((msg) => `<tr>
            <td><button class="secondary" data-action="recall" data-mid="${msg.message_id}">撤回</button></td>
            <td>${fmtTime(msg.time)}</td>
            <td>${esc(msg.nickname || msg.user_id)}</td>
            <td class="muted">${esc(msg.text)}</td>
          </tr>`).join("")}
        </table>` : `<div class="empty">暂无最近消息（OneBot 断线或尚未收到消息）</div>`}
      </div>
      <div id="act-result"></div>
    </div>`, "actions"), async (action) => {
    if (action === "logout") { await logout(); return; }
    const gid = Number(document.getElementById("act-group")?.value || 0);
    sessionStorage.setItem("action_gid", gid);
    const target = Number(document.getElementById("act-userid")?.value ||
      document.getElementById("act-user")?.value || 0);
    const body = { target_id: target };
    const dangerous = ["kick", "whole-ban", "recall"];
    if (action === "mute") {
      const mins = prompt("禁言分钟数（默认 30）", "30");
      if (mins === null) return;
      body.minutes = Number(mins) || 30;
    }
    if (action === "unban-all") { action = "whole-ban"; body.enable = false; }
    if (action === "whole-ban" && body.enable === undefined) body.enable = true;
    if (action === "card") body.card = document.getElementById("act-card")?.value || "";
    if (action === "recall") {
      const mid = Number(document.querySelector("[data-action=recall][data-mid]")?.dataset.mid || 0);
      if (!mid) return toast("请选择要撤回的消息", true);
      body.message_id = mid;
      if (!confirm("确定撤回该消息？")) return;
    }
    if (action === "kick" && !confirm(`确定将 ${target} 移出群？`)) return;
    if (action === "whole-ban" && body.enable && !confirm("确定开启全员禁言？")) return;
    if (!target && action !== "whole-ban" && action !== "recall") return toast("请选择或输入目标 QQ", true);
    const box = document.getElementById("act-result");
    box.innerHTML = `<p class="muted">执行中…</p>`;
    try {
      const res = await api("POST", `/api/v1/groups/${gid}/actions/${action}`, body, { idem: idemKey() });
      box.innerHTML = `<div class="success">${badge(res.action_result.result)}
        ${esc(res.action_result.detail || "")}</div>`;
      if (res.action_result.result === "unknown") {
        box.innerHTML += `<p class="muted">QQ 端可能已执行成功，请到群内确认，不要重复操作。</p>`;
      }
    } catch (e) {
      box.innerHTML = `<div class="error">${esc(e.message)}</div>`;
    }
  });
}

// ---------- 审计 ----------
async function viewAudit() {
  const params = new URLSearchParams(location.hash.split("?")[1] || "");
  const cursor = params.get("cursor") || "";
  const prevQuery = [...params.entries()].filter(([k]) => k !== "cursor").map(([k, v]) => `${k}=${v}`).join("&");
  const q = new URLSearchParams({ limit: "20" });
  if (cursor) q.set("cursor", cursor);
  if (params.get("group_id")) q.set("group_id", params.get("group_id"));
  if (params.get("result")) q.set("result", params.get("result"));
  let page;
  try { page = await api("GET", "/api/v1/audit?" + q); } catch (e) { toast(e.message, true); return; }
  on(shell(`
    <div class="content">
      <div class="card">
        <h2>审计记录</h2>
        <div class="row" style="margin-bottom:10px">
          <label>结果
            <select id="audit-result">
              <option value="">全部</option>
              <option value="ok" ${params.get("result") === "ok" ? "selected" : ""}>成功</option>
              <option value="failed" ${params.get("result") === "failed" ? "selected" : ""}>失败</option>
              <option value="unknown" ${params.get("result") === "unknown" ? "selected" : ""}>结果未知</option>
            </select>
          </label>
          <button class="secondary" data-action="filter">筛选</button>
        </div>
        ${page.entries?.length ? `<table>
          <tr><th>时间</th><th>来源</th><th>群</th><th>动作</th><th>目标</th><th>结果</th><th>详情</th></tr>
          ${page.entries.map((e) => `<tr>
            <td>${fmtTime(e.time)}</td>
            <td>${esc(e.source)}${e.actor_type === "automatic" ? " · 自动" : ""}</td>
            <td>${e.group_id || "-"}</td>
            <td>${esc(e.action)}</td>
            <td>${e.target_id || "-"}</td>
            <td>${badge(e.action_status || e.result)}</td>
            <td class="muted">${esc(e.detail || "")}</td>
          </tr>`).join("")}
        </table>` : `<div class="empty">暂无审计记录</div>`}
        <div class="row" style="margin-top:10px">
          ${cursor ? `<a class="secondary" style="text-decoration:none;padding:8px 14px;border-radius:6px;border:1px solid var(--border);color:var(--text)" href="#/audit?${prevQuery}">上一页</a>` : ""}
          ${page.next_cursor ? `<a class="secondary" style="text-decoration:none;padding:8px 14px;border-radius:6px;border:1px solid var(--border);color:var(--text)" href="#/audit?cursor=${page.next_cursor}">下一页</a>` : ""}
        </div>
      </div>
    </div>`, "audit"), async (action) => {
    if (action === "logout") { await logout(); }
    if (action === "filter") {
      const r = document.getElementById("audit-result").value;
      location.hash = "#/audit?" + (r ? `result=${r}` : "");
    }
  });
}

// ---------- 系统设置 ----------
async function viewSettings() {
  console.log("[debug] viewSettings start, hash=", location.hash);
  const s = await api("GET", "/api/v1/settings");
  console.log("[debug] settings loaded", s && s.revision);
  const st = await api("GET", "/api/v1/status");
  let hist = [];
  try { hist = (await api("GET", "/api/v1/config/history")).history || []; }
  catch { /* 未初始化时 history 不可用，按空处理 */ }
  on(shell(`
    <div class="content">
      <div class="card">
        <h2>系统设置 <span class="muted">revision ${s.revision}</span></h2>
        <form data-submit="save-settings">
          <input type="hidden" name="revision" value="${s.revision}">
          <div class="row">
            <label>机器人名称<input type="text" name="bot_name" value="${esc(s.system.bot_name)}"></label>
            <label>管理员 QQ<input type="number" name="owner" value="${s.system.owner}"></label>
            <label>时区<input type="text" name="timezone" value="${esc(s.system.timezone)}" placeholder="Asia/Shanghai"></label>
          </div>
          <h3>OneBot</h3>
          <div class="row">
            <label style="flex:2;min-width:220px">WS 地址<input type="text" name="ws_url" value="${esc(s.system.onebot.ws_url)}"></label>
            <label>API 超时（ms）<input type="number" name="api_timeout_ms" value="${s.system.onebot.api_timeout_ms}" min="1"></label>
          </div>
          <label>AccessToken
            <input type="text" name="access_token" placeholder="${s.system.onebot.access_token.configured ? "已配置（留空保持不变）" : "未配置"}" autocomplete="off">
          </label>
          <div class="row">
            <button type="button" class="secondary" data-action="clear-token" data-field="access_token">清除 AccessToken</button>
            <button type="button" class="secondary" data-action="test-onebot">测试 OneBot 连接</button>
          </div>
          <h3>NapCat WebUI</h3>
          <div class="row">
            <label style="flex:2;min-width:220px">WebUI 地址<input type="text" name="webui_url" value="${esc(s.system.napcat.webui_url || "")}" placeholder="http://127.0.0.1:6099"></label>
          </div>
          <label>WebUI Token
            <input type="password" name="webui_token" placeholder="${s.system.napcat.webui_token.configured ? "已配置（留空保持不变）" : "未配置"}" autocomplete="new-password">
          </label>
          <div class="row">
            <button type="button" class="secondary" data-action="clear-token" data-field="webui_token">清除 WebUI Token</button>
            <button type="button" class="secondary" data-action="test-napcat">测试 NapCat 连接</button>
          </div>
          <h3>NapCat 掉线监控</h3>
          <p class="muted" style="margin-bottom:6px">检测 NapCat 登录状态：掉线时向提醒邮箱发送一封邮件（每个掉线周期一封，扫码恢复后重置）。
            独立于邮箱验证功能——未启用或未配置时不影响其他功能。</p>
          <label class="inline" style="margin:6px 0"><input type="checkbox" name="watchdog_enabled" ${s.system.watchdog.enabled ? "checked" : ""}> 启用掉线监控</label>
          <div class="row">
            <label style="width:170px">检测间隔（分钟）<input type="number" name="watchdog_interval_minutes" value="${s.system.watchdog.interval_minutes || 10}" min="1" max="1440"></label>
            <label style="flex:2;min-width:200px">提醒收件邮箱（留空 = 用邮箱验证的收件人）<input type="text" name="watchdog_email_to" value="${esc(s.system.watchdog.email_to || "")}" placeholder="留空自动使用系统邮箱收件人"></label>
          </div>
          <div class="row">
            <button type="button" class="secondary" data-action="test-watchdog">发送测试提醒</button>
          </div>
          <h3>邮箱两步验证（登录安全）</h3>
          <p class="muted" style="margin-bottom:6px">开启后：输入正确密码 → 向收件邮箱发送 6 位验证码 → 验证通过才登录。
            使用邮箱的 <b>SMTP 授权码</b>（QQ/163 邮箱在设置中生成），建议同时开启「登录保护」。</p>
          <label class="inline" style="margin:6px 0"><input type="checkbox" name="email_enabled" ${s.system.email.enabled ? "checked" : ""}> 开启邮箱两步验证</label>
          <div class="row">
            <label style="flex:2;min-width:180px">SMTP 服务器<input type="text" name="smtp_host" value="${esc(s.system.email.smtp_host || "")}" placeholder="smtp.qq.com"></label>
            <label style="width:120px">端口<input type="number" name="smtp_port" value="${s.system.email.smtp_port || 465}" min="1" max="65535"></label>
          </div>
          <div class="row">
            <label style="flex:2;min-width:220px">SMTP 账号（邮箱地址）<input type="text" name="smtp_user" value="${esc(s.system.email.smtp_user || "")}" placeholder="bot@qq.com"></label>
            <label style="flex:2;min-width:180px">收件邮箱（验证码发到这里）<input type="text" name="email_to" value="${esc(s.system.email.to || "")}" placeholder="admin@qq.com"></label>
          </div>
          <label>SMTP 授权码
            <input type="password" name="smtp_password" placeholder="${s.system.email.smtp_password.configured ? "已配置（留空保持不变）" : "未配置"}" autocomplete="new-password">
          </label>
          <div class="row">
            <button type="button" class="secondary" data-action="clear-token" data-field="smtp_password">清除授权码</button>
            <button type="button" class="secondary" data-action="test-email">发送测试邮件</button>
          </div>
          <div class="row" style="margin-top:16px">
            <button type="submit">保存系统设置</button>
            <span id="settings-msg"></span>
          </div>
        </form>
      </div>
      <div class="card">
        <h2>配置历史（恢复）</h2>
        ${hist.length ? `<table>
          <tr><th>revision</th><th>时间</th><th>操作者</th><th>摘要</th><th></th></tr>
          ${hist.slice().reverse().map((h) => `<tr>
            <td>${h.revision}</td><td>${fmtTime(h.time)}</td><td>${esc(h.actor)}</td>
            <td class="muted">${esc(h.summary)}</td>
            <td><button class="secondary" data-action="restore" data-rev="${h.revision}">恢复</button></td>
          </tr>`).join("")}
        </table>` : `<div class="empty">暂无历史</div>`}
      </div>
      <div class="card">
        <h2>修改管理员密码</h2>
        <form data-submit="change-password">
          <label>当前密码<input type="password" name="current_password" required></label>
          <label>新密码<input type="password" name="new_password" required></label>
          <button type="submit" style="margin-top:10px">修改密码（全部会话将退出）</button>
        </form>
      </div>
      <div class="card">
        <h2>运行信息</h2>
        <table>
          <tr><th>配置版本</th><td>revision ${st.config_revision}</td></tr>
          <tr><th>运行时间</th><td>${fmtDur(st.uptime_seconds)}</td></tr>
          <tr><th>群数量</th><td>${st.groups.enabled}/${st.groups.total}</td></tr>
        </table>
      </div>
    </div>`, "settings"), async (action, form) => {
    if (action === "logout") { await logout(); return; }
    if (action === "clear-token") {
      const field = form.dataset.field;
      const sys = collectSystemSettings(form);
      if (field === "access_token") { sys.onebot = sys.onebot || {}; sys.onebot.access_token = { clear: true }; }
      else if (field === "webui_token") { sys.napcat = sys.napcat || {}; sys.napcat.webui_token = { clear: true }; }
      else if (field === "smtp_password") { sys.email = sys.email || {}; sys.email.smtp_password = { clear: true }; }
      try {
        await api("PUT", "/api/v1/settings", { revision: Number(form.revision.value), system: sys });
        toast("已清除"); location.hash = "#/settings";
      } catch (e) { toast(e.message, true); }
    }
    if (action === "test-onebot" || action === "test-napcat" || action === "test-email" || action === "test-watchdog") {
      const msg = document.getElementById("settings-msg");
      msg.textContent = "测试中…";
      try {
        const res = await api("POST", `/api/v1/settings/test-${action.slice(5)}`, {});
        msg.textContent = res.ok ? `✅ ${res.detail}` : `❌ ${res.detail}`;
      } catch (e) { msg.textContent = "❌ " + e.message; }
    }
    if (action === "restore") {
      if (!confirm(`确定恢复到 revision ${form.dataset.rev}？当前配置将回滚。`)) return;
      try {
        await api("POST", `/api/v1/config/history/${form.dataset.rev}/restore`, {});
        toast("已恢复"); location.hash = "#/settings";
      } catch (e) { toast(e.message, true); }
    }
    if (action === "save-settings") {
      try {
        await api("PUT", "/api/v1/settings", {
          revision: Number(form.revision.value),
          system: collectSystemSettings(form),
        });
        toast("已保存"); location.hash = "#/settings";
      } catch (e) {
        if (e.status === 409) {
          if (confirm("配置已被其他页面修改，重新加载最新配置？")) location.hash = "#/settings";
        } else toast(e.message, true);
      }
    }
    if (action === "change-password") {
      const fd = new FormData(form);
      try {
        await api("PUT", "/api/v1/auth/password", {
          current_password: fd.get("current_password"),
          new_password: fd.get("new_password"),
        });
        toast("密码已修改，请重新登录");
        location.hash = "#/login";
      } catch (e) { toast(e.message, true); }
    }
  });
}

function collectSystemSettings(form) {
  const fd = new FormData(form);
  const sys = {
    bot_name: fd.get("bot_name"),
    owner: Number(fd.get("owner") || 0),
    timezone: fd.get("timezone"),
    onebot: {
      ws_url: fd.get("ws_url"),
      api_timeout_ms: Number(fd.get("api_timeout_ms") || 5000),
    },
    napcat: { webui_url: fd.get("webui_url") },
    email: {
      enabled: fd.get("email_enabled") === "on",
      smtp_host: fd.get("smtp_host"),
      smtp_port: Number(fd.get("smtp_port") || 465),
      smtp_user: fd.get("smtp_user"),
      to: fd.get("email_to"),
    },
    watchdog: {
      enabled: fd.get("watchdog_enabled") === "on",
      interval_minutes: Number(fd.get("watchdog_interval_minutes") || 10),
      email_to: fd.get("watchdog_email_to"),
    },
  };
  const at = String(fd.get("access_token") || "");
  if (at.trim()) sys.onebot.access_token = at.trim();
  const wt = String(fd.get("webui_token") || "");
  if (wt.trim()) sys.napcat.webui_token = wt.trim();
  const sp = String(fd.get("smtp_password") || "");
  if (sp.trim()) sys.email.smtp_password = sp.trim();
  return sys;
}

// ---------- civgo 问答配置 ----------

// civgo 配置区块渲染辅助：布尔开关、数字输入、文本输入。
function ck(name, label, checked, extra) {
  return `<label class="inline" style="margin:6px 0"><input type="checkbox" name="${name}" ${checked ? "checked" : ""}> ${label}</label>`;
}
function num(name, label, val, min, max, extra) {
  return `<label style="width:${extra?.w || 150}px">${label}<input type="number" name="${name}" value="${val}" min="${min || 0}"${max ? ` max="${max}"` : ""}></label>`;
}

async function viewCivgo() {
  let d;
  try { d = await api("GET", "/api/v1/civgo"); }
  catch (e) { toast(e.message, true); return; }
  const c = d.config || {};
  const st = d.status || {};
  const notConfigured = !d.configured;
  on(shell(`
    <div class="content">
      <div class="card">
        <h2>civgo 问答服务 ${d.enabled ? `<span class="badge ok">已启用</span>` : `<span class="badge warn">${notConfigured ? "未配置" : "已停用"}</span>`}</h2>
        ${notConfigured ? `<p class="muted" style="margin:6px 0">civgo 配置尚不存在或无效。保存后会写入 data/civgo/civgo.json；若模块未启动，<b>重启进程后生效</b>。</p>` : ""}
        <table>
          <tr><th>文档地图</th><td>${st.docmap_files ?? "-"} 文件 / ${st.docmap_headings ?? "-"} 标题</td></tr>
          <tr><th>最近同步</th><td>${fmtTime(st.last_sync_at)}${st.fail_count ? ` <span class="badge bad">失败 ${st.fail_count} 次</span>` : ""}</td></tr>
          <tr><th>同步错误</th><td class="muted">${esc(st.last_error || "-")}</td></tr>
          <tr><th>历史记录</th><td>${st.history_total ?? "-"} 条</td></tr>
          <tr><th>累计问答</th><td>${st.total_requests ?? "-"} 次 / ${st.total_tokens ?? "-"} tokens</td></tr>
        </table>
        <form data-submit="save-civgo">
          <h3>总开关</h3>
          ${ck("enabled", "启用 civgo 问答（群内 @ 机器人回答游戏问题）", !!c.enabled)}
          <h3>文档仓库（git 同步）</h3>
          <div class="row">
            <label style="flex:2;min-width:220px">仓库 URL<input type="text" name="repo_url" value="${esc(c.repo?.url || "")}" placeholder="https://github.com/AutumnPizazz/civgo.git"></label>
            <label>分支（留空自动探测）<input type="text" name="repo_branch" value="${esc(c.repo?.branch || "")}" placeholder="stable"></label>
          </div>
          <div class="row">
            <label style="flex:2;min-width:200px">文档路径<input type="text" name="repo_docs_path" value="${esc(c.repo?.docs_path || "")}" placeholder="docs/game_content"></label>
            ${num("repo_sync_interval_sec", "同步间隔（秒）", c.repo?.sync_interval_sec ?? 300, 30, 3600)}
          </div>
          ${ck("repo_clone_shallow", "浅克隆", c.repo?.clone_shallow)} ${ck("repo_sparse_checkout", "稀疏检出（只取文档目录）", c.repo?.sparse_checkout)}
          <h3>AI 网关</h3>
          <div class="row">
            <label style="flex:2;min-width:220px">Base URL<input type="text" name="ai_base_url" value="${esc(c.ai?.base_url || "")}" placeholder="https://ai.realseek.wiki/v1"></label>
            <label style="flex:1;min-width:160px">对话模型<input type="text" name="ai_chat_model" value="${esc(c.ai?.chat_model || "")}" placeholder="deepseek-v4-flash"></label>
          </div>
          <label>API Key
            <input type="password" name="ai_api_key" placeholder="${c.ai?.api_key?.configured ? "已配置（留空保持不变）" : "未配置（必填）"}" autocomplete="new-password">
          </label>
          <div class="row">
            <button type="button" class="secondary" data-action="clear-ai-key">清除 API Key</button>
            <button type="button" class="secondary" data-action="test-ai">测试 AI 连接</button>
            <span class="muted">测试会验证网关 function calling 支持（模块未启动时不可用）</span>
          </div>
          <div class="row">
            ${num("ai_chat_timeout_sec", "请求超时（秒）", c.ai?.chat_timeout_sec ?? 90, 10, 600)}
            ${num("ai_max_output_tokens", "最大输出 tokens", c.ai?.max_output_tokens ?? 2048, 100, 8192, { w: 180 })}
          </div>
          <h3>AI 自主检索（agent）</h3>
          <div class="row">
            ${num("agent_max_tool_calls", "工具调用上限", c.agent?.max_tool_calls ?? 8, 1, 30)}
            ${num("agent_max_context_chars", "上下文预算（字）", c.agent?.max_context_chars ?? 12000, 1000, 100000, { w: 180 })}
          </div>
          <div class="row">
            ${num("agent_read_page_lines", "单页行数", c.agent?.read_page_lines ?? 200, 10, 1000)}
            ${num("agent_read_page_max_chars", "单页字符上限", c.agent?.read_page_max_chars ?? 8000, 1000, 30000, { w: 180 })}
          </div>
          <h3>群历史记录（AI 决策召回）</h3>
          ${ck("history_enabled", "记录问答历史（recall_history 工具用）", !!c.history?.enabled)} ${ck("history_persist", "持久化到磁盘", !!c.history?.persist)}
          <div class="row">
            ${num("history_max_entries_per_group", "每群条数上限", c.history?.max_entries_per_group ?? 50, 10, 200)}
            ${num("history_max_recall_entries", "单次召回条数", c.history?.max_recall_entries ?? 5, 1, 10)}
          </div>
          <h3>token 用量预警（邮件提醒）</h3>
          ${ck("usage_alert_enabled", "启用用量预警（需 SMTP 已配置）", !!c.usage_alert?.enabled)}
          <div class="row">
            ${num("usage_alert_window_minutes", "窗口（分钟）", c.usage_alert?.window_minutes ?? 5, 1, 120)}
            ${num("usage_alert_threshold_tokens", "触发阈值（tokens）", c.usage_alert?.threshold_tokens ?? 500000, 1000, 0, { w: 190 })}
            ${num("usage_alert_cooldown_minutes", "冷却（分钟）", c.usage_alert?.cooldown_minutes ?? 30, 1, 1440)}
          </div>
          <label>提醒收件邮箱（留空 = 用系统邮箱收件人）<input type="text" name="usage_alert_email_to" value="${esc(c.usage_alert?.email_to || "")}" placeholder="admin@qq.com"></label>
          <h3>安全防护（越狱/有害内容拦截）</h3>
          <p class="muted" style="margin-bottom:6px">拦截「忽略之前的指令」「解除限制」等越狱诱导与违法犯罪内容提问，命中直接拒绝（不走 AI）。
            拦截次数达到阈值后发邮件提醒。关键词表内置，命中会在日志中记录。</p>
          ${ck("guard_enabled", "启用安全拦截", !!c.guard?.enabled)}
          <label>拦截回复话术<input type="text" name="guard_reject_reply" value="${esc(c.guard?.reject_reply || "")}" placeholder="这个话题不太方便聊，换个游戏问题试试吧～"></label>
          <div class="row">
            ${num("guard_alert_threshold", "提醒阈值（10 分钟内拦截次数）", c.guard?.alert_threshold ?? 5, 0, 100)}
            <label style="flex:2;min-width:200px">提醒邮箱（留空 = 不提醒）<input type="text" name="guard_alert_email" value="${esc(c.guard?.alert_email || "")}" placeholder="admin@qq.com"></label>
          </div>
          <h3>启用群</h3>
          <label>群号（逗号分隔）<input type="text" name="groups" value="${esc((c.groups || []).join(", "))}" placeholder="123456, 789012"></label>
          <h3>限流</h3>
          <div class="row">
            ${num("rate_per_user_min", "每用户/分钟", c.rate_limit?.per_user_min ?? 3, 1, 60)}
            ${num("rate_per_group_min", "每群/分钟", c.rate_limit?.per_group_min ?? 10, 1, 120)}
            ${num("rate_max_concurrent_ai", "AI 并发上限", c.rate_limit?.max_concurrent_ai ?? 2, 1, 20)}
          </div>
          <div class="row" style="margin-top:16px">
            <button type="submit">保存 civgo 配置</button>
            <span id="civgo-msg"></span>
          </div>
        </form>
      </div>
    </div>`, "civgo"), async (action, form) => {
    if (action === "logout") { await logout(); return; }
    const msg = document.getElementById("civgo-msg");
    if (action === "test-ai") {
      msg.textContent = "测试中…";
      try {
        const res = await api("POST", "/api/v1/civgo/test-ai", {});
        msg.textContent = res.ok ? `✅ ${res.detail}` : `❌ ${res.detail}`;
      } catch (e) { msg.textContent = "❌ " + e.message; }
      return;
    }
    if (action === "clear-ai-key") {
      const cfg = collectCivgoConfig(form);
      cfg.ai = cfg.ai || {};
      cfg.ai.api_key = { clear: true };
      await saveCivgo(cfg, msg);
      return;
    }
    if (action === "save-civgo") {
      await saveCivgo(collectCivgoConfig(form), msg);
    }
  });
}

// 收集 civgo 表单为配置对象（api_key 仅在有输入时携带）。
function collectCivgoConfig(form) {
  const fd = new FormData(form);
  const n = (k, def) => { const v = Number(fd.get(k)); return Number.isFinite(v) && v > 0 ? v : def; };
  const cfg = {
    enabled: fd.get("enabled") === "on",
    repo: {
      url: fd.get("repo_url"),
      branch: fd.get("repo_branch"),
      docs_path: fd.get("repo_docs_path"),
      sync_interval_sec: n("repo_sync_interval_sec", 300),
      clone_shallow: fd.get("repo_clone_shallow") === "on",
      sparse_checkout: fd.get("repo_sparse_checkout") === "on",
    },
    ai: {
      base_url: fd.get("ai_base_url"),
      chat_model: fd.get("ai_chat_model"),
      chat_timeout_sec: n("ai_chat_timeout_sec", 90),
      max_output_tokens: n("ai_max_output_tokens", 2048),
    },
    agent: {
      max_tool_calls: n("agent_max_tool_calls", 8),
      max_context_chars: n("agent_max_context_chars", 12000),
      read_page_lines: n("agent_read_page_lines", 200),
      read_page_max_chars: n("agent_read_page_max_chars", 8000),
    },
    history: {
      enabled: fd.get("history_enabled") === "on",
      max_entries_per_group: n("history_max_entries_per_group", 50),
      persist: fd.get("history_persist") === "on",
      max_recall_entries: n("history_max_recall_entries", 5),
    },
    usage_alert: {
      enabled: fd.get("usage_alert_enabled") === "on",
      window_minutes: n("usage_alert_window_minutes", 5),
      threshold_tokens: n("usage_alert_threshold_tokens", 500000),
      cooldown_minutes: n("usage_alert_cooldown_minutes", 30),
      email_to: fd.get("usage_alert_email_to"),
    },
    guard: {
      enabled: fd.get("guard_enabled") === "on",
      reject_reply: fd.get("guard_reject_reply"),
      alert_threshold: n("guard_alert_threshold", 5),
      alert_email: fd.get("guard_alert_email"),
    },
    groups: String(fd.get("groups") || "").split(/[,，\s]+/).map((s) => Number(s.trim())).filter((x) => Number.isFinite(x) && x > 0),
    rate_limit: {
      per_user_min: n("rate_per_user_min", 3),
      per_group_min: n("rate_per_group_min", 10),
      max_concurrent_ai: n("rate_max_concurrent_ai", 2),
    },
  };
  const key = String(fd.get("ai_api_key") || "").trim();
  if (key) cfg.ai.api_key = key;
  return cfg;
}

async function saveCivgo(cfg, msg) {
  try {
    const res = await api("PUT", "/api/v1/civgo", { config: cfg });
    msg.textContent = res.effective ? "✅ 已保存并立即生效" : "✅ 已保存（模块未启动，重启进程后生效）";
    setTimeout(() => location.hash = "#/civgo", 600);
  } catch (e) { msg.textContent = "❌ " + e.message; }
}

// ---------- 路由 ----------
async function logout() {
  try { await api("POST", "/api/v1/auth/logout", {}); } catch { /* ignore */ }
  location.hash = "#/login";
}

const routes = [
  ["#/login", viewLogin],
  ["#/overview", viewOverview],
  ["#/groups", viewGroups],
  [/^#\/groups\/\d+$/, viewGroups],
  ["#/actions", viewActions],
  ["#/audit", viewAudit],
  ["#/settings", viewSettings],
  ["#/civgo", viewCivgo],
];

async function router() {
  const hash = location.hash || "#/overview";
  console.log("[debug] router hash=", hash);
  session = await initSession();
  if (!session) { await viewLogin(); return; }
  if (hash === "#/login") { location.hash = "#/overview"; return; }
  for (const [pattern, fn] of routes) {
    if (pattern instanceof RegExp ? pattern.test(hash) : pattern === hash) {
      try { await fn(); } catch (e) { console.error("[router error]", e && (e.stack || e.message) || e); toast(e.message, true); }
      return;
    }
  }
  location.hash = "#/overview";
}

window.addEventListener("hashchange", router);
router();
