import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, mkdir, rm, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'node:http';
import { Client } from '@modelcontextprotocol/client';
import { StdioClientTransport } from '@modelcontextprotocol/client/stdio';

const exec = promisify(execFile);
const root = fileURLToPath(new URL('../', import.meta.url));
const npm = process.env.npm_execpath || process.env.NPM_CLI_PATH;
assert(npm, 'Run via npm run test:package, or set NPM_CLI_PATH');
const temp = await mkdtemp(join(tmpdir(), 'cwa npm 中文 space-'));
const cwd = join(temp, 'empty cwd'),
  cache = join(temp, 'empty cache');
await mkdir(cwd);
await mkdir(cache);
const fake = createServer((_req, res) => {
  res.setHeader('content-type', 'application/atom+xml');
  res.end('<feed xmlns="http://www.w3.org/2005/Atom"></feed>');
});
await new Promise((resolve) => fake.listen(0, '127.0.0.1', resolve));
const env = {
  ...process.env,
  CWA_BASE_URL: 'http://127.0.0.1:' + fake.address().port,
  CWA_USERNAME: 'synthetic',
  CWA_PASSWORD: 'synthetic',
  STATE_DIR: join(temp, 'state'),
};
const base = ['exec', '--yes', '--ignore-scripts', '--no-audit', '--no-fund', '--cache', cache];
const clients = [];
try {
  const packed = JSON.parse(
    (
      await exec(
        process.execPath,
        [npm, 'pack', '--ignore-scripts', '--json', '--pack-destination', temp],
        { cwd: root },
      )
    ).stdout,
  )[0];
  const files = packed.files.map((f) => f.path);
  assert(files.includes('npm-shrinkwrap.json'));
  assert(files.includes('dist/cli.js'));
  assert(!files.some((f) => /^(src|tests|testdata|\.work|\.tools|node_modules)\//.test(f)));
  assert(!files.some((f) => /\.go$|\.exe$|\.tar\.gz$|sqlite|ticket\.key|\.env$/.test(f)));
  const tarball = resolve(temp, packed.filename);
  const published = process.env.PUBLISHED_PACKAGE_SPEC;
  if (published) {
    assert.equal(
      published,
      packed.name + '@' + packed.version,
      'Public smoke must use this exact package/version',
    );
    const registry = JSON.parse(
      (
        await exec(
          process.execPath,
          [
            npm,
            'view',
            published,
            'dist.integrity',
            '--json',
            '--registry=https://registry.npmjs.org',
          ],
          { cwd },
        )
      ).stdout,
    );
    assert.equal(registry, packed.integrity, 'Published tarball differs from the checked artifact');
  }
  const args = [
    ...base,
    ...(published ? ['--registry=https://registry.npmjs.org'] : []),
    '--package',
    published || tarball,
    '--',
    'cwa-book-mcp',
  ];
  const before = performance.now();
  const version = await exec(process.execPath, [npm, ...args, '--version'], {
    cwd,
    env,
    timeout: 180000,
  });
  assert.equal(version.stdout.trim(), packed.version);
  const coldMs = performance.now() - before;
  let stderr = '';
  for (let n = 0; n < 2; n++) {
    const transport = new StdioClientTransport({
      command: process.execPath,
      args: [npm, ...args],
      cwd,
      env,
      stderr: 'pipe',
    });
    transport.stderr?.on('data', (chunk) => {
      stderr += String(chunk);
    });
    const client = new Client({ name: 'packed-npx-client', version: '1.0.0' });
    clients.push(client);
    await client.connect(transport);
    assert.equal((await client.listTools()).tools.length, 5);
    const response = await client.callTool({
      name: 'search_books',
      arguments: { query: '中文 packed smoke' },
    });
    assert.equal(response.isError, undefined);
    assert.deepEqual(response.structuredContent.candidates, []);
    await client.close();
  }
  const key = await readFile(join(env.STATE_DIR, 'ticket.key'));
  assert.equal(key.length, 32);
  assert(!stderr.includes('synthetic'));
  assert.deepEqual(
    stderr.split(/\r?\n/).filter((line) => line && !/^npm (warn|notice)\b/.test(line)),
    [],
  );
  console.log(
    JSON.stringify({
      node: process.version,
      platform: process.platform,
      arch: process.arch,
      version: packed.version,
      tarball_bytes: packed.size,
      files: files.length,
      integrity: packed.integrity,
      cold_install_ms: Math.round(coldMs),
      distribution: published ? 'public_registry' : 'local_tarball',
      empty_cwd: true,
      empty_cache: true,
      install_scripts: false,
      stdio_tools: 5,
      cached_restarts: 2,
      state_persistent: true,
    }),
  );
} finally {
  for (const client of clients) await client.close();
  fake.closeAllConnections();
  await new Promise((resolve) => fake.close(resolve));
  await rm(temp, { recursive: true, force: true });
}
