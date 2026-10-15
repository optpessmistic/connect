import { test, expect, button, login } from './fixtures.mjs';

test('expired login can explicitly clear its old local session', async ({ page, app }) => {
  await login(page, app);
  const old = await page.evaluate(() => localStorage.getItem('connect-session'));
  await page.route('**/connect/detect', route => route.fulfill({ status: 401, json: { error: '登录已失效' } }));
  await button(page, '检测客户端').click();
  await expect(button(page, '清除已过期会话')).toBeEnabled();
  const logout = page.waitForRequest(request => request.url().endsWith('/connect/logout'));
  await button(page, '清除已过期会话').click();
  expect((await logout).headers()['x-connect-session']).toBe(old);
  await expect(button(page, '清除已过期会话')).toBeHidden();
  expect(await page.evaluate(() => localStorage.getItem('connect-session'))).toBeNull();
});

test('a delayed session refresh cannot overwrite a newer login', async ({ page, app }) => {
  await login(page, app);
  const old = await page.evaluate(() => localStorage.getItem('connect-session'));
  let release;
  let captured;
  let delivered;
  const gate = new Promise(resolve => { release = resolve; });
  const ready = new Promise(resolve => { captured = resolve; });
  const finished = new Promise(resolve => { delivered = resolve; });
  await page.route('**/connect/session', async route => {
    const response = await route.fetch();
    captured();
    await gate;
    await route.fulfill({ response });
    delivered();
  });
  try {
    await button(page, 'API Key').click();
    await button(page, '刷新密钥').click();
    await ready;
    await page.route('**/connect/history', route => route.fulfill({ status: 401, json: { error: '登录已失效' } }));
    await button(page, '配置记录').click();
    await expect(button(page, '登录')).toBeEnabled();
    await page.getByLabel('密码', { exact: true }).fill('fixture-only');
    await button(page, '登录').click();
    await expect(button(page, 'API Key')).toBeVisible();
    const current = await page.evaluate(() => localStorage.getItem('connect-session'));
    expect(current).not.toBe(old);
    const responseReceived = page.waitForResponse(response => response.url().endsWith('/connect/session'));
    release();
    await finished;
    await (await responseReceived).finished();
    // Let the old response's promise and React updates run before checking.
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    // A subsequent business request must use the new ID after the old fetch settles.
    await expect(button(page, '检测客户端')).toBeEnabled();
    const detect = page.waitForRequest(request => request.url().endsWith('/connect/detect'));
    await button(page, '检测客户端').click();
    expect((await detect).headers()['x-connect-session']).toBe(current);
    expect(await page.evaluate(() => localStorage.getItem('connect-session'))).toBe(current);
    await expect(button(page, 'API Key')).toBeVisible();
  } finally { release(); }
});

test('credential cleanup failures stay visible and allow explicit logout retry', async ({ page, app }) => {
  await login(page, app);
  await page.route('**/connect/detect', route => route.fulfill({ status: 401, json: {
    error: '登录已失效；无法清除本机记住的登录，请重试退出登录', credential_cleanup_required: true,
  } }));
  await button(page, '检测客户端').click();
  await expect(page.getByRole('alert')).toContainText('无法清除本机记住的登录');
  await expect(button(page, '清除已过期会话')).toBeEnabled();
  let fail = true;
  await page.route('**/connect/logout', async route => {
    if (fail) {
      fail = false;
      await route.fulfill({ status: 400, json: { error: '凭据库暂时不可用' } });
    } else await route.continue();
  });
  await button(page, '清除已过期会话').click();
  await expect(page.getByRole('alert')).toContainText('保存的登录尚未清除');
  await button(page, '清除已过期会话').click();
  await expect(button(page, '清除已过期会话')).toBeHidden();
  await expect(page.getByRole('alert')).toHaveCount(0);
});

test('history failure offers retry and an older response cannot replace newer results', async ({ page, app }) => {
  await login(page, app);
  let release;
  let captured;
  const gate = new Promise(resolve => { release = resolve; });
  const ready = new Promise(resolve => { captured = resolve; });
  let requests = 0;
  await page.route('**/connect/history', async route => {
    requests++;
    if (requests === 1) {
      captured();
      await gate;
      await route.fulfill({ status: 200, json: [{
        id: 'obsolete-record', client: 'claude', path: 'obsolete-fixture-path', at: '2026-10-14T00:00:00Z',
        state: 'failed', operation: 'apply', changes: [], backup: '',
      }] });
    } else if (requests === 2) {
      await route.fulfill({ status: 503, json: { error: '记录暂时不可读' } });
    } else await route.continue();
  });
  try {
    await button(page, '配置记录').click();
    await ready;
    await expect(page.getByText('正在加载配置记录…')).toBeVisible();
    await button(page, '客户端').click();
    await button(page, '配置记录').click();
    await expect(page.getByRole('alert')).toContainText('记录暂时不可读');
    await button(page, '重试加载记录').click();
    await expect(page.getByText('还没有配置记录')).toBeVisible();
    const oldResponse = page.waitForResponse(response => response.url().endsWith('/connect/history'));
    release();
    await (await oldResponse).finished();
    await page.evaluate(() => new Promise(resolve => requestAnimationFrame(() => requestAnimationFrame(resolve))));
    await expect(page.getByText('还没有配置记录')).toBeVisible();
    await expect(page.getByText('obsolete-fixture-path')).toHaveCount(0);
  } finally { release(); }
});
