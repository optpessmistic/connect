import assert from "node:assert/strict";
import { beforeEach, test } from "node:test";

const storage = new Map();
globalThis.location = new URL("http://127.0.0.1:5173");
globalThis.localStorage = {
  getItem: key => storage.get(key) ?? null,
  setItem: (key, value) => storage.set(key, value),
  removeItem: key => storage.delete(key),
};
const { APIError, StaleRequestError, clearRememberedSession, hasRememberedSession, onSessionExpired, request } = await import("../src/api.ts");
const response = (status, data) => new Response(JSON.stringify(data), {status, headers:{"Content-Type":"application/json"}});
const unauthorized = error => error instanceof APIError && error.status === 401;
const stale = error => error instanceof StaleRequestError;

beforeEach(() => {
  clearRememberedSession();
  storage.clear();
  globalThis.fetch = async () => { throw new Error("Unexpected request: tests must stub the local transport"); };
});

async function logIn(id = "local-session") {
  globalThis.fetch = async () => response(200, {session:id});
  await request("login", {email:"fixture@example.test", password:"fixture-only"});
}

test("expired business sessions notify the UI once and retain the old ID only for logout", async t => {
  await logIn();
  let expirations = 0;
  t.after(onSessionExpired(() => { expirations++; }));
  globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers["X-Connect-Session"], "local-session");
    return response(401, {error:"请先登录"});
  };
  await assert.rejects(request("history"), unauthorized);
  assert.equal(expirations, 1);
  assert.equal(hasRememberedSession(), false);
  assert.equal(storage.has("connect-session"), false);

  globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers["X-Connect-Session"], "");
    return response(401, {error:"请先登录"});
  };
  await assert.rejects(request("detect"), unauthorized);
  assert.equal(expirations, 1);

  globalThis.fetch = async (url, options) => {
    assert.equal(url, "/connect/logout");
    assert.equal(options.headers["X-Connect-Session"], "local-session");
    return response(200, {ok:true});
  };
  await request("logout");
  globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers["X-Connect-Session"], "");
    return response(200, {ok:true});
  };
  await request("logout");
});

test("failed credential cleanup preserves the expired ID for an explicit retry", async () => {
  await logIn();
  globalThis.fetch = async () => response(401, {error:"请先登录"});
  await assert.rejects(request("models"), unauthorized);
  globalThis.fetch = async () => { throw new Error("offline"); };
  await assert.rejects(request("logout"), /本地连接服务不可用/);
  globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers["X-Connect-Session"], "local-session");
    return response(200, {ok:true});
  };
  await request("logout");
});

test("a delayed 401 from an old session does not expire a new login", async t => {
  await logIn("old-session");
  let finishOldRequest;
  let expirations = 0;
  t.after(onSessionExpired(() => { expirations++; }));
  globalThis.fetch = () => new Promise(resolve => { finishOldRequest = resolve; });
  const oldRequest = assert.rejects(request("history"), stale);
  await logIn("new-session");
  finishOldRequest(response(401, {error:"请先登录"}));
  await oldRequest;
  assert.equal(expirations, 0);
  assert.equal(hasRememberedSession(), true);
  assert.equal(storage.get("connect-session"), "new-session");
});

test("validation failures and offline requests preserve the login", async t => {
  await logIn();
  let expirations = 0;
  t.after(onSessionExpired(() => { expirations++; }));
  globalThis.fetch = async () => response(400, {error:"模型已不可用"});
  await assert.rejects(request("plan"), /模型已不可用/);
  globalThis.fetch = async () => { throw new Error("offline"); };
  await assert.rejects(request("session"), /本地连接服务不可用/);
  assert.equal(hasRememberedSession(), true);
  assert.equal(expirations, 0);
});

test("an expired OTP challenge follows the same login reset path", async t => {
  await logIn("otp-challenge");
  let expirations = 0;
  t.after(onSessionExpired(() => { expirations++; }));
  globalThis.fetch = async () => response(401, {error:"请先登录"});
  await assert.rejects(request("login-otp", {code:"123456"}), unauthorized);
  assert.equal(expirations, 1);
  assert.equal(hasRememberedSession(), false);
});

test("successful reauthentication replaces an expired ID used by logout", async () => {
  await logIn("old-session");
  globalThis.fetch = async () => response(401, {error:"请先登录"});
  await assert.rejects(request("history"), unauthorized);
  await logIn("new-session");
  globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers["X-Connect-Session"], "new-session");
    return response(200, {ok:true});
  };
  await request("logout");
  assert.equal(hasRememberedSession(), false);
});

