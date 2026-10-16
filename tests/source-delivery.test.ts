import { afterEach, expect, test } from 'vitest';
import { readdir } from 'node:fs/promises';
import { createHash } from 'node:crypto';
import { fetch } from 'undici';
import { createApp, type App } from '../src/app.js';
import { acquireSchema, downloadBookSchema } from '../src/tasks.js';
import { searchSchema } from '../src/domain.js';
import { book, mockCwa, mockSource, testConfig, until } from './fixtures.js';

const cleanups: (() => Promise<unknown>)[] = [];
afterEach(async () => {
  for (const fn of cleanups.splice(0).reverse()) await fn();
});
async function setup(format: 'epub' | 'pdf' = 'epub', write = true) {
  const source = await mockSource(format),
    cwa = await mockCwa();
  cleanups.push(source.close, cwa.close);
  cwa.state.nextBook = book({ formats: [format] });
  const temp = await testConfig(cwa.url, {
    ZLIBRARY_BASE_URL: source.url,
    ZLIBRARY_USER_ID: '7',
    ZLIBRARY_USER_KEY: 'private-session-key',
    CWA_WRITE_ENABLED: String(write),
  });
  cleanups.push(temp.cleanup);
  const app = await createApp(temp.config);
  cleanups.push(app.close);
  const search = await app.catalog.search(
    searchSchema.parse({ query: '合成测试书', formats: [format] }),
  );
  const candidate = (search.candidates as { candidate_id: string }[])[0]!;
  const input = {
    request_id: 'direct-source',
    candidate_id: candidate.candidate_id,
    required_format: format,
  };
  const start = (extra: Record<string, unknown> = {}) =>
    app.tasks.download(downloadBookSchema.parse({ ...input, ...extra })).id as string;
  return { app, cwa, source, temp, input, start };
}
async function settle(app: App, id: string) {
  await until(() =>
    ['download_available', 'ready', 'blocked', 'needs_review'].includes(app.store.get(id).state),
  );
  return app.store.get(id);
}

test.each(['epub', 'pdf'] as const)(
  '%s direct delivery validates anonymously and reuses the same bytes for default CWA collection',
  async (format) => {
    const { app, cwa, source, input, start } = await setup(format);
    const id = start();
    expect(app.tasks.download(downloadBookSchema.parse({ ...input, save_to_cwa: true })).id).toBe(
      id,
    );
    expect(() => start({ save_to_cwa: false })).toThrow('request_id');
    await until(() => !!app.store.get(id).source_download?.verification);
    const early = await app.gateway.downloadUrl(id);
    expect(early).toMatchObject({
      delivery: 'source',
      status: 'link_issued',
      format,
      authentication: { scheme: 'none' },
      expires_at: null,
    });
    expect(early.url).toBe(source.url + '/file?ticket=single-file-capability');
    expect((await settle(app, id)).state).toBe('ready');
    expect(source.state.tickets).toBe(1);
    expect(source.state.downloads).toBe(1);
    expect(cwa.state.uploads).toBe(1);
    expect(cwa.state.bodies[0]!.includes(source.data)).toBe(true);
    const link = await app.gateway.downloadUrl(id);
    const response = await fetch(link.url as string);
    expect(Buffer.from(await response.arrayBuffer())).toEqual(source.data);
    expect(link.verification).toMatchObject({
      size_bytes: source.data.length,
      sha256: createHash('sha256').update(source.data).digest('hex'),
    });
    for (const headers of source.state.headers) {
      expect(headers.cookie).toBeUndefined();
      expect(headers['remix-userkey']).toBeUndefined();
      expect(headers.authorization).toBeUndefined();
    }
    const task = app.tasks.get(id);
    expect(task).toMatchObject({
      download: { status: 'available' },
      collection: { requested: true, state: 'ready' },
    });
    expect(JSON.stringify(task)).not.toMatch(
      /single-file-capability|private-session-key|source_requested_at/,
    );
    expect((await app.gateway.downloadUrl(id)).url).toBe(link.url);
    expect(source.state.tickets).toBe(1);
    expect(await readdir(app.files.dir)).toEqual([]);
  },
);

