import { afterEach, expect, test } from 'vitest';
import { readFile, writeFile, readdir } from 'node:fs/promises';
import { Readable } from 'node:stream';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';
import { Store } from '../src/store.js';
import { loadConfig } from '../src/config.js';
import { Files, validateFile } from '../src/files.js';
import { Tickets } from '../src/gateway.js';
import { Catalog } from '../src/catalog.js';
import { AppError, searchSchema } from '../src/domain.js';
import { book, epub, pdf, testConfig, storedZip } from './fixtures.js';

const cleanups: (() => Promise<unknown> | void)[] = [];
afterEach(async () => {
  for (const cleanup of cleanups.splice(0).reverse()) await cleanup();
});
test('exclusive state ownership, key integrity, identity isolation and transaction rollback', async () => {
  const temp = await testConfig('http://localhost:8083');
  cleanups.push(temp.cleanup);
  const store = new Store(temp.config.stateDir, 'one');
  expect(() => new Store(temp.config.stateDir, 'one')).toThrow('状态目录已有');
  store.db.transaction(() =>
    store.db.prepare('INSERT INTO meta VALUES (?,?)').run('test', 'value'),
  )();
  expect(() =>
    store.db.transaction(() => {
      store.db.prepare('DELETE FROM meta').run();
      throw Error('rollback');
    })(),
  ).toThrow('rollback');
  expect(store.db.prepare("SELECT value FROM meta WHERE key='test'").get()).toEqual({
    value: 'value',
  });
  store.close();
  expect(() => new Store(temp.config.stateDir, 'two')).toThrow('属于其他');
  await writeFile(join(temp.config.stateDir, 'ticket.key'), 'bad');
  expect(() => new Store(temp.config.stateDir, 'one')).toThrow('密钥损坏');
});
test('v1 state upgrades without losing tasks, idempotency or ticket keys; unknown versions fail closed', async () => {
  const temp = await testConfig('http://localhost');
  cleanups.push(temp.cleanup);
  const first = new Store(temp.config.stateDir, 'upgrade');
  const now = Date.now();
  const input = { request_id: 'legacy' };
  const task = first.create('legacy', input, () => ({
    id: randomUUID(),
    kind: 'acquire',
    state: 'queued',
    required_format: 'epub',
    book: book(),
    created_at: now,
    updated_at: now,
    next_run_at: now,
  }));
  const key = Buffer.from(first.key);
  first.db.prepare("UPDATE meta SET value='1' WHERE key='schema_version'").run();
  first.close();
  const second = new Store(temp.config.stateDir, 'upgrade');
  expect(second.existing('legacy', input)?.id).toBe(task.id);
  expect(second.key).toEqual(key);
  expect(second.db.prepare("SELECT value FROM meta WHERE key='schema_version'").get()).toEqual({
    value: '2',
  });
  second.db.prepare("UPDATE meta SET value='999' WHERE key='schema_version'").run();
  second.close();
  expect(() => new Store(temp.config.stateDir, 'upgrade')).toThrow('状态版本不兼容');
});

