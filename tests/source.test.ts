import { afterEach, expect, test } from 'vitest';
import { readFile } from 'node:fs/promises';
import { join } from 'node:path';
import { Source } from '../src/source.js';
import { Http, textBody } from '../src/http.js';
import { createApp } from '../src/app.js';
import { acquireSchema } from '../src/tasks.js';
import { searchSchema, type Book } from '../src/domain.js';
import { body, epub, listen, mockCwa, testConfig, until } from './fixtures.js';

const cleanups: (() => Promise<unknown>)[] = [];
afterEach(async () => {
  for (const cleanup of cleanups.splice(0).reverse()) await cleanup();
});
async function sourceFixture(metadata: Record<string, unknown> = {}) {
  const state = {
    login: 0,
    profile: 0,
    search: 0,
    files: 0,
    expired: false,
    challenge: false,
    quota: false,
    rpc: false,
    seen: [] as Record<string, string | undefined>[],
  };
  const raw = {
    id: 25,
    hash: 'hash25',
    title: '合成测试书',
    author: '测试作者',
    language: 'chinese',
    extension: 'epub',
    filesize: epub.length,
    ...metadata,
  };
  const endpoint = await listen(async (req, res) => {
    const path = new URL(req.url!, 'http://localhost').pathname;
    res.setHeader('content-type', 'application/json');
    if (state.challenge) {
      res.setHeader('content-type', 'text/html');
      res.end('<html>cf-chl challenge-platform</html>');
      return;
    }
    if (path === '/eapi/user/login') {
      state.login++;
      const form = new URLSearchParams((await body(req)).toString());
      if (
        form.get('email') !== 'test@example.invalid' ||
        form.get('password') !== 'private-password' ||
        state.rpc
      ) {
        res.end(JSON.stringify({ success: 0, error: 'Authorization failed' }));
        return;
      }
      state.expired = false;
      res.end(JSON.stringify({ success: 1, user: { id: 7, remix_user_key: 'session-key' } }));
      return;
    }
    if (path === '/') {
      res.setHeader('content-type', 'text/html');
      res.end('<script>var loginDomain = "/rpc.php"</script>');
      return;
    }
    if (path === '/rpc.php') {
      const form = new URLSearchParams((await body(req)).toString());
      if (form.get('action') !== 'login' || form.get('email') !== 'test@example.invalid') {
        res.writeHead(403);
        res.end();
        return;
      }
      res.setHeader('set-cookie', ['remix_userid=7; Path=/', 'remix_userkey=session-key; Path=/']);
      res.end('{"response":{}}');
      return;
    }
    const headers = {
      id: req.headers['remix-userid'] as string | undefined,
      key: req.headers['remix-userkey'] as string | undefined,
      cookie: req.headers.cookie,
    };
    state.seen.push(headers);
    if (headers.id !== '7' || headers.key !== 'session-key' || state.expired) {
      res.end(JSON.stringify({ success: 0, error: 'Session expired' }));
      return;
    }
    if (path === '/eapi/user/profile') {
      state.profile++;
      res.end(JSON.stringify({ success: 1, user: { id: 7 } }));
      return;
    }
    if (path === '/eapi/book/search') {
      state.search++;
      res.end(
        JSON.stringify({ success: 1, books: [raw], pagination: { current: 1, total_pages: 1 } }),
      );
      return;
    }
    if (path === '/eapi/book/25/hash25') {
      res.end(JSON.stringify({ success: 1, book: raw }));
      return;
    }
    if (path === '/eapi/book/25/hash25/file') {
      if (state.quota) {
        res.end(JSON.stringify({ success: 0, error: 'Daily quota limit' }));
        return;
      }
      state.files++;
      res.end(JSON.stringify({ success: 1, file: { downloadLink: '/books/25' } }));
      return;
    }
    if (path === '/books/25') {
      res.setHeader('content-type', 'application/epub+zip');
      res.setHeader('content-length', epub.length);
      res.end(epub);
      return;
    }
    res.writeHead(404);
    res.end();
  });
  cleanups.push(endpoint.close);
  return { ...endpoint, state };
}
test.each([
  { name: 'null', identifier: null, isbn: undefined },
  {
    name: 'multiple identifiers',
    identifier: '9780140390693,0140390693,9780141439570,0141439572,9780141439518,0141439513',
    isbn: '9780140390693',
  },
])(
  '$name source metadata preserves search candidates and EPUB download',
  async ({ identifier, isbn }) => {
    const fixture = await sourceFixture({ isbn: null, identifier, filesize: null });
    const temp = await testConfig('http://localhost', {
      ZLIBRARY_BASE_URL: fixture.url,
      ZLIBRARY_USER_ID: '7',
      ZLIBRARY_USER_KEY: 'session-key',
    });
    cleanups.push(temp.cleanup);
    const source = new Source(temp.config);
    cleanups.push(() => source.close());
    const search = await source.search('合成测试书');
    expect(search.books).toHaveLength(1);
    const book = search.books[0]!;
    expect(book).toMatchObject({ id: '25', title: '合成测试书', language: 'zh' });
    expect(book.isbn).toBe(isbn);
    expect(book.size_bytes).toBeUndefined();
    const response = await source.open(book);
    expect(Buffer.from(await response.body.arrayBuffer())).toEqual(epub);
    expect(fixture.state.files).toBe(1);
  },
);
test('email login, private session cache, controlled reauthentication, download, quota and CAPTCHA', async () => {
  const fixture = await sourceFixture();
  const temp = await testConfig('http://localhost', {
    ZLIBRARY_BASE_URL: fixture.url,
    ZLIBRARY_EMAIL: 'test@example.invalid',
    ZLIBRARY_PASSWORD: 'private-password',
  });
  cleanups.push(temp.cleanup);
  const source = new Source(temp.config);
  cleanups.push(() => source.close());
  const search = await source.search('合成测试书');
  expect(search.books[0]!.language).toBe('zh');
  expect(fixture.state.login).toBe(1);
  const cache = await readFile(join(temp.config.stateDir, 'source-session.json'), 'utf8');
  expect(cache).not.toContain('private-password');
  fixture.state.expired = true;
  await source.search('合成测试书');
  expect(fixture.state.login).toBe(2);
  const r = await source.open(search.books[0]!);
  const chunks: Buffer[] = [];
  for await (const chunk of r.body) chunks.push(Buffer.from(chunk));
  expect(Buffer.concat(chunks)).toEqual(epub);
  fixture.state.quota = true;
  await expect(source.open(search.books[0]!)).rejects.toMatchObject({ code: 'QUOTA_EXCEEDED' });
  fixture.state.challenge = true;
  await expect(source.search('anything')).rejects.toMatchObject({ code: 'CAPTCHA_REQUIRED' });
});
test('same-origin RPC login fallback and existing remix credentials', async () => {
  const fixture = await sourceFixture();
  fixture.state.rpc = true;
  const temp = await testConfig('http://localhost', {
    ZLIBRARY_BASE_URL: fixture.url,
    ZLIBRARY_EMAIL: 'test@example.invalid',
    ZLIBRARY_PASSWORD: 'private-password',
  });
  cleanups.push(temp.cleanup);
  const source = new Source(temp.config);
  cleanups.push(() => source.close());
  expect((await source.search('合成测试书')).books).toHaveLength(1);
  const session = new Source({
    ...temp.config,
    sourceEmail: undefined,
    sourcePassword: undefined,
    sourceUserId: '7',
    sourceUserKey: 'session-key',
  });
  cleanups.push(() => session.close());
  expect((await session.search('合成测试书')).books).toHaveLength(1);
  expect(fixture.state.login).toBe(1);
  fixture.state.expired = true;
  await expect(session.search('合成测试书')).rejects.toMatchObject({ code: 'AUTH_EXPIRED' });
  expect(fixture.state.login).toBe(1);
});
test('concurrent cold requests share one login and concurrent expiry shares reauthentication', async () => {
  const fixture = await sourceFixture();
  const temp = await testConfig('http://localhost', {
    ZLIBRARY_BASE_URL: fixture.url,
    ZLIBRARY_EMAIL: 'test@example.invalid',
    ZLIBRARY_PASSWORD: 'private-password',
  });
  cleanups.push(temp.cleanup);
  const source = new Source(temp.config);
  cleanups.push(() => source.close());
  await Promise.all(Array.from({ length: 6 }, () => source.search('合成测试书')));
  expect(fixture.state.login).toBe(1);
  fixture.state.expired = true;
  await Promise.all(Array.from({ length: 6 }, () => source.search('合成测试书')));
  expect(fixture.state.login).toBe(2);
});
test('source acquisition verifies CWA after ingestion and skips source transfer for library reuse', async () => {
  const fixture = await sourceFixture(),
    cwa = await mockCwa();
  cleanups.push(cwa.close);
  const temp = await testConfig(cwa.url, {
    ZLIBRARY_BASE_URL: fixture.url,
    ZLIBRARY_USER_ID: '7',
    ZLIBRARY_USER_KEY: 'session-key',
  });
  cleanups.push(temp.cleanup);
  const app = await createApp(temp.config);
  cleanups.push(app.close);
  const result = await app.catalog.search(searchSchema.parse({ query: '合成测试书' }));
  expect(result.selected_tier).toBe('source_zh');
  const candidate = (result.candidates as (Book & { candidate_id: string })[])[0]!;
  const start = app.tasks.acquire(
    acquireSchema.parse({
      request_id: 'source1',
      candidate_id: candidate.candidate_id,
      required_format: 'epub',
    }),
  );
  await until(() =>
    ['ready', 'blocked', 'needs_review'].includes(app.store.get(start.id as string).state),
  );
  expect(app.store.get(start.id as string).state).toBe('ready');
  expect(fixture.state.files).toBe(1);
  expect(cwa.state.uploads).toBe(1);
  const reuse = app.tasks.acquire(
    acquireSchema.parse({
      request_id: 'source2',
      candidate_id: candidate.candidate_id,
      required_format: 'epub',
    }),
  );
  await until(() => app.store.get(reuse.id as string).state === 'ready');
  expect(fixture.state.files).toBe(1);
  expect(cwa.state.uploads).toBe(1);
});
test('cross-origin redirects strip all authentication and unknown origins are blocked', async () => {
  let captured: Record<string, unknown> = {};
  const destination = await listen((req, res) => {
    captured = req.headers;
    res.end('ok');
  });
  cleanups.push(destination.close);
  const origin = await listen((_req, res) => {
    res.writeHead(302, { location: destination.url + '/file' });
    res.end();
  });
  cleanups.push(origin.close);
  const http = new Http(origin.url, 'SOURCE', 2000);
  cleanups.push(() => http.close());
  await http.jar.setCookie('secret=cookie; Path=/', origin.url);
  const r = await http.send('/start', {
    headers: {
      authorization: 'secret',
      cookie: 'secret',
      'remix-userid': '7',
      'remix-userkey': 'key',
      'x-api-key': 'private',
    },
    allowedOrigins: [origin.url, destination.url],
  });
  expect(await textBody(r)).toBe('ok');
  for (const key of ['authorization', 'cookie', 'remix-userid', 'remix-userkey', 'x-api-key'])
    expect(captured[key]).toBeUndefined();
  await expect(http.send('/start')).rejects.toMatchObject({ code: 'ORIGIN_BLOCKED' });
});
