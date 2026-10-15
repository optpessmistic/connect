import { test as base, expect } from '@playwright/test';
import { execFile, spawn } from 'node:child_process';
import { once } from 'node:events';
import { mkdir, mkdtemp, readFile, realpath, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { promisify } from 'node:util';
import { createServer } from 'vite';

const frontend = fileURLToPath(new URL('..', import.meta.url));
const repository = path.dirname(frontend);
const origin = 'http://127.0.0.1:5173';
const execFileAsync = promisify(execFile);

async function stop(child) {
  if (!child?.pid || child.exitCode !== null || child.signalCode !== null) return;
  const exited = once(child, 'exit', { signal: AbortSignal.timeout(15_000) });
  // Install a handler immediately, including while taskkill is still running.
  exited.catch(() => {});
  if (process.platform === 'win32') {
    // Only this fixture's live ChildProcess PID and its descendants are targeted.
    try { await execFileAsync('taskkill.exe', ['/PID', String(child.pid), '/T', '/F'], { windowsHide: true, timeout: 5_000 }); }
    catch { child.kill('SIGKILL'); }
  } else {
    // Every child is spawned in its own process group, including Go compilers.
    try { process.kill(-child.pid, 'SIGKILL'); }
    catch (error) { if (error.code !== 'ESRCH') throw error; }
  }
  try { await exited; }
  catch { throw new Error(`Fixture child ${child.pid} did not stop within 15 seconds`); }
}

async function removeOwned(directory) {
  // Only directories created by this fixture can be recursively removed.
  if (path.dirname(directory) !== path.resolve(tmpdir()) || !path.basename(directory).startsWith('connect-e2e-')) {
    throw new Error('Refusing to remove a directory outside the E2E temporary workspace');
  }
  await rm(directory, { recursive: true, force: true, maxRetries: 3 });
}

async function compile(binary) {
  const child = spawn(process.env.CONNECT_GO || 'go', ['build', '-o', binary, './cmd/connect-dev'], {
    cwd: repository, windowsHide: true, detached: process.platform !== 'win32', stdio: ['ignore', 'pipe', 'pipe'],
  });
  let output = '';
  child.stdout.on('data', data => { output += data; });
  child.stderr.on('data', data => { output += data; });
  let timer;
  const timedOut = new Promise((_, reject) => { timer = setTimeout(() => reject(new Error('Fixture API build timed out')), 120_000); });
  try {
    const [code] = await Promise.race([once(child, 'exit'), timedOut]);
    if (code !== 0) throw new Error(`Fixture API build failed (${code}): ${output}`);
  } finally {
    clearTimeout(timer);
    await stop(child);
  }
}

async function launch(binary, directory) {
  const child = spawn(binary, ['--fixture-dir', directory, '--mock', '--listen', '127.0.0.1:0'], {
    cwd: repository, windowsHide: true, detached: process.platform !== 'win32', stdio: ['ignore', 'pipe', 'pipe'],
  });
  try {
    const ready = await new Promise((resolve, reject) => {
      let stdout = '';
      let stderr = '';
      const timer = setTimeout(() => finish(new Error(`Fixture API startup timed out: ${stderr}`)), 20_000);
      const finish = (error, value) => {
        clearTimeout(timer);
        child.off('exit', onExit);
        child.off('error', onError);
        if (error) reject(error); else resolve(value);
      };
      const onExit = code => finish(new Error(`Fixture API exited (${code}): ${stderr}`));
      const onError = error => finish(error);
      child.on('error', onError);
      child.on('exit', onExit);
      child.stderr.on('data', data => { stderr += data; });
      child.stdout.on('data', data => {
        stdout += data;
        const line = stdout.split(/\r?\n/).find(value => value.startsWith('CONNECT_DEV_READY '));
        if (!line || !stdout.endsWith('\n')) return;
        try { finish(null, JSON.parse(line.slice('CONNECT_DEV_READY '.length))); }
        catch (error) { finish(error); }
      });
    });
    if (!ready.mock || path.resolve(ready.fixture_dir) !== await realpath(directory) || new URL(ready.address).hostname !== '127.0.0.1') {
      throw new Error('Fixture API did not confirm the expected isolated offline mode');
    }
    return { child, address: ready.address };
  } catch (error) {
    await stop(child);
    throw error;
  }
}

export const test = base.extend({
  backendBinary: [async ({}, use) => {
    const directory = await mkdtemp(path.join(tmpdir(), 'connect-e2e-build-'));
    const binary = path.join(directory, process.platform === 'win32' ? 'connect-dev.exe' : 'connect-dev');
    try { await compile(binary); await use(binary); }
    finally { await removeOwned(directory); }
  }, { scope: 'worker', timeout: 130_000 }],

  app: async ({ backendBinary }, use) => {
    const directory = await mkdtemp(path.join(tmpdir(), 'connect-e2e-state-'));
    let api;
    let vite;
    try {
      for (const client of ['claude', 'opencode']) await mkdir(path.join(directory, client));
      const original = { claude: { env: { USER_KEEP: 'preserved' } }, opencode: { theme: 'dark', providers: { custom: { name: 'preserved' } } } };
      const configPath = client => path.join(directory, client, client === 'claude' ? 'settings.json' : 'opencode.jsonc');
      for (const [client, config] of Object.entries(original)) await writeFile(configPath(client), JSON.stringify(config, null, 2));
      api = await launch(backendBinary, directory);
      vite = await createServer({
        root: frontend, configFile: false, logLevel: 'error',
        server: { host: '127.0.0.1', port: 5173, strictPort: true, proxy: { '/connect/': { target: api.address, changeOrigin: false } } },
      });
      await vite.listen();
      await use({ url: origin, original, readConfig: async client => JSON.parse(await readFile(configPath(client), 'utf8')) });
    } finally {
      try { await vite?.close(); }
      finally {
        try { await stop(api?.child); }
        finally { await removeOwned(directory); }
      }
    }
  },

  page: async ({ context, app }, use) => {
    const errors = [];
    const external = [];
    await context.route('**/*', async route => {
      if (new URL(route.request().url()).origin !== app.url) {
        external.push(route.request().url());
        await route.abort();
      } else await route.continue();
    });
    const page = await context.newPage();
    page.on('pageerror', error => errors.push(error.message));
    try { await use(page); }
    finally {
      expect(errors, 'browser runtime errors').toEqual([]);
      expect(external, 'requests outside the isolated local UI').toEqual([]);
    }
  },
});
export { expect };

export const button = (page, name) => page.getByRole('button', { name, exact: true });
export async function login(page, app) {
  await page.goto(app.url);
  await expect(button(page, '登录')).toBeEnabled();
  await page.getByRole('textbox', { name: '账号', exact: true }).fill('ui-fixture@example.test');
  await page.getByLabel('密码', { exact: true }).fill('fixture-only');
  await button(page, '登录').click();
  await expect(button(page, 'API Key')).toBeVisible();
  await expect(page.getByText('测试目录模式：只修改测试文件，不影响本机客户端。')).toBeVisible();
}

export async function previewAndApply(page) {
  await button(page, '预览配置变更').click();
  await expect(button(page, '应用配置')).toBeEnabled();
  await button(page, '应用配置').click();
  await expect(page.getByRole('dialog')).toBeHidden();
  await expect(page.getByRole('status')).toContainText(/已写入|配置未变化/);
}

export async function restoreLatest(page) {
  const response = page.waitForResponse(response => response.url().endsWith('/connect/history'));
  await button(page, '配置记录').click();
  const records = await (await response).json();
  await expect(page.locator('.history-record')).toHaveCount(records.length);
  await page.locator('.history-record button:enabled').first().click();
  await expect(button(page, '确认恢复')).toBeEnabled();
  await button(page, '确认恢复').click();
  await expect(page.getByRole('dialog')).toBeHidden();
  await expect(page.getByRole('status')).toContainText('已恢复');
}
