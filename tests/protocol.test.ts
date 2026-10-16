import { afterEach, expect, test } from 'vitest';
import { spawn } from 'node:child_process';
import { once } from 'node:events';
import { resolve } from 'node:path';
import { Client } from '@modelcontextprotocol/client';
import { StdioClientTransport } from '@modelcontextprotocol/client/stdio';
import { fetch } from 'undici';
import { book, epub, mockCwa, testConfig, until } from './fixtures.js';
import { Store } from '../src/store.js';

const cleanups: (() => Promise<unknown>)[] = [];
afterEach(async () => {
  for (const cleanup of cleanups.splice(0).reverse()) await cleanup();
});
async function setup() {
  const cwa = await mockCwa();
  cleanups.push(cwa.close);
  const temp = await testConfig(cwa.url);
  cleanups.push(temp.cleanup);
  const env = {
    ...Object.fromEntries(
      Object.entries(process.env).filter(
        (entry): entry is [string, string] => typeof entry[1] === 'string',
      ),
    ),
    CWA_BASE_URL: cwa.url,
    CWA_USERNAME: 'reader',
    CWA_PASSWORD: 'synthetic-password',
    STATE_DIR: temp.config.stateDir,
    CWA_WRITE_ENABLED: 'true',
    POLL_INTERVAL_SECONDS: '1',
  };
  return { cwa, temp, env };
}
async function connect(env: Record<string, string>) {
  let stderr = '';
  const transport = new StdioClientTransport({
    command: process.execPath,
    args: [resolve('dist/cli.js')],
    env,
    stderr: 'pipe',
  });
  const client = new Client({ name: 'acceptance-client', version: '1.0.0' });
  transport.stderr?.on('data', (data) => {
    stderr += String(data);
  });
  await client.connect(transport);
  cleanups.push(() => client.close());
  return { transport, client, stderr: () => stderr };
}
test('real stdio child lists six tools, returns structured/text results and exits on EOF', async () => {
  const { env, temp } = await setup();
  const { client, stderr } = await connect(env);
  const list = await client.listTools();
  expect(list.tools.map((t) => t.name).sort()).toEqual([
    'acquire_book',
    'download_book',
    'get_download_url',
    'get_task',
    'search_books',
    'upload_book',
  ]);
  const result = await client.callTool({ name: 'search_books', arguments: { query: '中文测试' } });
  expect(result.isError).not.toBe(true);
  expect(result.structuredContent).toMatchObject({ candidates: [], partial: true });
  const missing = await client.callTool({
    name: 'get_task',
    arguments: { task_id: '123e4567-e89b-42d3-a456-426614174000' },
  });
  expect(missing.isError).toBe(true);
  expect(missing.structuredContent).toMatchObject({ error: { code: 'TASK_NOT_FOUND' } });
  await client.close();
  const store = new Store(temp.config.stateDir, temp.config.identity);
  store.close();
  expect(stderr()).toBe('');
});
test('stdio defaults to authenticated CWA OPDS links and keeps signed gateway delivery explicit', async () => {
  const { cwa, env } = await setup();
  cwa.state.books = [book()];
  const { client, stderr } = await connect(env);
  const search = await client.callTool({
    name: 'search_books',
    arguments: { query: '合成测试书' },
  });
  const candidate = (search.structuredContent as { candidates: { candidate_id: string }[] })
    .candidates[0]!;
  const started = await client.callTool({
    name: 'acquire_book',
    arguments: {
      request_id: 'stdio-direct',
      candidate_id: candidate.candidate_id,
      required_format: 'epub',
    },
  });
  const taskId = (started.structuredContent as { id: string }).id;
  await until(async () => {
    const task = await client.callTool({ name: 'get_task', arguments: { task_id: taskId } });
    return (task.structuredContent as { state?: string })?.state === 'ready';
  });
  const direct = await client.callTool({
    name: 'get_download_url',
    arguments: { task_id: taskId },
  });
  expect(direct.isError).not.toBe(true);
  expect(direct.structuredContent).toMatchObject({
    delivery: 'opds',
    url: cwa.url + '/opds/download/1/epub/',
    web_url: cwa.url + '/book/1',
    authentication: { scheme: 'basic' },
    verification: { size_bytes: epub.length },
  });
  expect((direct.structuredContent as Record<string, unknown>)?.expires_at).toBeUndefined();
  expect(direct.content).toContainEqual({
    type: 'text',
    text: JSON.stringify(direct.structuredContent),
  });
  const signed = await client.callTool({
    name: 'get_download_url',
    arguments: { task_id: taskId, delivery: 'gateway' },
  });
  expect(signed.isError).not.toBe(true);
  expect(signed.structuredContent).toMatchObject({
    delivery: 'gateway',
    expires_at: expect.any(String),
    size_bytes: epub.length,
  });
  const response = await fetch((signed.structuredContent as { url: string }).url);
  expect(response.status).toBe(200);
  expect(Buffer.from(await response.arrayBuffer())).toEqual(epub);
  const invalid = await client.callTool({
    name: 'get_download_url',
    arguments: { task_id: taskId, delivery: 'unknown' },
  });
  expect(invalid.isError).toBe(true);
  expect(stderr()).toBe('');
});