test('download-only needs no CWA write permission and cached collection is explicit and idempotent', async () => {
  const { app, cwa, source, start, input } = await setup('epub', false);
  expect(() => start()).toThrow('CWA_WRITE_ENABLED');
  cwa.state.failSearch = true;
  const id = start({ save_to_cwa: false });
  expect((await settle(app, id)).state).toBe('download_available');
  expect(app.tasks.get(id)).toMatchObject({
    collection: { requested: false, state: 'not_requested' },
  });
  expect(app.tasks.download(downloadBookSchema.parse({ ...input, save_to_cwa: false })).id).toBe(
    id,
  );
  expect(cwa.state.uploads).toBe(0);
  expect(() =>
    app.tasks.download(downloadBookSchema.parse({ action: 'collect', task_id: id })),
  ).toThrow('CWA_WRITE_ENABLED');
  cwa.state.failSearch = false;
  app.config.writeEnabled = true;
  for (let i = 0; i < 2; i++)
    app.tasks.download(downloadBookSchema.parse({ action: 'collect', task_id: id }));
  expect((await settle(app, id)).state).toBe('ready');
  expect(cwa.state.uploads).toBe(1);
  expect(source.state.tickets).toBe(1);
  expect(source.state.downloads).toBe(1);
});

test('CWA failure leaves original delivery usable and retry never re-downloads the source', async () => {
  const { app, cwa, source, start } = await setup();
  cwa.state.failSearch = true;
  const id = start();
  expect((await settle(app, id)).state).toBe('blocked');
  expect(app.tasks.get(id)).toMatchObject({
    download: { status: 'available' },
    collection: { state: 'blocked' },
  });
  expect((await app.gateway.downloadUrl(id)).delivery).toBe('source');
  cwa.state.failSearch = false;
  cwa.state.dropUpload = true;
  app.tasks.download(downloadBookSchema.parse({ action: 'retry', task_id: id }));
  expect((await settle(app, id)).state).toBe('ready');
  expect(cwa.state.uploads).toBe(1);
  expect(source.state.tickets).toBe(1);
});

test('an explicitly selected source original can be delivered while collection reuses CWA', async () => {
  const { app, cwa, source, start } = await setup();
  cwa.state.books = [cwa.state.nextBook];
  const id = start();
  expect((await settle(app, id)).state).toBe('ready');
  expect(source.state.tickets).toBe(1);
  expect(cwa.state.uploads).toBe(0);
  expect((await app.gateway.downloadUrl(id)).delivery).toBe('source');
});

test('CWA candidates and format conversion are rejected before consuming source quota', async () => {
  const { app, source, start, input } = await setup();
  expect(() => start({ required_format: 'pdf' })).toThrow('原文件');
  const candidate = app.catalog.expose(book());
  expect(() => start({ candidate_id: candidate.candidate_id })).toThrow('此候选来自 CWA');
  expect(source.state.tickets).toBe(0);
  expect(() =>
    app.tasks.download(downloadBookSchema.parse({ action: 'collect', ...input })),
  ).toThrow('只接受');
});

test.each(['privateLink', 'authRequired', 'badFile', 'dropTicket'] as const)(
  '%s cannot expose a source link or trigger automatic re-ticketing',
  async (failure) => {
    const { app, source, cwa, temp, start } = await setup();
    source.state[failure] = true;
    const id = start();
    const task = await settle(app, id);
    expect(task.state).toBe('blocked');
    expect(task.error?.code).toBe(
      {
        privateLink: 'SOURCE_LINK_PRIVATE',
        authRequired: 'SOURCE_LINK_AUTH_REQUIRED',
        badFile: 'FILE_INVALID',
        dropTicket: 'SOURCE_UNAVAILABLE',
      }[failure],
    );
    await expect(app.gateway.downloadUrl(id)).rejects.toMatchObject({ code: 'TASK_NOT_READY' });
    expect(() =>
      app.tasks.download(downloadBookSchema.parse({ action: 'retry', task_id: id })),
    ).toThrow('不会自动再次取票');
    await app.close();
    const restarted = await createApp(temp.config);
    cleanups.push(restarted.close);
    expect(restarted.store.get(id).state).toBe('blocked');
    expect(source.state.tickets).toBe(1);
    expect(cwa.state.uploads).toBe(0);
    expect(JSON.stringify(restarted.tasks.get(id))).not.toContain('private-session-key');
  },
);

