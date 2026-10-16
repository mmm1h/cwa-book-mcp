import { afterEach, expect, test } from 'vitest';
import { createHash } from 'node:crypto';
import { readdir } from 'node:fs/promises';
import { fetch } from 'undici';
import { createApp, type App } from '../src/app.js';
import { acquireSchema } from '../src/tasks.js';
import { searchSchema, type Format } from '../src/domain.js';
import { book, epub, pdf, listen, mockCwa, testConfig, until } from './fixtures.js';

const cleanups: (() => Promise<unknown>)[] = [];
afterEach(async () => {
  for (const cleanup of cleanups.splice(0).reverse()) await cleanup();
});
async function setup(basePath = '') {
  const mock = await mockCwa(basePath);
  cleanups.push(mock.close);
  const temp = await testConfig(mock.url, { CWA_WRITE_ENABLED: 'false' });
  cleanups.push(temp.cleanup);
  const app = await createApp(temp.config);
  cleanups.push(app.close);
  return { app, mock, temp };
}
async function start(app: App, mock: Awaited<ReturnType<typeof mockCwa>>, format: Format = 'epub') {
  mock.state.books = [book({ formats: [format] })];
  const result = await app.catalog.search(searchSchema.parse({ query: '合成测试书' }));
  const candidates = result.candidates as { candidate_id: string; acquisition_urls?: unknown }[];
  expect(candidates[0]!.acquisition_urls).toBeUndefined();
  return app.tasks.acquire(
    acquireSchema.parse({
      request_id: 'direct-download',
      candidate_id: candidates[0]!.candidate_id,
      required_format: format,
    }),
  ).id as string;
}
async function ready(app: App, taskId: string) {
  await until(() => ['ready', 'blocked', 'failed'].includes(app.store.get(taskId).state));
  expect(app.store.get(taskId).state).toBe('ready');
}

test.each(['epub', 'pdf'] as const)(
  'default %s delivery honors the acquisition href and base path, authenticates and ignores stale feed length',
  async (format) => {
    const { app, mock } = await setup('/library');
    mock.state.acquisitionHref = 'download/1/' + format;
    mock.state.opdsLength = 1;
    const taskId = await start(app, mock, format);
    await ready(app, taskId);
    const before = Date.now();
    const result = await app.gateway.downloadUrl(taskId);
    const data = format === 'epub' ? epub : pdf;
    expect(result).toEqual({
      task_id: taskId,
      delivery: 'opds',
      url: mock.url + '/opds/download/1/' + format,
      web_url: mock.url + '/book/1',
      authentication: { scheme: 'basic' },
      verification: {
        size_bytes: data.length,
        sha256: createHash('sha256').update(data).digest('hex'),
        at: expect.any(Number),
      },
    });
    expect((result.verification as { at: number }).at).toBeGreaterThanOrEqual(before);
    expect(JSON.stringify(result)).not.toContain('synthetic-password');
    expect(result.expires_at).toBeUndefined();
    const task = app.tasks.get(taskId) as { cwa: { acquisition_urls?: unknown } };
    expect(task.cwa.acquisition_urls).toBeUndefined();
    expect((await fetch(result.url as string)).status).toBe(401);
    const response = await fetch(result.url as string, {
      headers: {
        authorization: 'Basic ' + Buffer.from('reader:synthetic-password').toString('base64'),
      },
    });
    expect(response.status).toBe(200);
    expect(Buffer.from(await response.arrayBuffer())).toEqual(data);
    expect((await readdir(app.files.dir)).filter((name) => name.startsWith('verify-'))).toEqual([]);
    expect(mock.state.uploads).toBe(0);
  },
);

test('persisted tasks without acquisition URLs still return standard CWA links after restart', async () => {
  const { app, mock, temp } = await setup('/library');
  const taskId = await start(app, mock);
  await ready(app, taskId);
  app.store.mutate(taskId, (task) => {
    delete task.cwa!.acquisition_urls;
  });
  await app.close();
  const restarted = await createApp(temp.config);
  cleanups.push(restarted.close);
  const result = await restarted.gateway.downloadUrl(taskId);
  expect(result.url).toBe(mock.url + '/opds/download/1/epub/');
  expect(result.web_url).toBe(mock.url + '/book/1');
  expect(result.delivery).toBe('opds');
});

