import { Agent, request, type Dispatcher } from 'undici';
import { CookieJar } from 'tough-cookie';
import { lookup } from 'node:dns';
import { BlockList, isIP, type LookupFunction } from 'node:net';
import { fail, AppError } from './domain.js';
import { VERSION } from './version.js';

export type Response = Dispatcher.ResponseData & { url: string };
const blocked = new BlockList();
for (const [address, prefix] of [
  ['0.0.0.0', 8],
  ['10.0.0.0', 8],
  ['100.64.0.0', 10],
  ['127.0.0.0', 8],
  ['169.254.0.0', 16],
  ['172.16.0.0', 12],
  ['192.0.0.0', 24],
  ['192.0.2.0', 24],
  ['192.168.0.0', 16],
  ['198.18.0.0', 15],
  ['198.51.100.0', 24],
  ['203.0.113.0', 24],
  ['224.0.0.0', 4],
  ['240.0.0.0', 4],
] as const)
  blocked.addSubnet(address, prefix, 'ipv4');
const globalV6 = new BlockList();
globalV6.addSubnet('2000::', 3, 'ipv6');
const fakeDns = new BlockList();
fakeDns.addSubnet('198.18.0.0', 15, 'ipv4');
for (const [address, prefix] of [
  ['2001::', 32],
  ['2001:2::', 48],
  ['2001:db8::', 32],
  ['2002::', 16],
] as const)
  blocked.addSubnet(address, prefix, 'ipv6');
