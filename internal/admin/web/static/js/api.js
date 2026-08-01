// api.js —— 同源 API 封装：session 检查、CSRF token（仅内存）、统一错误。
let csrfToken = "";

export async function api(method, path, body, { idem } = {}) {
  const headers = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  if (method !== "GET" && csrfToken) headers["X-CSRF-Token"] = csrfToken;
  if (idem) headers["Idempotency-Key"] = idem;
  const resp = await fetch(path, {
    method,
    headers,
    body: body !== undefined ? JSON.stringify(body) : undefined,
  });
  let data = null;
  try { data = await resp.json(); } catch { /* 非 JSON 响应 */ }
  if (resp.status === 401) {
    // 会话过期：清除内存 CSRF，跳回登录页
    csrfToken = "";
    location.hash = "#/login";
    throw new ApiError(resp.status, "会话已过期，请重新登录", data);
  }
  if (!resp.ok) {
    const msg = data?.error?.message || `请求失败（HTTP ${resp.status}）`;
    throw new ApiError(resp.status, msg, data);
  }
  return data;
}

export class ApiError extends Error {
  constructor(status, message, payload) {
    super(message);
    this.status = status;
    this.payload = payload;
  }
}

// 初始化：检查会话；未登录跳登录页，已登录取 CSRF。
export async function initSession() {
  const s = await api("GET", "/api/v1/auth/session");
  if (!s.authenticated) {
    location.hash = "#/login";
    return null;
  }
  csrfToken = s.csrf_token;
  return s;
}

export function currentCsrf() { return csrfToken; }

// 随机 Idempotency-Key（危险动作防重复提交）。
export function idemKey() {
  return "k" + Math.random().toString(36).slice(2) + Date.now().toString(36);
}

export function fmtTime(iso) {
  if (!iso) return "-";
  const d = new Date(iso);
  const p = (n) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}`;
}

export function fmtDur(sec) {
  if (!sec) return "0s";
  const h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = sec % 60;
  if (h) return `${h}h${m}m`;
  if (m) return `${m}m${s}s`;
  return `${s}s`;
}

export function esc(s) {
  return String(s ?? "").replace(/[&<>"']/g, (c) => ({
    "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;",
  }[c]));
}

export function badge(result) {
  const map = { ok: ["ok", "成功"], failed: ["bad", "失败"], unknown: ["unknown", "结果未知"] };
  const [cls, label] = map[result] || ["warn", result];
  return `<span class="badge ${cls}">${label}</span>`;
}
