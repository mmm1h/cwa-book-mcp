import { afterEach, describe, expect, test } from 'vitest';
import { fetch } from 'undici';
import { Readable } from 'node:stream';
import { createHash } from 'node:crypto';
import { createApp, type App } from '../src/app.js';
import { acquireSchema, uploadSchema } from '../src/tasks.js';
import { searchSchema } from '../src/domain.js';
import { epub, mockCwa, testConfig, until, book } from './fixtures.js';

const cleanups: (() => Promise<unknown>)[] = [];
afterEach(async () => {
  for (const cleanup of cleanups.splice(0).reverse()) await cleanup();
});
async function setup() {
  const mock = await mockCwa();
  cleanups.push(mock.close);
  const temp = await testConfig(mock.url);
  cleanups.push(temp.cleanup);
  const app = await createApp(temp.config);
  cleanups.push(app.close);
  return { app, mock, temp };
}
function prepare(app: App, key = 'upload-1') {
  return app.tasks.upload(
    uploadSchema.parse({
      request_id: key,
      title: '合成测试书',
      authors: ['测试作者'],
      language: 'zh',
      filename: '合成测试书.epub',
      size_bytes: epub.length,
      required_format: 'epub',
    }),
  );
}
async function settle(app: App, taskId: string) {
  await until(() =>
    ['ready', 'blocked', 'needs_review', 'failed'].includes(app.store.get(taskId).state),
  );
  return app.store.get(taskId);
}
describe('book workflow', () => {
  test('different request IDs cannot duplicate a pending submission for the same book', async () => {
    const { app, mock } = await setup();
    mock.state.dropUpload = true;
    mock.state.nextBook = book({ title: 'not yet indexed' });
    const first = prepare(app, 'first'),
      second = prepare(app, 'second');
    await app.tasks.receive(first.id, Readable.from(epub), epub.length);
    await app.tasks.receive(second.id, Readable.from(epub), epub.length);
    await until(() => app.store.get(first.id).submitted_at !== undefined);
    const blocked = await settle(app, second.id);
    expect(blocked.state).toBe('blocked');
    expect(blocked.error?.code).toBe('RELATED_SUBMISSION_PENDING');
    expect(blocked.waiting_for).toBe(first.id);
    expect(mock.state.uploads).toBe(1);
  });
  test('HTTP upload → CWA CSRF multipart → verified ready → signed download; repeated upload is idempotent', async () => {
    const { app, mock } = await setup();
    const t = prepare(app);
    expect(prepare(app).id).toBe(t.id);
    expect(() =>
      app.tasks.upload(
        uploadSchema.parse({
          request_id: 'upload-1',
          title: 'changed',
          filename: 'a.epub',
          size_bytes: epub.length,
          required_format: 'epub',
        }),
      ),
    ).toThrow('request_id');
    const upload = app.gateway.upload(t).upload as { url: string; headers: Record<string, string> };
    const put = () => fetch(upload.url, { method: 'PUT', headers: upload.headers, body: epub });
    expect((await put()).status).toBe(200);
    const ready = await settle(app, t.id);
    expect(ready.state).toBe('ready');
    expect(ready.verification?.sha256).toBe(createHash('sha256').update(epub).digest('hex'));
    expect(mock.state.uploads).toBe(1);
    expect((await put()).status).toBe(200);
    expect(mock.state.uploads).toBe(1);
    expect(Number(mock.state.headers[0]!['content-length'])).toBe(mock.state.bodies[0]!.length);
    expect(mock.state.bodies[0]!.includes(Buffer.from('Content-Type: application/epub+zip'))).toBe(
      true,
    );
    const result = await app.gateway.downloadUrl(t.id);
    const get = await fetch(result.url as string);
    expect(get.status).toBe(200);
    expect(Buffer.from(await get.arrayBuffer())).toEqual(epub);
    expect((await fetch(result.url as string, { headers: { range: 'bytes=0-1' } })).status).toBe(
      416,
    );
    const tampered = new URL(result.url as string);
    tampered.searchParams.set('ticket', tampered.searchParams.get('ticket')! + 'a');
    expect((await fetch(tampered)).status).toBe(403);
    app.tasks.acquire(acquireSchema.parse({ action: 'cancel', task_id: t.id }));
    expect((await fetch(result.url as string)).status).toBe(403);
    expect(mock.state.books).toHaveLength(1);
  });
  test('lost CWA upload response reconciles without a second submission', async () => {
    const { app, mock } = await setup();
    mock.state.dropUpload = true;
    const t = prepare(app);
    await app.tasks.receive(t.id, Readable.from(epub), epub.length);
    expect((await settle(app, t.id)).state).toBe('ready');
    expect(mock.state.uploads).toBe(1);
  });
  test('HTTP 200 with a CWA queue error preserves safe feedback across reconciliation and retry', async () => {
    const { app, mock } = await setup();
    app.config.confirmMs = 80;
    mock.state.rejectUpload = true;
    const t = prepare(app);
    await app.tasks.receive(t.id, Readable.from(epub), epub.length);
    const reviewed = await settle(app, t.id);
    expect(reviewed.state).toBe('needs_review');
    expect(reviewed.error?.code).toBe('RECONCILIATION_REQUIRED');
    expect(reviewed.submission_error).toMatchObject({
      code: 'UPLOAD_OUTCOME_UNKNOWN',
      message: 'CWA 反馈上传排队失败；检查 CWA 日志，并核对书库',
    });
    expect(JSON.stringify(app.tasks.get(t.id))).not.toContain('private server detail');
    app.tasks.acquire(acquireSchema.parse({ action: 'retry', task_id: t.id }));
    expect((await settle(app, t.id)).submission_error).toEqual(reviewed.submission_error);
    expect(mock.state.uploads).toBe(1);
  });
  test('CWA queue error still reconciles an already ingested book without resubmitting', async () => {
    const { app, mock } = await setup();
    mock.state.rejectUpload = true;
    mock.state.acceptDespiteRejection = true;
    const t = prepare(app);
    await app.tasks.receive(t.id, Readable.from(epub), epub.length);
    const ready = await settle(app, t.id);
    expect(ready.state).toBe('ready');
    expect(ready.submission_error?.message).toContain('排队失败');
    expect(ready.error).toBeUndefined();
    expect(mock.state.uploads).toBe(1);
  });
  test('unknown submission with no matching result remains reviewable; retry only reconciles', async () => {
    const { app, mock } = await setup();
    app.config.confirmMs = 80;
    mock.state.dropUpload = true;
    mock.state.nextBook = book({ title: 'other title' });
    const t = prepare(app);
    await app.tasks.receive(t.id, Readable.from(epub), epub.length);
    expect((await settle(app, t.id)).state).toBe('needs_review');
    app.tasks.acquire(acquireSchema.parse({ action: 'retry', task_id: t.id }));
    expect((await settle(app, t.id)).state).toBe('needs_review');
    expect(mock.state.uploads).toBe(1);
    const manuallyFound = app.catalog.expose(mock.state.books[0]!);
    app.tasks.acquire(
      acquireSchema.parse({
        action: 'resolve',
        task_id: t.id,
        candidate_id: manuallyFound.candidate_id,
      }),
    );
    expect((await settle(app, t.id)).state).toBe('ready');
    expect(mock.state.uploads).toBe(1);
  });
  test('missing input before submission can be uploaded again after retry', async () => {
    const { app, mock } = await setup();
    const task = prepare(app);
    await app.tasks.receive(task.id, Readable.from(epub), epub.length);
    await app.files.remove(task.id);
    expect((await settle(app, task.id)).error?.code).toBe('INPUT_MISSING');
    app.tasks.acquire(acquireSchema.parse({ action: 'retry', task_id: task.id }));
    expect(app.store.get(task.id).state).toBe('awaiting_upload');
    await app.tasks.receive(task.id, Readable.from(epub), epub.length);
    expect((await settle(app, task.id)).state).toBe('ready');
    expect(mock.state.uploads).toBe(1);
  });
  test('existing library book is reused; multiple matches require explicit resolve', async () => {
    const { app, mock } = await setup();
    mock.state.books = [book(), book({ id: '2' })];
    const t = prepare(app);
    await app.tasks.receive(t.id, Readable.from(epub), epub.length);
    expect((await settle(app, t.id)).state).toBe('needs_review');
    const task = app.tasks.get(t.id);
    const choices = task.choices as { candidate_id: string }[];
    app.tasks.acquire(
      acquireSchema.parse({
        action: 'resolve',
        task_id: t.id,
        candidate_id: choices[1]!.candidate_id,
      }),
    );
    expect((await settle(app, t.id)).state).toBe('ready');
    expect(mock.state.uploads).toBe(0);
  });
  test('wrong CWA file, reused numeric ID, failed search, and unavailable format never become ready or upload blindly', async () => {
    const { app, mock } = await setup();
    mock.state.books = [book()];
    mock.state.badFile = true;
    const candidate = app.catalog.expose(mock.state.books[0]!);
    const result = app.tasks.acquire(
      acquireSchema.parse({
        request_id: 'bad',
        candidate_id: candidate.candidate_id,
        required_format: 'epub',
      }),
    );
    const t = await settle(app, result.id as string);
    expect(t.state).toBe('blocked');
    expect(t.error?.code).toBe('FILE_INVALID');
    mock.state.badFile = false;
    mock.state.identityChanged = true;
    app.tasks.acquire(acquireSchema.parse({ action: 'retry', task_id: t.id }));
    expect((await settle(app, t.id)).error?.code).toBe('BOOK_IDENTITY_CHANGED');
    mock.state.identityChanged = false;
    mock.state.books[0]!.formats = ['pdf'];
    app.tasks.acquire(acquireSchema.parse({ action: 'retry', task_id: t.id }));
    expect((await settle(app, t.id)).error?.code).toBe('FORMAT_UNAVAILABLE');
    mock.state.books = [];
    mock.state.failSearch = true;
    const upload = prepare(app);
    await app.tasks.receive(upload.id, Readable.from(epub), epub.length);
    expect((await settle(app, upload.id)).state).toBe('blocked');
    expect(mock.state.uploads).toBe(0);
  });
  test('write defaults off but CWA acquire/download work without source credentials', async () => {
    const { app, mock } = await setup();
    app.config.writeEnabled = false;
    mock.state.books = [book()];
    expect(() => prepare(app)).toThrow('CWA_WRITE_ENABLED');
    const result = await app.catalog.search(searchSchema.parse({ query: '合成测试书' }));
    expect(result.partial).toBe(false);
    const candidates = result.candidates as { candidate_id: string }[];
    const started = app.tasks.acquire(
      acquireSchema.parse({
        request_id: 'read',
        candidate_id: candidates[0]!.candidate_id,
        required_format: 'epub',
      }),
    );
    expect((await settle(app, started.id as string)).state).toBe('ready');
  });
  test('complete file survives publication/DB gap and key/ticket survive fixed-port restart', async () => {
    const { app, mock, temp } = await setup();
    const t = prepare(app);
    const key = Buffer.from(app.store.key);
    await app.tasks.stop();
    await app.files.save(t.id, Readable.from(epub), 'epub', epub.length);
    const address = app.gateway.server.address() as { port: number };
    temp.config.filePort = address.port;
    const ticket = app.gateway.upload(t).upload as { url: string; headers: Record<string, string> };
    await app.close();
    const restarted = await createApp(temp.config);
    cleanups.push(restarted.close);
    expect(restarted.store.key).toEqual(key);
    expect((await settle(restarted, t.id)).state).toBe('ready');
    expect(
      (await fetch(ticket.url, { method: 'PUT', headers: ticket.headers, body: epub })).status,
    ).toBe(200);
    expect(mock.state.uploads).toBe(1);
  });
});