test('download-only restart retains link and cache; expiry refuses collection and cleans temporary bytes', async () => {
  const { app, source, cwa, temp, start } = await setup();
  const id = start({ save_to_cwa: false });
  await settle(app, id);
  const link = await app.gateway.downloadUrl(id);
  await app.close();
  const second = await createApp(temp.config);
  cleanups.push(second.close);
  expect((await second.gateway.downloadUrl(id)).url).toBe(link.url);
  expect(await second.files.exists(id)).toBe(true);
  second.store.mutate(id, (task) => {
    task.source_download!.available_until = Date.now() - 1;
  });
  await expect(second.gateway.downloadUrl(id)).rejects.toMatchObject({
    code: 'SOURCE_LINK_EXPIRED',
  });
  expect(() =>
    second.tasks.download(downloadBookSchema.parse({ action: 'collect', task_id: id })),
  ).toThrow('缓存已过期');
  await second.close();
  const third = await createApp(temp.config);
  cleanups.push(third.close);
  expect(third.tasks.get(id)).toMatchObject({ download: { status: 'expired' } });
  expect(await third.files.exists(id)).toBe(false);
  expect(source.state.tickets).toBe(1);
  expect(cwa.state.uploads).toBe(0);
});

test('crash after a completed source file recovers validation without requesting another ticket', async () => {
  const { app, source, temp, start } = await setup();
  const id = start({ save_to_cwa: false });
  await settle(app, id);
  await app.close();
  // Simulate the fsync/rename completed immediately before the verification transaction.
  const { Store } = await import('../src/store.js');
  const store = new Store(temp.config.stateDir, temp.config.identity);
  store.mutate(id, (task) => {
    task.state = 'downloading';
    task.source_download!.verification = undefined;
    task.staged_sha256 = undefined;
  });
  store.close();
  const second = await createApp(temp.config);
  cleanups.push(second.close);
  expect(second.store.get(id).state).toBe('download_available');
  expect((await second.gateway.downloadUrl(id)).status).toBe('link_issued');
  expect(source.state.tickets).toBe(1);
});

test('lost cached file never causes a hidden second source download, and cancel revokes link issuance', async () => {
  const { app, cwa, source, start } = await setup();
  cwa.state.failSearch = true;
  const id = start();
  await settle(app, id);
  await app.files.remove(id);
  cwa.state.failSearch = false;
  app.tasks.download(downloadBookSchema.parse({ action: 'retry', task_id: id }));
  expect((await settle(app, id)).error?.code).toBe('INPUT_MISSING');
  expect(source.state.tickets).toBe(1);
  app.tasks.acquire(acquireSchema.parse({ action: 'cancel', task_id: id }));
  await expect(app.gateway.downloadUrl(id)).rejects.toMatchObject({ code: 'INVALID_STATE' });
});

test('upstream expiry limits link issuance but the separate file cache still supports collection', async () => {
  const { app, source, cwa, start } = await setup();
  source.state.expires = Math.floor(Date.now() / 1000) + 300;
  const id = start({ save_to_cwa: false });
  await settle(app, id);
  const result = await app.gateway.downloadUrl(id);
  expect(result.expires_at).toBe(source.state.expires * 1000);
  expect(result.available_until).toBe(result.expires_at);
  app.store.mutate(id, (task) => {
    task.source_download!.expires_at = Date.now() - 1;
  });
  expect(app.tasks.get(id)).toMatchObject({ download: { status: 'expired' } });
  await expect(app.gateway.downloadUrl(id)).rejects.toMatchObject({ code: 'SOURCE_LINK_EXPIRED' });
  app.tasks.download(downloadBookSchema.parse({ action: 'collect', task_id: id }));
  expect((await settle(app, id)).state).toBe('ready');
  expect(cwa.state.uploads).toBe(1);
  expect(source.state.tickets).toBe(1);
});