test('config rejects ambiguous credentials and remote URLs without stable file port', async () => {
  const env = {
    CWA_BASE_URL: 'http://localhost:8083',
    CWA_USERNAME: 'reader',
    CWA_PASSWORD: 'synthetic',
  };
  expect(loadConfig(env).writeEnabled).toBe(false);
  expect(loadConfig(env).stateDir).toBe(loadConfig({ ...env, CWA_PASSWORD: 'rotated' }).stateDir);
  expect(loadConfig({ ...env, INSTANCE_ID: 'other' }).stateDir).not.toBe(loadConfig(env).stateDir);
  expect(() => loadConfig({ ...env, CWA_PASSWORD_FILE: 'missing' })).toThrow('不可同时');
  expect(() => loadConfig({ ...env, PUBLIC_BASE_URL: 'https://example.invalid/books' })).toThrow(
    'FILE_PORT',
  );
  expect(() =>
    loadConfig({
      ...env,
      ZLIBRARY_EMAIL: 'example',
      ZLIBRARY_PASSWORD: 'p',
      ZLIBRARY_USER_ID: '1',
      ZLIBRARY_USER_KEY: 'key',
    }),
  ).toThrow('互斥');
  expect(() => loadConfig({ ...env, CWA_BASE_URL: 'http://user:password@localhost' })).toThrow(
    '不含凭据',
  );
});
test('file checks catch CRC corruption, HTML, incorrect length/hash; partials removed', async () => {
  const temp = await testConfig('http://localhost');
  cleanups.push(temp.cleanup);
  const files = new Files(join(temp.config.stateDir, 'files'), 1024 * 1024, 10 * 1024 * 1024);
  await files.init();
  await files.save('epub', Readable.from(epub), 'epub', epub.length);
  await files.save('pdf', Readable.from(pdf), 'pdf', pdf.length);
  expect((await validateFile(files.path('epub'), 'epub', 1024 * 1024)).size_bytes).toBe(
    epub.length,
  );
  const corrupt = Buffer.from(epub);
  const at = corrupt.indexOf(Buffer.from('application/epub+zip'));
  corrupt[at] = 0;
  await expect(files.save('corrupt', Readable.from(corrupt), 'epub')).rejects.toThrow();
  await expect(files.save('bad', Readable.from('<html>login</html>'), 'pdf')).rejects.toThrow(
    'PDF',
  );
  await expect(files.save('short', Readable.from(epub), 'epub', epub.length + 10)).rejects.toThrow(
    '长度',
  );
  await expect(
    files.save('hash', Readable.from(epub), 'epub', epub.length, '0'.repeat(64)),
  ).rejects.toThrow('SHA-256');
  expect((await readdir(files.dir)).filter((f) => f.endsWith('.part'))).toEqual([]);
  expect(await readFile(files.path('epub'))).toEqual(epub);
});
test('tickets bind expiry and signature', () => {
  const tickets = new Tickets(Buffer.alloc(32, 1));
  const value = {
    purpose: 'upload' as const,
    task_id: randomUUID(),
    format: 'epub' as const,
    expires: Math.floor(Date.now() / 1000) + 100,
  };
  const token = tickets.sign(value);
  expect(tickets.verify(token)).toEqual(value);
  expect(() => tickets.verify(tickets.sign({ ...value, expires: 1 }))).toThrow('过期');
  expect(() => new Tickets(Buffer.alloc(32, 2)).verify(token)).toThrow('无效');
});
test('a CRC-valid ZIP with a fake or malformed EPUB package is rejected', async () => {
  const temp = await testConfig('http://localhost');
  cleanups.push(temp.cleanup);
  const files = new Files(join(temp.config.stateDir, 'files'), 1024 * 1024, 10 * 1024 * 1024);
  await files.init();
  for (const opf of [
    '<not-package/>',
    '<package xmlns="http://www.idpf.org/2007/opf"><manifest><item id="one"/></manifest><spine><itemref idref="missing"/></spine></package>',
  ]) {
    const data = storedZip({
      mimetype: 'application/epub+zip',
      'META-INF/container.xml':
        '<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="book.opf"/></rootfiles></container>',
      'book.opf': opf,
    });
    await expect(files.save('fake', Readable.from(data), 'epub')).rejects.toMatchObject({
      code: 'FILE_INVALID',
    });
  }
});
test('Chinese tier priority, bounded pagination, cursor binding and failures never fall through', async () => {
  const temp = await testConfig('http://localhost');
  cleanups.push(temp.cleanup);
  const store = new Store(temp.config.stateDir, 'test');
  cleanups.push(() => store.close());
  const en = book({ language: 'en' }),
    zh = book();
  let sourceCalls = 0,
    cwaCalls = 0;
  const cwa = {
    search: async (_q: string, cursor?: string) => {
      cwaCalls++;
      return cursor === '3'
        ? { books: [zh] }
        : { books: [en], next: String(Number(cursor || '0') + 1) };
    },
  };
  const source = {
    search: async () => {
      sourceCalls++;
      return { books: [book({ origin: 'zlibrary' })] };
    },
  };
  const catalog = new Catalog(store, cwa, source);
  const input = searchSchema.parse({ query: '合成测试书' });
  const first = await catalog.search(input);
  expect(first.partial).toBe(true);
  expect(first.candidates).toEqual([]);
  expect(sourceCalls).toBe(0);
  const second = await catalog.search({ ...input, cursor: first.next_cursor as string });
  expect(second.selected_tier).toBe('cwa_zh');
  expect(sourceCalls).toBe(0);
  expect(cwaCalls).toBe(4);
  await expect(
    catalog.search({ ...input, query: 'other', cursor: first.next_cursor as string }),
  ).rejects.toThrow('不属于');
  const failed = new Catalog(
    store,
    {
      search: async () => {
        throw new AppError('AUTH_EXPIRED', 'no');
      },
    },
    source,
  );
  const result = await failed.search(input);
  expect(result.partial).toBe(true);
  expect(result.warnings).toEqual(['AUTH_EXPIRED']);
  expect(sourceCalls).toBe(0);
  const sourceFirst = new Catalog(store, { search: async () => ({ books: [en] }) }, source);
  expect((await sourceFirst.search(input)).selected_tier).toBe('source_zh');
});
