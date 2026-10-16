import assert from 'node:assert/strict';
import { spawn, execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { createServer } from 'node:http';
import { mkdtemp, mkdir, writeFile, rm } from 'node:fs/promises';
import { join, resolve } from 'node:path';
import { tmpdir } from 'node:os';
import { Client, StreamableHTTPClientTransport } from '@modelcontextprotocol/client';
import { fetch } from 'undici';

const exec = promisify(execFile),
  delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const npm = process.env.npm_execpath || process.env.NPM_CLI_PATH;
assert(npm, 'Run via npm run test:clients');
const temp = await mkdtemp(join(tmpdir(), 'cwa-client-'));
const clients = [];
const fake = createServer((_req, res) => {
  res.setHeader('content-type', 'application/atom+xml');
  res.end('<feed xmlns="http://www.w3.org/2005/Atom"></feed>');
});
await new Promise((resolve) => fake.listen(0, '127.0.0.1', resolve));
async function port() {
  const server = createServer();
  await new Promise((resolve) => server.listen(0, '127.0.0.1', resolve));
  const value = server.address().port;
  await new Promise((resolve) => server.close(resolve));
  return value;
}
let hub,
  logs = '';
try {
  const packed = JSON.parse(
    (
      await exec(process.execPath, [
        npm,
        'pack',
        '--ignore-scripts',
        '--json',
        '--pack-destination',
        temp,
      ])
    ).stdout,
  )[0];
  const tarball = join(temp, packed.filename);
  const settings = {
    type: 'stdio',
    command: 'npx',
    args: ['-y', '--ignore-scripts', '--package', tarball, 'cwa-book-mcp'],
    perSessionClient: false,
    env: {
      CWA_BASE_URL: 'http://127.0.0.1:' + fake.address().port,
      CWA_USERNAME: 'synthetic',
      CWA_PASSWORD: 'synthetic',
      STATE_DIR: join(temp, 'hub-book-state'),
    },
  };
  if (process.env.MCPHUB_ENTRY) {
    const hubPort = await port();
    const settingsPath = join(temp, 'mcp_settings.json');
    await writeFile(
      settingsPath,
      JSON.stringify({
        mcpServers: { 'cwa-books': settings },
        users: [],
        systemConfig: {
          routing: { enableBearerAuth: false, skipAuth: true },
          oauthServer: { enabled: false },
        },
      }),
    );
    hub = spawn(process.execPath, [resolve(process.env.MCPHUB_ENTRY)], {
      cwd: temp,
      env: {
        ...process.env,
        PORT: String(hubPort),
        MCPHUB_SETTING_PATH: settingsPath,
        NODE_ENV: 'test',
        DISABLE_WEB: 'true',
        USE_DB: 'false',
        ADMIN_PASSWORD: 'synthetic-client-acceptance',
        JWT_SECRET: 'synthetic-client-acceptance-key-32-bytes',
      },
      stdio: ['ignore', 'pipe', 'pipe'],
    });
    for (const stream of [hub.stdout, hub.stderr])
      stream.on('data', (chunk) => {
        logs = (logs + String(chunk)).slice(-32000);
      });
    const base = 'http://127.0.0.1:' + hubPort;
    const deadline = Date.now() + 120000;
    for (;;) {
      if (hub.exitCode !== null) throw Error('MCPHub exited: ' + logs);
      try {
        const response = await fetch(base + '/health');
        if (response.status === 200) break;
        await response.text();
      } catch {
        /* starting */
      }
      if (Date.now() > deadline) throw Error('MCPHub startup timed out: ' + logs);
      await delay(500);
    }
    for (let attempt = 0; attempt < 30; attempt++) {
      const client = new Client({ name: 'hub-device-one', version: '1.0.0' });
      try {
        const transport = new StreamableHTTPClientTransport(new URL(base + '/mcp/cwa-books'));
        await client.connect(transport);
        clients.push(client);
        const tools = await client.listTools();
        if (tools.tools.length !== 5) {
          await client.close();
          await delay(1000);
          continue;
        }
        const search = tools.tools.find((t) => t.name.endsWith('search_books'));
        assert(search);
        const response = await client.callTool({
          name: search.name,
          arguments: { query: '中文 hub smoke' },
        });
        assert(!response.isError);
        await client.close();
        // A new downstream device must reuse the same upstream state owner.
        const next = new Client({ name: 'hub-device-two', version: '1.0.0' });
        clients.push(next);
        await next.connect(new StreamableHTTPClientTransport(new URL(base + '/mcp/cwa-books')));
        assert.equal((await next.listTools()).tools.length, 6);
        assert(
          !(await next.callTool({ name: search.name, arguments: { query: 'reconnect' } })).isError,
        );
        await next.close();
        console.log(
          JSON.stringify({
            client: 'MCPHub',
            version: '1.1.3',
            tools: 5,
            npx_tarball: true,
            shared_child_after_disconnect: true,
          }),
        );
        break;
      } catch (error) {
        await client.close();
        if (attempt === 29) throw Error(String(error) + '\n' + logs, { cause: error });
        await delay(1000);
      }
      if (attempt === 29) throw Error('MCPHub did not expose six tools');
    }
  }
  if (process.env.CLAUDE_CLI_PATH) {
    const configDir = join(temp, 'claude-config');
    await mkdir(configDir);
    const env = {
      ...process.env,
      CLAUDE_CONFIG_DIR: configDir,
      CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC: '1',
    };
    const executable = process.env.CLAUDE_CLI_PATH;
    const binaryArgs = executable.endsWith('.js') ? [executable] : [];
    const command = binaryArgs.length ? process.execPath : executable;
    const entry = {
      ...settings,
      env: { ...settings.env, STATE_DIR: join(temp, 'claude-book-state') },
    };
    delete entry.perSessionClient;
    await exec(
      command,
      [...binaryArgs, 'mcp', 'add-json', '--scope', 'user', 'cwa-books', JSON.stringify(entry)],
      { cwd: temp, env, timeout: 60000 },
    );
    const result = await exec(command, [...binaryArgs, 'mcp', 'list'], {
      cwd: temp,
      env,
      timeout: 90000,
    });
    assert(/cwa-books.*Connected/s.test(result.stdout), result.stdout + result.stderr);
    console.log(
      JSON.stringify({
        client: 'Claude Code',
        stdio_health: 'Connected',
        npx_tarball: true,
        isolated_config: true,
      }),
    );
  }
  assert(
    process.env.MCPHUB_ENTRY || process.env.CLAUDE_CLI_PATH,
    'Set MCPHUB_ENTRY and/or CLAUDE_CLI_PATH',
  );
} finally {
  for (const client of clients) await client.close();
  if (hub && hub.exitCode === null) {
    hub.kill();
    await delay(1000);
    if (hub.exitCode === null) hub.kill('SIGKILL');
  }
  fake.closeAllConnections();
  await new Promise((resolve) => fake.close(resolve));
  // Hub children close on stdin EOF; allow their SQLite handles to release on Windows.
  await rm(temp, { recursive: true, force: true, maxRetries: 20, retryDelay: 250 });
}