test("a delayed successful session refresh cannot revive an expired session after reauthentication", async () => {
  await logIn("old-session");
  let finishOldRequest;
  globalThis.fetch = () => new Promise(resolve => { finishOldRequest = resolve; });
  const oldRequest = assert.rejects(request("session"), stale);
  globalThis.fetch = async () => response(401, {error:"请先登录"});
  await assert.rejects(request("history"), unauthorized);
  await logIn("new-session");
  finishOldRequest(response(200, {session:"old-session", keys:[{id:1}]}));
  await oldRequest;
  assert.equal(storage.get("connect-session"), "new-session");
  globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers["X-Connect-Session"], "new-session");
    return response(200, []);
  };
  await request("detect");
});

test("a delayed refresh cannot revive a session after explicit logout", async () => {
  await logIn();
  let finishOldRequest;
  globalThis.fetch = () => new Promise(resolve => { finishOldRequest = resolve; });
  const oldRequest = assert.rejects(request("session"), stale);
  globalThis.fetch = async () => response(200, {ok:true});
  await request("logout");
  finishOldRequest(response(200, {session:"local-session"}));
  await oldRequest;
  assert.equal(hasRememberedSession(), false);
  assert.equal(storage.has("connect-session"), false);
});

test("late logout and network failures cannot clear or disturb a newer login", async () => {
  await logIn("old-session");
  let finishLogout;
  globalThis.fetch = () => new Promise(resolve => { finishLogout = resolve; });
  const oldLogout = assert.rejects(request("logout"), stale);
  await logIn("new-session");
  finishLogout(response(200, {ok:true}));
  await oldLogout;
  assert.equal(storage.get("connect-session"), "new-session");

  let failOldRequest;
  globalThis.fetch = () => new Promise((_resolve, reject) => { failOldRequest = reject; });
  const oldFailure = assert.rejects(request("models"), stale);
  await logIn("latest-session");
  failOldRequest(new Error("offline"));
  await oldFailure;
  assert.equal(storage.get("connect-session"), "latest-session");
});

test("a failed logout still invalidates older requests but preserves the session for retry", async () => {
  await logIn();
  let finishOldRequest;
  globalThis.fetch = () => new Promise(resolve => { finishOldRequest = resolve; });
  const oldRequest = assert.rejects(request("history"), stale);
  globalThis.fetch = async () => response(500, {error:"凭据库暂不可用"});
  await assert.rejects(request("logout"), /凭据库暂不可用/);
  finishOldRequest(response(200, [{id:"old-history"}]));
  await oldRequest;
  assert.equal(hasRememberedSession(), true);
  assert.equal(storage.get("connect-session"), "local-session");
});

test("API errors default to no credential cleanup requirement", async () => {
  assert.equal(new APIError("失败", 400).credentialCleanupRequired, false);
  globalThis.fetch = async () => response(401, {error:"请先登录"});
  await assert.rejects(request("session"), error => unauthorized(error) && error.credentialCleanupRequired === false);
});

test("credential cleanup details reach the expiration callback and preserve the ID for logout", async t => {
  await logIn();
  const details = "登录已失效，但系统凭据清理失败，请重试。";
  let notified;
  t.after(onSessionExpired(error => { notified = error; }));
  globalThis.fetch = async () => response(401, {error:details, credential_cleanup_required:true});
  await assert.rejects(request("models"), error => {
    assert.equal(error, notified);
    assert.equal(error.message, details);
    assert.equal(error.credentialCleanupRequired, true);
    return unauthorized(error);
  });
  assert.equal(hasRememberedSession(), false);
  globalThis.fetch = async (_url, options) => {
    assert.equal(options.headers["X-Connect-Session"], "local-session");
    return response(200, {ok:true});
  };
  await request("logout");
});

test("initial resume without a frontend session exposes cleanup failure details for an explicit retry", async t => {
  let expirations = 0;
  t.after(onSessionExpired(() => { expirations++; }));
  const details = "保存的登录已失效；清理系统凭据失败，请重试恢复登录。";
  globalThis.fetch = async (url, options) => {
    assert.equal(url, "/connect/resume");
    assert.equal(options.headers["X-Connect-Session"], "");
    return response(401, {error:details, credential_cleanup_required:true});
  };
  await assert.rejects(request("resume"), error => unauthorized(error) && error.credentialCleanupRequired && error.message === details);
  assert.equal(expirations, 0);
  globalThis.fetch = async () => response(200, {saved:false});
  assert.deepEqual(await request("resume"), {saved:false});
});