export function publicAddress(address: string): boolean {
  if (isIP(address) === 4) return !blocked.check(address, 'ipv4');
  return isIP(address) === 6 && globalV6.check(address, 'ipv6') && !blocked.check(address, 'ipv6');
}
export function publicHttps(url: URL): boolean {
  const host = url.hostname.replace(/^\[|\]$/g, '');
  return (
    url.protocol === 'https:' &&
    !url.username &&
    !url.password &&
    !url.port &&
    (isIP(host)
      ? publicAddress(host)
      : host.includes('.') && !/(^|\.)(localhost|local|internal|home|lan)\.?$/i.test(host))
  );
}
const publicLookup: LookupFunction = (hostname, options, callback) => {
  lookup(hostname, { ...options, all: true }, (error, addresses) => {
    if (error) return callback(error, '');
    // TUN proxies use this reserved range as synthetic DNS answers. Permit it
    // only through hostname lookup; literal reserved/private URLs stay blocked,
    // and HTTPS still verifies the original hostname's certificate.
    if (
      !addresses.length ||
      addresses.some(
        (a) =>
          !publicAddress(a.address) && !fakeDns.check(a.address, a.family === 6 ? 'ipv6' : 'ipv4'),
      )
    )
      return callback(
        Object.assign(new Error('Public destination required'), { code: 'EACCES' }),
        '',
      );
    if (options.all) callback(null, addresses);
    else callback(null, addresses[0]!.address, addresses[0]!.family);
  });
};
export class Http {
  readonly jar = new CookieJar();
  private agent = new Agent({ connections: 4, pipelining: 1, connect: { timeout: 15000 } });
  private publicAgent = new Agent({
    connections: 4,
    pipelining: 1,
    connect: { timeout: 10000, lookup: publicLookup },
  });
  constructor(
    readonly base: string,
    readonly label: string,
    private timeoutMs: number,
    private publicOnly = false,
  ) {}
  url(path: string): string {
    return path.startsWith('/') ? this.base + path : new URL(path, this.base + '/').href;
  }
  async send(
    path: string,
    options: {
      method?: 'GET' | 'POST';
      body?: Dispatcher.DispatchOptions['body'];
      headers?: Record<string, string>;
      signal?: AbortSignal;
      allowedOrigins?: string[];
      timeoutMs?: number;
      cookies?: boolean;
      publicOrigins?: boolean;
      anonymous?: boolean;
    } = {},
  ): Promise<Response> {
    let url = this.url(path);
    let method = options.method || 'GET';
    let body = options.body;
    const origin = new URL(this.base).origin;
    const allowed = new Set(options.allowedOrigins || [origin]);
    let crossed = false;
    const timeout = AbortSignal.timeout(options.timeoutMs ?? this.timeoutMs);
    const signal = options.signal ? AbortSignal.any([options.signal, timeout]) : timeout;
    try {
      for (let redirect = 0; redirect <= 5; redirect++) {
        const u = new URL(url);
        const unlisted = !allowed.has(u.origin);
        if (
          !['http:', 'https:'].includes(u.protocol) ||
          u.username ||
          u.password ||
          (unlisted && !(options.publicOrigins && publicHttps(u))) ||
          (this.publicOnly && !publicHttps(u))
        )
          fail('ORIGIN_BLOCKED', this.label + ' 请求目标未通过来源检查');
        if (u.origin !== origin) crossed = true;
        const headers: Record<string, string> = {
          'user-agent': 'cwa-book-mcp/' + VERSION,
          'accept-encoding': 'identity',
          ...options.headers,
        };
        if (crossed || options.anonymous) {
          for (const name of Object.keys(headers))
            if (/^(authorization|cookie|remix[-_]|x-)/i.test(name)) delete headers[name];
        } else if (options.cookies !== false) {
          const cookie = await this.jar.getCookieString(url);
          if (cookie) headers.cookie = cookie;
        }
        if (method === 'GET') {
          delete headers['content-type'];
          delete headers['content-length'];
        }
        const response = await request(url, {
          method,
          body,
          headers,
          signal,
          dispatcher: this.publicOnly || unlisted ? this.publicAgent : this.agent,
          headersTimeout: 30000,
          bodyTimeout: 30000,
        });
        // Undici emits an AbortError when an unread body is explicitly destroyed.
        // Consumers still observe stream errors through iteration/pipeline.
        response.body.on('error', () => {});
        const cookies = response.headers['set-cookie'];
        if (!crossed && !options.anonymous && options.cookies !== false)
          for (const cookie of Array.isArray(cookies) ? cookies : cookies ? [cookies] : [])
            await this.jar.setCookie(cookie, url);
        if ([301, 302, 303, 307, 308].includes(response.statusCode)) {
          const location = response.headers.location;
          response.body.destroy();
          if (!location || Array.isArray(location))
            fail('UPSTREAM_INVALID', this.label + ' 重定向无效');
          if (method === 'POST' && [307, 308].includes(response.statusCode))
            fail('UPLOAD_OUTCOME_UNKNOWN', '拒绝重放重定向后的提交');
          if (method === 'POST') {
            method = 'GET';
            body = undefined;
          }
          const next = new URL(location, url);
          if (u.protocol === 'https:' && next.protocol !== 'https:')
            fail('ORIGIN_BLOCKED', '拒绝 HTTPS 降级重定向');
          url = next.href;
          continue;
        }
        return Object.assign(response, { url });
      }
      return fail('UPSTREAM_INVALID', this.label + ' 重定向次数过多');
    } catch (error) {
      if (error instanceof AppError) throw error;
      if (error && typeof error === 'object' && 'code' in error && error.code === 'EACCES')
        fail('ORIGIN_BLOCKED', this.label + ' 拒绝访问非公网目标');
      throw new AppError(this.label + '_UNAVAILABLE', this.label + ' 请求失败或超时', true);
    }
  }
  async close(): Promise<void> {
    await Promise.all([this.agent.destroy(), this.publicAgent.destroy()]);
  }
}
export function status(response: Response, label: string): void {
  const n = response.statusCode;
  if (n >= 200 && n < 300) return;
  response.body.destroy();
  if (n === 401 || n === 403) fail('AUTH_EXPIRED', label + ' 认证或权限失效');
  if (n === 429) fail('QUOTA_EXCEEDED', label + ' 配额或频率限制', true);
  if (n === 404) fail('BOOK_NOT_FOUND', label + ' 记录或接口不存在');
  if (n === 413) fail('FILE_TOO_LARGE', label + ' 拒绝文件大小');
  fail(label + '_UNAVAILABLE', label + ' 请求未成功', true);
}
export async function textBody(response: Response, max = 4 * 1024 * 1024): Promise<string> {
  const chunks: Buffer[] = [];
  let bytes = 0;
  try {
    for await (const data of response.body) {
      const chunk = Buffer.from(data);
      bytes += chunk.length;
      if (bytes > max) fail('METADATA_TOO_LARGE', '上游元数据超过上限');
      chunks.push(chunk);
    }
    return Buffer.concat(chunks).toString('utf8');
  } finally {
    response.body.destroy();
  }
}
export async function jsonBody(response: Response): Promise<unknown> {
  const data = await textBody(response);
  try {
    return JSON.parse(data) as unknown;
  } catch {
    return fail('UPSTREAM_INVALID', '上游未返回有效 JSON');
  }
}
