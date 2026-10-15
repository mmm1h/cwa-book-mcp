import { afterEach, expect, test, vi } from 'vitest';
import { Http } from '../src/http.js';

const dns = vi.hoisted(() => ({
  addresses: [] as { address: string; family: number }[],
  calls: 0,
}));
vi.mock('node:dns', () => ({
  lookup: (
    _hostname: string,
    _options: unknown,
    callback: (error: null, addresses: typeof dns.addresses) => void,
  ) => {
    dns.calls++;
    callback(null, dns.addresses);
  },
}));
const cleanups: (() => Promise<unknown>)[] = [];
afterEach(async () => {
  for (const cleanup of cleanups.splice(0)) await cleanup();
});
test('automatic origins reject DNS answers resolving to local or mixed public/private addresses before connecting', async () => {
  const http = new Http('https://source.example.invalid', 'SOURCE', 1000, true);
  const download = new Http('https://source.example.invalid', 'SOURCE', 1000);
  cleanups.push(
    () => http.close(),
    () => download.close(),
  );
  for (const addresses of [
    [{ address: '127.0.0.1', family: 4 }],
    [{ address: '169.254.169.254', family: 4 }],
    [{ address: '::ffff:172.17.0.1', family: 6 }],
    [
      { address: '8.8.8.8', family: 4 },
      { address: '192.168.1.1', family: 4 },
    ],
  ]) {
    dns.addresses = addresses;
    await expect(http.send('/eapi/info/domains')).rejects.toMatchObject({ code: 'ORIGIN_BLOCKED' });
    await expect(
      download.send('https://cdn.example.invalid/book', { publicOrigins: true }),
    ).rejects.toMatchObject({ code: 'ORIGIN_BLOCKED' });
  }
  expect(dns.calls).toBeGreaterThanOrEqual(8);
});