test('SIGKILL after CWA receives a submission recovers without reuploading', async () => {
  const { cwa, env, temp } = await setup();
  cwa.state.hangUpload = true;
  const first = await connect(env);
  const prepared = await first.client.callTool({
    name: 'upload_book',
    arguments: {
      request_id: 'crash',
      title: '合成测试书',
      authors: ['测试作者'],
      language: 'zh',
      filename: 'book.epub',
      size_bytes: epub.length,
      required_format: 'epub',
    },
  });
  const data = prepared.structuredContent as {
    task_id: string;
    upload: { url: string; headers: Record<string, string> };
  };
  const response = await fetch(data.upload.url, {
    method: 'PUT',
    headers: data.upload.headers,
    body: epub,
  });
  expect(response.status).toBe(200);
  await response.text();
  await until(() => cwa.state.uploads === 1);
  const pid = first.transport.pid!;
  process.kill(pid, 'SIGKILL');
  await until(() => {
    try {
      process.kill(pid, 0);
      return false;
    } catch {
      return true;
    }
  });
  const second = await connect(env);
  let task: Record<string, unknown> | undefined;
  await until(async () => {
    const r = await second.client.callTool({
      name: 'get_task',
      arguments: { task_id: data.task_id },
    });
    task = r.structuredContent as Record<string, unknown>;
    return task?.state === 'ready';
  });
  expect(task?.state).toBe('ready');
  expect(cwa.state.uploads).toBe(1);
  await second.client.close();
  const store = new Store(temp.config.stateDir, temp.config.identity);
  expect(store.get(data.task_id).state).toBe('ready');
  store.close();
});
test.each(['2024-11-05', '2025-03-26', '2025-06-18', '2025-11-25'])(
  'negotiates protocol %s with pure JSON stdout',
  async (protocolVersion) => {
    const { env } = await setup();
    const child = spawn(process.execPath, [resolve('dist/cli.js')], {
      env,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    let stdout = '',
      stderr = '';
    child.stdout.on('data', (data) => {
      stdout += String(data);
    });
    child.stderr.on('data', (data) => {
      stderr += String(data);
    });
    cleanups.push(async () => {
      if (child.exitCode === null && !child.killed) child.kill();
    });
    child.stdin.write(
      JSON.stringify({
        jsonrpc: '2.0',
        id: 1,
        method: 'initialize',
        params: {
          protocolVersion,
          capabilities: {},
          clientInfo: { name: 'raw-client', version: '1' },
        },
      }) + '\n',
    );
    await until(() => stdout.includes('\n'));
    const first = JSON.parse(stdout.trim().split('\n')[0]!);
    expect(first.result.protocolVersion).toBe(protocolVersion);
    const exited = once(child, 'exit');
    child.stdin.end();
    await exited;
    for (const line of stdout.trim().split('\n')) expect(JSON.parse(line).jsonrpc).toBe('2.0');
    expect(stderr).toBe('');
    expect(child.exitCode).toBe(0);
  },
);
