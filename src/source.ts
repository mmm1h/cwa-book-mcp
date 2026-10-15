import { readFile, rename, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { z } from 'zod';
import {
  AppError,
  bookSchema,
  digest,
  fail,
  language,
  sameBook,
  type Book,
  type Page,
} from './domain.js';
import { Http, jsonBody, status, textBody, type Response } from './http.js';
import { sourceConfigured, type Config } from './config.js';
import { SourceDomains } from './source-domains.js';

const scalar = z.union([z.string(), z.number()]).transform(String);
const credentials = z.object({ id: scalar, key: z.string().min(1).max(4096) });
type Credentials = z.infer<typeof credentials>;
const envelope = z
  .object({ success: z.union([z.number(), z.boolean()]), error: z.string().optional() })
  .passthrough();
const rawBook = z.object({
  id: scalar,
  hash: z.string().min(1),
  title: z.string(),
  author: z.string().default(''),
  language: z.string().default('unknown'),
  extension: z.string(),
  isbn: scalar.nullish(),
  identifier: scalar.nullish(),
  filesize: z.union([z.string(), z.number()]).nullish(),
});
function mapBook(raw: z.infer<typeof rawBook>): Book {
  const size = Number(raw.filesize);
  const isbn = [raw.isbn, raw.identifier]
    .flatMap((value) => (value || '').split(/[,;，\r\n]/))
    .map((value) => value.trim())
    .find((value) => value.length > 0 && value.length <= 64);
  return bookSchema.parse({
    origin: 'zlibrary',
    id: raw.id,
    hash: raw.hash,
    title: raw.title,
    authors: raw.author ? raw.author.split(/;\s*/).slice(0, 16) : [],
    language: language(raw.language),
    formats: [raw.extension.toLowerCase()],
    isbn,
    size_bytes: Number.isSafeInteger(size) && size > 0 ? size : undefined,
  });
}
export class Source {
  private readonly domains: SourceDomains;
  private session?: Credentials;
  private authenticating?: Promise<void>;
  private loaded = false;
  constructor(readonly config: Config) {
    this.domains = new SourceDomains(config);
    if (config.sourceUserId && config.sourceUserKey)
      this.session = credentials.parse({ id: config.sourceUserId, key: config.sourceUserKey });
  }
  get configured(): boolean {
    return sourceConfigured(this.config);
  }
  private fingerprint(): string {
    return digest({ url: this.config.sourceUrl || 'auto', email: this.config.sourceEmail });
  }
  private async raw(
    http: Http,
    path: string,
    data?: URLSearchParams,
    signal?: AbortSignal,
  ): Promise<Record<string, unknown>> {
    const headers: Record<string, string> = { accept: 'application/json' };
    if (data) headers['content-type'] = 'application/x-www-form-urlencoded';
    if (this.session) {
      headers['remix-userid'] = this.session.id;
      headers['remix-userkey'] = this.session.key;
      headers.cookie =
        'remix_userid=' +
        encodeURIComponent(this.session.id) +
        '; remix_userkey=' +
        encodeURIComponent(this.session.key);
    }
    const response = await http.send(path, {
      method: data ? 'POST' : 'GET',
      headers,
      body: data?.toString(),
      signal,
      cookies: false,
    });
    const ct = String(response.headers['content-type'] || '');
    if (ct.includes('text/html')) {
      const text = await textBody(response);
      if (/captcha|cf-chl|challenge-platform/i.test(text))
        fail('CAPTCHA_REQUIRED', '书源需要人工验证');
      if (this.config.sourceUrl) fail('AUTH_EXPIRED', '书源返回登录页或非 API 响应');
      fail('SOURCE_UNAVAILABLE', '书源入口未返回 API 响应', true);
    }
    status(response, 'SOURCE');
    const parsed = envelope.safeParse(await jsonBody(response));
    if (!parsed.success) fail('UPSTREAM_INVALID', '书源 API 响应不兼容');
    const result = parsed.data;
    if (result.success !== 1 && result.success !== true) {
      const message = result.error || '';
      if (/captcha|challenge|bot protection/i.test(message))
        fail('CAPTCHA_REQUIRED', '书源需要人工验证');
      if (/limit|quota/i.test(message)) fail('QUOTA_EXCEEDED', '书源额度不足', true);
      if (/authoriz|authentic|login|session|password|credentials/i.test(message))
        fail('AUTH_EXPIRED', '书源认证失效');
      fail('SOURCE_REJECTED', '书源拒绝请求', true);
    }
    return result;
  }
  private async login(http: Http, signal?: AbortSignal): Promise<void> {
    if (!this.config.sourceEmail || !this.config.sourcePassword)
      fail('AUTH_REQUIRED', '请配置书源邮箱/密码或已有会话');
    this.session = undefined;
    try {
      const result = await this.raw(
        http,
        '/eapi/user/login',
        new URLSearchParams({
          email: this.config.sourceEmail,
          password: this.config.sourcePassword,
        }),
        signal,
      );
      const user = z
        .object({
          id: scalar,
          remix_userkey: z.string().optional(),
          remix_user_key: z.string().optional(),
        })
        .parse(result.user);
      this.session = credentials.parse({
        id: user.id,
        key: user.remix_userkey || user.remix_user_key,
      });
    } catch (error) {
      if (!(error instanceof AppError) || error.code !== 'AUTH_EXPIRED') throw error;
      // The compatibility login stays on the selected, verified origin.
      const page = await http.send('/', { signal });
      status(page, 'SOURCE');
      const html = await textBody(page);
      if (/cf-chl|challenge-platform|captcha/i.test(html))
        fail('CAPTCHA_REQUIRED', '书源登录需要人工验证');
      const hint = /loginDomain\s*[:=]\s*["']([^"']+)["']/.exec(html)?.[1];
      const rpc = hint ? new URL(hint, http.base + '/') : new URL(http.url('/rpc.php'));
      if (rpc.origin !== new URL(http.base).origin)
        fail('ORIGIN_BLOCKED', '书源登录入口跨来源，请使用同源入口');
      if (!rpc.pathname.endsWith('.php'))
        rpc.pathname = rpc.pathname.replace(/\/$/, '') + '/rpc.php';
      const response = await http.send(rpc.href, {
        method: 'POST',
        signal,
        headers: { 'content-type': 'application/x-www-form-urlencoded' },
        body: new URLSearchParams({
          isModal: 'true',
          email: this.config.sourceEmail,
          password: this.config.sourcePassword,
          site_mode: 'books',
          action: 'login',
          isSingleLogin: '1',
          redirectUrl: '',
          gg_json_mode: '1',
        }).toString(),
      });
      status(response, 'SOURCE');
      const body = await textBody(response);
      if (/captcha|challenge-platform/i.test(body))
        fail('CAPTCHA_REQUIRED', '书源登录需要人工验证');
      const jar = await http.jar.getCookies(http.base + '/');
      const mapped = Object.fromEntries(jar.map((c) => [c.key, c.value]));
      const parsed = credentials.safeParse({ id: mapped.remix_userid, key: mapped.remix_userkey });
      if (!parsed.success) fail('AUTH_EXPIRED', '书源登录未取得有效会话');
      this.session = parsed.data;
    }
    await this.raw(http, '/eapi/user/profile', undefined, signal);
    const path = join(this.config.stateDir, 'source-session.json');
    await writeFile(
      path + '.part',
      JSON.stringify({ fingerprint: this.fingerprint(), ...this.session }),
      { mode: 0o600 },
    );
    await rename(path + '.part', path);
  }
  private async auth(http: Http, signal?: AbortSignal, force = false): Promise<void> {
    if (this.authenticating) return this.authenticating;
    if (this.session && !force) return;
    this.authenticating = (async () => {
      if (!this.loaded && !this.session && this.config.sourceEmail) {
        this.loaded = true;
        try {
          const text = await readFile(join(this.config.stateDir, 'source-session.json'), 'utf8');
          if (text.length <= 16384) {
            const cached = z
              .object({ fingerprint: z.string(), ...credentials.shape })
              .parse(JSON.parse(text));
            const fingerprints = [this.fingerprint()];
            if (!this.config.sourceUrl)
              fingerprints.push(
                ...this.domains.origins.map((url) =>
                  digest({ url, email: this.config.sourceEmail }),
                ),
              );
            if (fingerprints.includes(cached.fingerprint)) this.session = cached;
          }
        } catch {
          /* Missing or unusable cache can be replaced through authenticated login. */
        }
      }
      if (!this.session || force) await this.login(http, signal);
    })();
    try {
      await this.authenticating;
    } finally {
      this.authenticating = undefined;
    }
  }
  private async api(
    path: string,
    data?: URLSearchParams,
    signal?: AbortSignal,
  ): Promise<{ result: Record<string, unknown>; http: Http }> {
    if (!this.configured) fail('SOURCE_NOT_CONFIGURED', '书源尚未配置');
    // Search/detail requests are repeatable. A file-ticket response may already
    // consume quota when lost, so a failed ticket request is never replayed.
    const attempts = this.config.sourceUrl || path.endsWith('/file') ? 1 : 2;
    for (let attempt = 0; attempt < attempts; attempt++) {
      const http = await this.domains.get(signal);
      try {
        return { result: await this.apiAt(http, path, data, signal), http };
      } catch (error) {
        if (!(error instanceof AppError) || error.code !== 'SOURCE_UNAVAILABLE' || signal?.aborted)
          throw error;
        this.domains.reject(http);
        if (attempt + 1 === attempts) throw error;
      }
    }
    return fail('SOURCE_UNAVAILABLE', '书源入口不可用', true);
  }
  private async apiAt(
    http: Http,
    path: string,
    data?: URLSearchParams,
    signal?: AbortSignal,
  ): Promise<Record<string, unknown>> {
    await this.auth(http, signal);
    const attemptedSession = this.session;
    try {
      return await this.raw(http, path, data, signal);
    } catch (error) {
      if (!(error instanceof AppError) || error.code !== 'AUTH_EXPIRED' || !this.config.sourceEmail)
        throw error;
      if (!this.session || this.session === attemptedSession) await this.auth(http, signal, true);
      return this.raw(http, path, data, signal);
    }
  }
  async search(query: string, cursor?: string, signal?: AbortSignal): Promise<Page> {
    if (!this.configured) fail('SOURCE_NOT_CONFIGURED', '书源尚未配置');
    const page = cursor ? Number(cursor) : 1;
    if (!Number.isSafeInteger(page) || page < 1 || page > 100000)
      fail('CURSOR_INVALID', '书源分页无效');
    const data = new URLSearchParams({ message: query, page: String(page), limit: '50' });
    data.append('extensions[]', 'epub');
    data.append('extensions[]', 'pdf');
    const { result } = await this.api('/eapi/book/search', data, signal);
    const list = z.array(rawBook).max(100).safeParse(result.books);
    if (!list.success) fail('UPSTREAM_INVALID', '书源候选格式不兼容');
    const pagination = z
      .object({ total_pages: z.number().int().nonnegative().optional() })
      .safeParse(result.pagination);
    const total = pagination.success ? pagination.data.total_pages : undefined;
    const books = list.data
      .map(mapBook)
      .filter((b) => b.formats.some((f) => f === 'epub' || f === 'pdf'));
    return {
      books,
      next: (total !== undefined ? page < total : list.data.length >= 50)
        ? String(page + 1)
        : undefined,
    };
  }
  async open(known: Book, signal?: AbortSignal): Promise<Response> {
    if (!this.configured || !known.hash) fail('SOURCE_NOT_CONFIGURED', '书源入口或候选不完整');
    const path =
      '/eapi/book/' + encodeURIComponent(known.id) + '/' + encodeURIComponent(known.hash);
    const { result } = await this.api(path, undefined, signal);
    const parsed = rawBook.safeParse(result.book);
    if (!parsed.success) fail('UPSTREAM_INVALID', '书源详情不兼容');
    const book = mapBook(parsed.data);
    if (
      book.id !== known.id ||
      book.hash !== known.hash ||
      !sameBook(known, book) ||
      book.formats[0] !== known.formats[0]
    )
      fail('SOURCE_CHANGED', '书源详情已变化，请重新搜索');
    const { result: link, http } = await this.api(path + '/file', undefined, signal);
    const fields = z
      .object({
        file: z.object({ downloadLink: z.string().optional() }).optional(),
        downloadLink: z.string().optional(),
        url: z.string().optional(),
        link: z.string().optional(),
      })
      .parse(link);
    const url = fields.file?.downloadLink || fields.downloadLink || fields.url || fields.link;
    if (!url) fail('QUOTA_EXCEEDED', '书源未提供下载地址，请检查额度');
    const response = await http.send(new URL(url, http.base + '/').href, {
      signal,
      allowedOrigins: [new URL(http.base).origin, ...this.config.sourceOrigins],
      publicOrigins: true,
      timeoutMs: this.config.transferMs,
      cookies: false,
      headers: this.session
        ? {
            'remix-userid': this.session.id,
            'remix-userkey': this.session.key,
            cookie:
              'remix_userid=' +
              encodeURIComponent(this.session.id) +
              '; remix_userkey=' +
              encodeURIComponent(this.session.key),
          }
        : {},
    });
    status(response, 'SOURCE');
    if (
      response.statusCode !== 200 ||
      /text\/html|application\/json/.test(String(response.headers['content-type'])) ||
      (response.headers['content-encoding'] && response.headers['content-encoding'] !== 'identity')
    ) {
      response.body.destroy();
      fail('SOURCE_FILE_INVALID', '书源未返回完整书籍文件');
    }
    return response;
  }
  async close(): Promise<void> {
    await this.domains.close();
  }
}
