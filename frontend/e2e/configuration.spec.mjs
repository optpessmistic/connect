import { test, expect, button, login, previewAndApply, restoreLatest } from './fixtures.mjs';

test('per-client keys, protocol switches, and restore preserve existing settings', async ({ page, app }) => {
  await login(page, app);
  await button(page, 'API Key').click();
  await button(page, 'OpenAI 测试 Key 用于配置 OpenCode').click();
  await previewAndApply(page);
  let config = await app.readConfig('opencode');
  expect(Object.keys(config.providers.surplustoken.models)).toEqual(['gpt-fixture']);
  expect(config.providers.surplustoken.settings.apiKey).toBe('fixture-openai-key');
  expect(config.providers.custom).toEqual(app.original.opencode.providers.custom);
  await button(page, '离线测试 Key 用于配置 OpenCode').click();
  await previewAndApply(page);
  config = await app.readConfig('opencode');
  expect(Object.keys(config.providers.surplustoken.models)).toEqual(['fixture-model']);
  expect(config.providers.surplustoken.canonical).toBe('anthropic');
  expect(config.providers.surplustoken.settings.apiKey).toBe('fixture-not-a-real-key');
  expect(config.providers.surplustoken.settings.transport).toBeUndefined();
  await restoreLatest(page);
  expect(Object.keys((await app.readConfig('opencode')).providers.surplustoken.models)).toEqual(['gpt-fixture']);
  await restoreLatest(page);
  expect(await app.readConfig('opencode')).toEqual(app.original.opencode);

  await button(page, 'API Key').click();
  await button(page, '离线测试 Key 用于配置 Claude Code').click();
  await previewAndApply(page);
  expect((await app.readConfig('claude')).env.ANTHROPIC_MODEL).toBe('fixture-model');
  expect((await app.readConfig('claude')).env.ANTHROPIC_AUTH_TOKEN).toBe('fixture-not-a-real-key');
  await restoreLatest(page);
  expect(await app.readConfig('claude')).toEqual(app.original.claude);
});

test('offline key creation survives refresh and can be used to configure a client', async ({ page, app }) => {
  await login(page, app);
  await button(page, 'API Key').click();
  await button(page, '创建密钥').click();
  await page.getByRole('combobox', { name: '分组', exact: true }).click();
  await page.getByRole('option', { name: '离线测试分组', exact: true }).click();
  await button(page, '确认创建').click();
  await expect(page.getByRole('status')).toContainText('密钥已创建');
  await button(page, '刷新密钥').click();
  await expect(button(page, 'SurplusToken Connect 用于配置 Claude Code')).toBeEnabled();
  await button(page, 'SurplusToken Connect 用于配置 Claude Code').click();
  await previewAndApply(page);
  expect((await app.readConfig('claude')).env.ANTHROPIC_MODEL).toBe('fixture-model');
  expect((await app.readConfig('claude')).env.ANTHROPIC_AUTH_TOKEN).toBe('fixture-key-3');
});

test('model loading failure offers retry in the same dialog', async ({ page, app }) => {
  await login(page, app);
  let fail = true;
  await page.route('**/connect/models', async route => {
    if (fail) {
      fail = false;
      await route.fulfill({ status: 503, json: { error: '模型目录暂时不可用' } });
    } else await route.continue();
  });
  await button(page, 'Claude Code 配置').click();
  await expect(page.getByRole('dialog')).toContainText('模型目录暂时不可用');
  await button(page, '重试加载模型').click();
  await expect(button(page, '预览配置变更')).toBeEnabled();
  await previewAndApply(page);
});

test('failed apply keeps the dialog open and requires a fresh preview before retry', async ({ page, app }) => {
  await login(page, app);
  await button(page, 'Claude Code 配置').click();
  await button(page, '预览配置变更').click();
  await expect(button(page, '应用配置')).toBeEnabled();
  let fail = true;
  await page.route('**/connect/apply', async route => {
    if (fail) {
      fail = false;
      await route.fulfill({ status: 400, json: { error: '文件已改变，请重新预览' } });
    } else await route.continue();
  });
  await button(page, '应用配置').click();
  await expect(page.getByRole('dialog')).toContainText('文件已改变');
  expect(await app.readConfig('claude')).toEqual(app.original.claude);
  await button(page, '重新预览').click();
  await expect(button(page, '应用配置')).toBeEnabled();
  await button(page, '应用配置').click();
  await expect(page.getByRole('dialog')).toBeHidden();
  expect((await app.readConfig('claude')).env.ANTHROPIC_MODEL).toBe('fixture-model');
});
