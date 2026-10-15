import { afterEach, beforeEach, expect, test, vi } from 'vitest';
import { readFile, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import type { Dispatcher } from 'undici';
import { MockAgent } from 'undici';
import { Source } from '../src/source.js';
import { digest } from '../src/domain.js';
import { domainPolicyUrl, parseDomainPolicy } from '../src/source-domains.js';
import { publicAddress, Http } from '../src/http.js';
import { epub, testConfig } from './fixtures.js';

const network = vi.hoisted(() => ({
  dispatcher: undefined as Dispatcher | undefined,
  requests: [] as { url: string; headers: Record<string, string>; body?: unknown }[],
}));
vi.mock('undici', async (importOriginal) => {
  const actual = await importOriginal<typeof import('undici')>();
  return {
    ...actual,
    request: (url: string, options: Dispatcher.RequestOptions) => {
      network.requests.push({
        url,
        headers: options.headers as Record<string, string>,
        body: options.body,
      });
      return actual.request(url, { ...options, dispatcher: network.dispatcher });
    },
  };
});
let agent: MockAgent;
const cleanups: (() => Promise<unknown>)[] = [];
beforeEach(() => {
  agent = new MockAgent();
  agent.disableNetConnect();
  network.dispatcher = agent;
  network.requests = [];
});
afterEach(async () => {
  for (const cleanup of cleanups.splice(0).reverse()) await cleanup();
  await agent.close();
});
const primary = 'https://primary.example.invalid';
const secondary = 'https://secondary.example.invalid';
const raw = {
  id: 25,
  hash: 'hash25',
  title: '合成测试书',
  author: '测试作者',
  language: 'chinese',
  extension: 'epub',
};
function reply(url: string, path: string, data: unknown, code = 200, method = 'GET') {
  return agent
    .get(url)
    .intercept({ path, method })
    .reply(code, typeof data === 'string' || Buffer.isBuffer(data) ? data : JSON.stringify(data), {
      headers: { 'content-type': 'application/json' },
    });
}
function policy(origins = [primary], persistent = true) {
  const result = reply(
    new URL(domainPolicyUrl).origin,
    new URL(domainPolicyUrl).pathname,
    'var allowedDomainSuffixes = []string{\n' +
      origins.map((o) => '"' + new URL(o).hostname + '",').join('\n') +
      '\n}',
  );
  return persistent ? result.persist() : result;
}
function healthy(url: string, advertised = [url], persistent = true) {
  const result = reply(url, '/eapi/info/domains', {
    success: 1,
    domains: advertised.map((o) => ({
      domain: new URL(o).hostname,
      contentAvailable: true,
      isRedirector: false,
    })),
  });
  if (persistent) result.persist();
}
function search(url: string) {
  return reply(
    url,
    '/eapi/book/search',
    { success: 1, books: [raw], pagination: { total_pages: 1 } },
    200,
    'POST',
  );
}
async function setup(env: NodeJS.ProcessEnv = {}) {
  const temp = await testConfig('http://localhost', {
    ZLIBRARY_USER_ID: '7',
    ZLIBRARY_USER_KEY: 'session-key',
    ...env,
  });
  cleanups.push(temp.cleanup);
  const source = new Source(temp.config);
  cleanups.push(() => source.close());
  return { ...temp, source };
}
function credentialRequests(url: string) {
  return network.requests.filter(
    (r) =>
      new URL(r.url).origin === url &&
      (r.headers.cookie || r.headers['remix-userkey'] || String(r.body).includes('password=')),
  );
}

test('account-only configuration discovers maintained domains without credentials and shares concurrent login', async () => {
  policy();
  healthy(primary);
  search(primary).persist();
  reply(
    primary,
    '/eapi/user/login',
    { success: 1, user: { id: 7, remix_user_key: 'session-key' } },
    200,
    'POST',
  ).persist();
  reply(primary, '/eapi/user/profile', { success: 1 }).persist();
  const { source, config } = await setup({
    ZLIBRARY_USER_ID: '',
    ZLIBRARY_USER_KEY: '',
    ZLIBRARY_EMAIL: 'test@example.invalid',
    ZLIBRARY_PASSWORD: 'private-password',
  });
  expect(source.configured).toBe(true);
  expect(
    (await Promise.all(Array.from({ length: 6 }, () => source.search('合成测试书')))).every(
      (p) => p.books.length === 1,
    ),
  ).toBe(true);
  expect(network.requests.filter((r) => r.url.endsWith('/eapi/user/login'))).toHaveLength(1);
  expect(network.requests.filter((r) => r.url === domainPolicyUrl)).toHaveLength(1);
  for (const r of network.requests.filter(
    (r) => r.url === domainPolicyUrl || r.url.endsWith('/eapi/info/domains'),
  )) {
    expect(r.headers.cookie).toBeUndefined();
    expect(r.headers['remix-userkey']).toBeUndefined();
    expect(r.body).toBeUndefined();
  }
  expect(await readFile(join(config.stateDir, 'source-session.json'), 'utf8')).not.toContain(
    'private-password',
  );
  expect(await readFile(join(config.stateDir, 'source-domains.json'), 'utf8')).not.toMatch(
    /private-password|session-key|test@/,
  );
});

test('a broken selected API switches to an advertised healthy domain and keeps the account session', async () => {
  policy();
  healthy(primary, [primary, secondary]);
  healthy(secondary);
  search(primary);
  reply(primary, '/eapi/book/search', 'offline', 503, 'POST').persist();
  search(secondary).persist();
  const { source } = await setup();
  expect((await source.search('合成测试书')).books).toHaveLength(1);
  expect((await source.search('合成测试书')).books).toHaveLength(1);
  expect(credentialRequests(primary)).toHaveLength(2);
  expect(credentialRequests(secondary)).toHaveLength(1);
  expect(network.requests.some((r) => r.url.endsWith('/eapi/user/login'))).toBe(false);
});

test.each([
  [{ success: 0, error: 'Session expired' }, 'AUTH_EXPIRED'],
  [{ success: 0, error: 'Daily quota limit' }, 'QUOTA_EXCEEDED'],
  [{ success: 0, error: 'captcha required' }, 'CAPTCHA_REQUIRED'],
  [{ success: 0, error: 'unsupported request' }, 'SOURCE_REJECTED'],
])('business error %j does not rotate authentication through other domains', async (data, code) => {
  policy();
  healthy(primary, [secondary]);
  healthy(secondary);
  reply(primary, '/eapi/book/search', data, 200, 'POST').persist();
  const { source } = await setup();
  await expect(source.search('合成测试书')).rejects.toMatchObject({ code });
  expect(credentialRequests(primary)).toHaveLength(1);
  expect(credentialRequests(secondary)).toHaveLength(0);
});

test('bad password stays on the selected origin, including the RPC fallback', async () => {
  policy();
  healthy(primary, [secondary]);
  healthy(secondary);
  reply(primary, '/eapi/user/login', { success: 0, error: 'Invalid password' }, 200, 'POST');
  reply(primary, '/', '<html>login</html>');
  reply(primary, '/rpc.php', '{}', 200, 'POST');
  const { source } = await setup({
    ZLIBRARY_USER_ID: '',
    ZLIBRARY_USER_KEY: '',
    ZLIBRARY_EMAIL: 'test@example.invalid',
    ZLIBRARY_PASSWORD: 'wrong-password',
  });
  await expect(source.search('合成测试书')).rejects.toMatchObject({ code: 'AUTH_EXPIRED' });
  expect(credentialRequests(secondary)).toHaveLength(0);
  expect(network.requests.filter((r) => String(r.body).includes('password='))).toHaveLength(2);
});

test('domain cache survives restart, rechecks health, and tolerates a maintenance outage after expiry', async () => {
  policy([primary], false);
  healthy(primary);
  search(primary).persist();
  reply(
    new URL(domainPolicyUrl).origin,
    new URL(domainPolicyUrl).pathname,
    'offline',
    503,
  ).persist();
  const { source, config } = await setup();
  await source.search('合成测试书');
  await source.close();
  const cached = JSON.parse(await readFile(join(config.stateDir, 'source-domains.json'), 'utf8'));
  const restart = new Source(config);
  cleanups.push(() => restart.close());
  const before = network.requests.length;
  await restart.search('合成测试书');
  expect(network.requests.slice(before).some((r) => r.url === domainPolicyUrl)).toBe(false);
  expect(network.requests.slice(before).some((r) => r.url.endsWith('/eapi/info/domains'))).toBe(
    true,
  );
  await restart.close();
  await writeFile(
    join(config.stateDir, 'source-domains.json'),
    JSON.stringify({ ...cached, updatedAt: Date.now() - 2 * 86400000 }),
  );
  const offline = new Source(config);
  cleanups.push(() => offline.close());
  expect((await offline.search('合成测试书')).books).toHaveLength(1);
});

test('a refreshed maintenance list replaces stale cached authentication targets', async () => {
  policy([secondary]);
  healthy(secondary);
  search(secondary).persist();
  const { source, config } = await setup();
  await writeFile(
    join(config.stateDir, 'source-domains.json'),
    JSON.stringify({
      version: 1,
      updatedAt: Date.now() - 2 * 86400000,
      roots: [primary],
      advertised: [],
      preferred: primary,
    }),
  );
  await source.search('合成测试书');
  expect(network.requests.some((r) => new URL(r.url).origin === primary)).toBe(false);
  expect(credentialRequests(secondary)).toHaveLength(1);
});

test('failed cached domains trigger maintenance refresh before the daily TTL', async () => {
  policy([secondary]);
  healthy(secondary);
  search(secondary).persist();
  const { source, config } = await setup();
  await writeFile(
    join(config.stateDir, 'source-domains.json'),
    JSON.stringify({
      version: 1,
      updatedAt: Date.now(),
      roots: [primary],
      advertised: [],
      preferred: primary,
    }),
  );
  expect((await source.search('合成测试书')).books).toHaveLength(1);
  expect(network.requests.filter((r) => r.url === domainPolicyUrl)).toHaveLength(1);
  expect(credentialRequests(primary)).toHaveLength(0);
  expect(credentialRequests(secondary)).toHaveLength(1);
});

test('automatic mode reuses a legacy session only when its previous origin is trusted', async () => {
  policy();
  healthy(primary);
  search(primary).persist();
  const { source, config } = await setup({
    ZLIBRARY_USER_ID: '',
    ZLIBRARY_USER_KEY: '',
    ZLIBRARY_EMAIL: 'test@example.invalid',
    ZLIBRARY_PASSWORD: 'private-password',
  });
  await writeFile(
    join(config.stateDir, 'source-session.json'),
    JSON.stringify({
      fingerprint: digest({ url: primary, email: 'test@example.invalid' }),
      id: '7',
      key: 'session-key',
    }),
  );
  expect((await source.search('合成测试书')).books).toHaveLength(1);
  expect(network.requests.some((r) => r.url.endsWith('/eapi/user/login'))).toBe(false);
});

test('unavailable maintenance falls back to bundled roots; all invalid probes fail without authentication', async () => {
  reply(
    new URL(domainPolicyUrl).origin,
    new URL(domainPolicyUrl).pathname,
    '<html>offline</html>',
  ).persist();
  healthy('https://z-lib.gd', undefined, false);
  search('https://z-lib.gd').persist();
  const first = await setup();
  expect((await first.source.search('合成测试书')).books).toHaveLength(1);
  const second = await setup();
  await expect(second.source.search('合成测试书')).rejects.toMatchObject({
    code: 'SOURCE_UNAVAILABLE',
    retryable: true,
  });
  expect(network.requests.filter((r) => r.url.endsWith('/eapi/user/login'))).toHaveLength(0);
});

test('cancelling one concurrent discovery caller does not cancel the other caller', async () => {
  policy().delay(20);
  healthy(primary);
  search(primary).persist();
  const { source } = await setup();
  const abort = new AbortController();
  const cancelled = source.search('合成测试书', undefined, abort.signal);
  const other = source.search('合成测试书');
  abort.abort();
  await expect(cancelled).rejects.toMatchObject({ code: 'CANCELLED' });
  expect((await other).books).toHaveLength(1);
  expect(network.requests.filter((r) => r.url === domainPolicyUrl)).toHaveLength(1);
});

test('authenticated file links and rotating public CDN redirects need no configured origins and receive no credentials', async () => {
  policy();
  healthy(primary);
  search(primary).persist();
  reply(primary, '/eapi/book/25/hash25', { success: 1, book: raw });
  reply(primary, '/eapi/book/25/hash25/file', {
    success: 1,
    file: { downloadLink: 'https://cdn1.example.invalid/start' },
  });
  agent
    .get('https://cdn1.example.invalid')
    .intercept({ path: '/start' })
    .reply(302, '', { headers: { location: 'https://cdn2.example.invalid/book' } });
  agent
    .get('https://cdn2.example.invalid')
    .intercept({ path: '/book' })
    .reply(200, epub, { headers: { 'content-type': 'application/epub+zip' } });
  const { source } = await setup();
  const book = (await source.search('合成测试书')).books[0]!;
  expect(Buffer.from(await (await source.open(book)).body.arrayBuffer())).toEqual(epub);
  for (const url of ['https://cdn1.example.invalid', 'https://cdn2.example.invalid'])
    expect(credentialRequests(url)).toHaveLength(0);
});

test('a lost file-ticket response is not replayed on another mirror', async () => {
  policy();
  healthy(primary, [secondary]);
  healthy(secondary);
  search(primary).persist();
  reply(primary, '/eapi/book/25/hash25', { success: 1, book: raw });
  reply(primary, '/eapi/book/25/hash25/file', 'offline', 503);
  const { source } = await setup();
  const book = (await source.search('合成测试书')).books[0]!;
  await expect(source.open(book)).rejects.toMatchObject({ code: 'SOURCE_UNAVAILABLE' });
  expect(network.requests.filter((r) => r.url.endsWith('/file'))).toHaveLength(1);
  expect(credentialRequests(secondary)).toHaveLength(0);
});

test('public downloads reject private addresses, credentials, non-HTTPS and downgrade redirects', async () => {
  const http = new Http(primary, 'SOURCE', 1000);
  cleanups.push(() => http.close());
  for (const url of [
    'http://cdn.example.invalid/book',
    'https://127.0.0.1/book',
    'https://198.18.0.1/book',
    'https://[::1]/book',
    'https://localhost/book',
    'https://router.local/book',
    'https://user:secret@cdn.example.invalid/book',
  ])
    await expect(http.send(url, { publicOrigins: true })).rejects.toMatchObject({
      code: 'ORIGIN_BLOCKED',
    });
  agent
    .get(primary)
    .intercept({ path: '/start' })
    .reply(302, '', { headers: { location: 'http://cdn.example.invalid/file' } });
  await expect(http.send('/start', { publicOrigins: true })).rejects.toMatchObject({
    code: 'ORIGIN_BLOCKED',
  });
  expect(network.requests).toHaveLength(1);
  for (const address of [
    '127.0.0.1',
    '10.1.2.3',
    '169.254.169.254',
    '172.17.0.1',
    '192.168.1.1',
    '100.100.100.200',
    '::1',
    '::ffff:127.0.0.1',
    'fc00::1',
    'fe80::1',
  ])
    expect(publicAddress(address)).toBe(false);
  for (const address of ['8.8.8.8', '2606:4700:4700::1111'])
    expect(publicAddress(address)).toBe(true);
  expect(() => parseDomainPolicy('<a href="https://phishing.example.invalid">login</a>')).toThrow();
  expect(() => parseDomainPolicy('var allowedDomainSuffixes = []string{"127.0.0.1",}')).toThrow();
});
