import assert from 'node:assert/strict';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';
import { mkdtemp, mkdir, rm, readFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { createServer } from 'node:http';
import { createHash } from 'node:crypto';
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
const epub = await readFile(new URL('../testdata/synthetic.epub', import.meta.url));
const sha256 = createHash('sha256').update(epub).digest('hex');
const uuid = '123e4567-e89b-42d3-a456-426614174000';
const authorization = 'Basic ' + Buffer.from('synthetic:synthetic').toString('base64');
const fake = createServer((req, res) => {
  if (req.headers.authorization !== authorization) {
    res.writeHead(401);
    res.end();
    return;
  }
  const path = new URL(req.url, 'http://localhost').pathname;
  if (path === '/opds/search') {
    res.setHeader('content-type', 'application/atom+xml');
    res.end(
      '<feed xmlns="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/terms/">' +
        '<entry><id>urn:uuid:' +
        uuid +
        '</id><title>中文 packed smoke</title><dc:language>zh</dc:language>' +
        '<link rel="http://opds-spec.org/acquisition" href="download/1/epub/" type="application/epub+zip" length="1"/>' +
        '</entry></feed>',
    );
  } else if (path === '/ajax/book/' + uuid) {
    res.setHeader('content-type', 'application/json');
    res.end(
      JSON.stringify({
        application_id: '1',
        uuid,
        title: '中文 packed smoke',
        authors: [],
        languages: ['zh'],
        formats: ['EPUB'],
      }),
    );
  } else if (path === '/opds/download/1/epub/') {
    res.writeHead(200, { 'content-type': 'application/epub+zip', 'content-length': epub.length });
    res.end(epub);
  } else {
    res.writeHead(404);
    res.end();
  }
});
await new Promise((resolve) => fake.listen(0, '127.0.0.1', resolve));
const env = {
  ...process.env,
  CWA_BASE_URL: 'http://127.0.0.1:' + fake.address().port,
  CWA_USERNAME: 'synthetic',
  CWA_PASSWORD: 'synthetic',
  CWA_WRITE_ENABLED: 'false',
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
  let stderr = '',
    taskId;
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
    assert.equal(response.structuredContent.candidates.length, 1);
    if (n === 0) {
      const acquired = await client.callTool({
        name: 'acquire_book',
        arguments: {
          request_id: 'packed-direct',
          candidate_id: response.structuredContent.candidates[0].candidate_id,
          required_format: 'epub',
        },
      });
      assert(!acquired.isError);
      taskId = acquired.structuredContent.id;
    }
    const deadline = Date.now() + 10000;
    for (;;) {
      const task = await client.callTool({ name: 'get_task', arguments: { task_id: taskId } });
      assert(!task.isError);
      if (task.structuredContent.state === 'ready') break;
      assert(Date.now() < deadline, 'Packed task did not become ready');
      await new Promise((resolve) => setTimeout(resolve, 25));
    }
    const direct = await client.callTool({
      name: 'get_download_url',
      arguments: { task_id: taskId },
    });
    assert(!direct.isError);
    const link = direct.structuredContent;
    assert.equal(link.delivery, 'opds');
    assert.equal(link.url, env.CWA_BASE_URL + '/opds/download/1/epub/');
    assert.equal(link.web_url, env.CWA_BASE_URL + '/book/1');
    assert.deepEqual(link.authentication, { scheme: 'basic' });
    assert.equal(link.expires_at, undefined);
    assert.equal(link.verification.sha256, sha256);
    assert.equal(link.verification.size_bytes, epub.length);
    const download = await fetch(link.url, { headers: { authorization } });
    assert.equal(download.status, 200);
    assert.deepEqual(Buffer.from(await download.arrayBuffer()), epub);
    const gateway = await client.callTool({
      name: 'get_download_url',
      arguments: { task_id: taskId, delivery: 'gateway' },
    });
    assert(!gateway.isError);
    assert.equal(gateway.structuredContent.delivery, 'gateway');
    assert.equal(typeof gateway.structuredContent.expires_at, 'string');
    const signedDownload = await fetch(gateway.structuredContent.url);
    assert.equal(signedDownload.status, 200);
    assert.deepEqual(Buffer.from(await signedDownload.arrayBuffer()), epub);
    const upload = await client.callTool({
      name: 'upload_book',
      arguments: {
        request_id: 'packed-write-disabled',
        title: 'synthetic',
        filename: 'book.epub',
        size_bytes: epub.length,
        required_format: 'epub',
      },
    });
    assert.equal(upload.structuredContent.error.code, 'WRITE_DISABLED');
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
      opds_direct_download: true,
      signed_gateway_download: true,
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