test.each([401, 403, 404])(
  'upstream %d after ready prevents issuing a download link',
  async (status) => {
    const { app, mock } = await setup();
    const taskId = await start(app, mock);
    await ready(app, taskId);
    mock.state.downloadStatus = status;
    await expect(app.gateway.downloadUrl(taskId)).rejects.toMatchObject({
      code: status === 404 ? 'BOOK_NOT_FOUND' : 'AUTH_EXPIRED',
    });
    mock.state.downloadStatus = 200;
    expect((await app.gateway.downloadUrl(taskId)).delivery).toBe('opds');
  },
);

test.each(['invalid_file', 'login_html', 'identity_changed', 'format_removed', 'book_deleted'])(
  '%s after ready is detected before issuing an OPDS URL',
  async (failure) => {
    const { app, mock } = await setup();
    const taskId = await start(app, mock);
    await ready(app, taskId);
    const errors: Record<string, string> = {
      invalid_file: 'FILE_INVALID',
      login_html: 'CWA_FILE_INVALID',
      identity_changed: 'BOOK_IDENTITY_CHANGED',
      format_removed: 'FORMAT_UNAVAILABLE',
      book_deleted: 'BOOK_NOT_FOUND',
    };
    if (failure === 'invalid_file') mock.state.badFile = true;
    if (failure === 'login_html') mock.state.downloadType = 'text/html';
    if (failure === 'identity_changed') mock.state.identityChanged = true;
    if (failure === 'format_removed') mock.state.books[0]!.formats = ['pdf'];
    if (failure === 'book_deleted') mock.state.books = [];
    await expect(app.gateway.downloadUrl(taskId)).rejects.toMatchObject({ code: errors[failure] });
    expect((await readdir(app.files.dir)).filter((name) => name.startsWith('verify-'))).toEqual([]);
  },
);

test.each(['cross_origin', 'userinfo', 'query', 'fragment'])(
  'OPDS acquisition with %s is rejected before caching or returning a link',
  async (kind) => {
    const { app, mock } = await setup();
    mock.state.books = [book()];
    const url = new URL('/opds/download/1/epub/', mock.url);
    if (kind === 'cross_origin') url.hostname = 'other.example.invalid';
    if (kind === 'userinfo') {
      url.username = 'private-user';
      url.password = 'private-secret';
    }
    if (kind === 'query') url.search = '?token=private-secret';
    if (kind === 'fragment') url.hash = 'private-secret';
    mock.state.acquisitionHref = url.href;
    await expect(app.cwa.search('合成测试书')).rejects.toMatchObject({
      code: kind === 'cross_origin' || kind === 'userinfo' ? 'ORIGIN_BLOCKED' : 'UPSTREAM_INVALID',
    });
  },
);

test('cross-origin download redirects never receive CWA authentication', async () => {
  const { app, mock } = await setup();
  const taskId = await start(app, mock);
  await ready(app, taskId);
  let requests = 0;
  const other = await listen((_req, res) => {
    requests++;
    res.end();
  });
  cleanups.push(other.close);
  mock.state.downloadRedirect = other.url + '/book.epub';
  await expect(app.gateway.downloadUrl(taskId)).rejects.toMatchObject({ code: 'ORIGIN_BLOCKED' });
  expect(requests).toBe(0);
});

test('queued and cancelled tasks cannot obtain download links', async () => {
  const { app, mock } = await setup();
  const taskId = await start(app, mock);
  await expect(app.gateway.downloadUrl(taskId)).rejects.toMatchObject({ code: 'TASK_NOT_READY' });
  await ready(app, taskId);
  app.tasks.acquire(acquireSchema.parse({ action: 'cancel', task_id: taskId }));
  await expect(app.gateway.downloadUrl(taskId)).rejects.toMatchObject({ code: 'TASK_NOT_READY' });
});
