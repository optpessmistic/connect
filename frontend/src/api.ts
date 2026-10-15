export interface User { email: string; nickname?: string }
export interface Group { id: number; name: string; platform: string; allow_messages_dispatch: boolean; status: string }
export interface Key { id: number; name: string; group_id: number; status: string; mask: string; expires_at?: string | null }
export interface Installation { executable: string; installed: boolean; version: string; version_status: string; source: string; detail: string; family?: string; name?: string }
export interface Detection extends Installation { desktop?: Installation }
export interface DetectedClient { detection?: Detection; id: string; path: string; installed: boolean; supported: boolean; version: string; reason: string }
export interface Session { session: string; user: User; groups: Group[]; keys: Key[]; clients: DetectedClient[]; requires_2fa?: boolean; fixture?: boolean; warning?: string }
export interface Catalog { models: string[]; group: Group; messages: boolean }
export interface Change { field: string; before: string; after: string }
export interface Plan { id: string; client: string; path: string; changes: Change[]; noop: boolean; template?: string; model_count?: number }
export interface ConfigRecord { id: string; client: string; path: string; at: string; state: string; operation: string; changes: Change[]; backup: string }
export const native = location.protocol === "wails:" || location.hostname === "wails.localhost";
function remembered(): string { try { return native ? "" : localStorage.getItem("connect-session") || ""; } catch { return ""; } }
let session = remembered();
// Keep an expired ID only so an explicit logout can still clear native credentials.
let expiredSession = "";
let sessionGeneration = 0;
const expirationListeners = new Set<(error: APIError) => void>();
export function hasRememberedSession() { return !!session; }
export function clearRememberedSession() { sessionGeneration++; session = ""; expiredSession = ""; try { localStorage.removeItem("connect-session"); } catch {} }
export function onSessionExpired(listener: (error: APIError) => void) {
  expirationListeners.add(listener);
  return () => { expirationListeners.delete(listener); };
}
export class APIError extends Error {
  status: number;
  credentialCleanupRequired: boolean;
  constructor(message: string, status: number, credentialCleanupRequired = false) {
    super(message);
    this.status = status;
    this.credentialCleanupRequired = credentialCleanupRequired;
  }
}
export class StaleRequestError extends Error {
  constructor() { super("登录状态已变化，已忽略旧请求。"); this.name = "StaleRequestError"; }
}

export async function request<T>(action: string, body: object = {}): Promise<T> {
  // Auth attempts invalidate older work immediately, including when logout fails.
  // A late successful session response must not restore a session we just left.
  if (["login", "login-otp", "resume", "logout"].includes(action)) sessionGeneration++;
  const generation = sessionGeneration;
  const requestSession = action === "logout" ? session || expiredSession : session;
  const assertCurrent = () => { if (generation !== sessionGeneration) throw new StaleRequestError(); };
  let status: number;
  let data: T & {error?: string; session?: string; credential_cleanup_required?: boolean};
  if (native) {
    const { Call } = await import("@wailsio/runtime");
    assertCurrent();
    const pending = Call.ByName("surplustoken-connect/internal/httpapi.Bridge.Request", action, requestSession, JSON.stringify(body));
    const timeout = setTimeout(() => pending.cancel(), 45000);
    try {
      const response = await pending as {status:number; data:typeof data};
      status = response.status; data = response.data;
    } catch { assertCurrent(); throw new Error("原生连接服务不可用或请求超时，请重新打开 Connect。"); }
    finally { clearTimeout(timeout); }
  } else {
    let response: Response;
    try { response = await fetch(`/connect/${action}`, {method:"POST",headers:{"Content-Type":"application/json","X-Connect-Request":"1","X-Connect-Session":requestSession},body:JSON.stringify(body),signal:AbortSignal.timeout(45000)}); }
    catch { assertCurrent(); throw new Error("本地连接服务不可用或请求超时，请检查网络和 Connect 服务。"); }
    try { data = await response.json(); } catch { assertCurrent(); throw new Error("连接服务响应异常，请确认本地 API 已启动。"); }
    status = response.status;
  }
  assertCurrent();
  if (status < 200 || status >= 300) {
    const error = new APIError(data.error || "操作失败", status, data.credential_cleanup_required === true);
    if (status === 401 && action !== "login" && action !== "logout" && requestSession && requestSession === session) {
      expiredSession = session;
      session = "";
      sessionGeneration++;
      try { localStorage.removeItem("connect-session"); } catch {}
      for (const listener of expirationListeners) listener(error);
    }
    throw error;
  }
  if (data.session) { session = data.session; expiredSession = ""; if (!native) { try { localStorage.setItem("connect-session",session); } catch {} } }
  if (action === "logout") clearRememberedSession();
  return data;
}
