import { createReadStream } from 'node:fs';
import { stat } from 'node:fs/promises';
import { randomBytes } from 'node:crypto';
import { Readable } from 'node:stream';
import { load } from 'cheerio/slim';
import { SaxesParser } from 'saxes';
import { z } from 'zod';
import {
  AppError,
  bookSchema,
  fail,
  language,
  type Book,
  type Format,
  type Page,
} from './domain.js';
import { Http, jsonBody, status, textBody, type Response } from './http.js';
import type { Config } from './config.js';

const atom = 'http://www.w3.org/2005/Atom';
const detailSchema = z.object({
  application_id: z.union([z.number().int(), z.string()]).transform(String),
  uuid: z.string(),
  title: z.string(),
  authors: z.array(z.string()),
  languages: z.array(z.string()).default([]),
  formats: z.array(z.string()).default([]),
});
interface Position {
  url: string;
  skip: number;
}
function formInfo(html: string): { token: string; login: boolean } {
  const $ = load(html);
  return {
    token: $('input[name="csrf_token"]').first().attr('value') || '',
    login: $('input[name="username"]').length > 0 && $('input[name="password"]').length > 0,
  };
}
export class Cwa {
  readonly http: Http;
  private basic: string;
  constructor(readonly config: Config) {
    this.http = new Http(config.cwaUrl, 'CWA', config.metadataMs);
    this.basic =
      'Basic ' + Buffer.from(config.cwaUser + ':' + config.cwaPassword).toString('base64');
  }
  private read(path: string, signal?: AbortSignal): Promise<Response> {
    return this.http.send(path, { headers: { authorization: this.basic }, signal, cookies: false });
  }
  async search(query: string, cursor?: string, signal?: AbortSignal): Promise<Page> {
    let pos: Position = {
      url: this.http.url('/opds/search?query=' + encodeURIComponent(query)),
      skip: 0,
    };
    if (cursor) {
      try {
        pos = z
          .object({ url: z.string().url(), skip: z.number().int().min(0).max(100000) })
          .parse(JSON.parse(cursor));
      } catch {
        fail('CURSOR_INVALID', 'CWA 游标无效');
      }
    }
    const response = await this.read(pos.url, signal);
    status(response, 'CWA');
    const xml = await textBody(response, 16 * 1024 * 1024);
    const books: Book[] = [];
    let next: string | undefined;
    let current: Book | undefined;
    let root = false;
    let count = 0;
    const stack: { local: string; uri: string; text: string }[] = [];
    const parser = new SaxesParser({ xmlns: true });
    parser.on('doctype', () => fail('UPSTREAM_INVALID', 'OPDS 不允许 DTD'));
    parser.on('opentag', (tag) => {
      if (!root) {
        if (tag.local !== 'feed' || tag.uri !== atom)
          fail('CWA_UNAVAILABLE', 'CWA 未返回 Atom/OPDS feed');
        root = true;
      }
      stack.push({ local: tag.local, uri: tag.uri, text: '' });
      if (stack.length > 32) fail('UPSTREAM_INVALID', 'OPDS 层级过深');
      if (tag.local === 'entry' && tag.uri === atom) {
        current = {
          origin: 'cwa',
          id: '',
          title: '',
          authors: [],
          language: 'unknown',
          formats: [],
        };
      }
      if (tag.local === 'link' && tag.uri === atom) {
        const href = tag.attributes.href?.value,
          rel = tag.attributes.rel?.value || '';
        if (!href) return;
        const target = new URL(href, pos.url);
        if (target.origin !== new URL(this.config.cwaUrl).origin)
          fail('ORIGIN_BLOCKED', 'CWA OPDS 链接跨来源');
        if (!current && rel === 'next') next = target.href;
        if (current) {
          const match = /\/download\/(\d+)\/([a-zA-Z0-9]+)\/?$/.exec(target.pathname);
          if (rel.includes('acquisition') && match) {
            current.id = match[1]!;
            current.formats.push(match[2]!.toLowerCase());
          }
          const cover = /\/cover(?:_[^/]*)?\/(\d+)/.exec(target.pathname);
          if (!current.id && cover) current.id = cover[1]!;
        }
      }
    });
    parser.on('text', (text) => {
      const top = stack.at(-1);
      if (top) top.text += text;
    });
    parser.on('cdata', (text) => {
      const top = stack.at(-1);
      if (top) top.text += text;
    });
    parser.on('closetag', (tag) => {
      const node = stack.pop()!;
      const value = node.text.trim();
      if (current) {
        if (tag.uri === atom) {
          if (tag.local === 'title') current.title = value;
          if (tag.local === 'id') current.uuid = value.replace(/^urn:uuid:/, '');
          if (tag.local === 'name' && stack.at(-1)?.local === 'author') current.authors.push(value);
        }
        if (
          tag.local === 'language' &&
          (tag.uri === 'http://purl.org/dc/terms/' ||
            tag.uri === 'http://purl.org/dc/elements/1.1/')
        ) {
          const lang = language(value);
          if (current.language === 'unknown' || lang === 'zh') current.language = lang;
        }
        if (tag.local === 'entry' && tag.uri === atom) {
          if (current.id) {
            count++;
            if (count > pos.skip && books.length < 50) books.push(bookSchema.parse(current));
          }
          current = undefined;
        }
      }
    });
    try {
      parser.write(xml).close();
    } catch (error) {
      if (error instanceof AppError) throw error;
      fail('UPSTREAM_INVALID', 'CWA OPDS 响应无效');
    }
    if (!root) fail('UPSTREAM_INVALID', 'CWA OPDS 为空');
    if (count > pos.skip + 50)
      return { books, next: JSON.stringify({ ...pos, skip: pos.skip + 50 }) };
    if (next === pos.url) fail('SEARCH_INCOMPLETE', 'CWA 分页没有前进');
    return { books, next: next ? JSON.stringify({ url: next, skip: 0 }) : undefined };
  }
  async book(known: Book, signal?: AbortSignal): Promise<Book> {
    if (!known.uuid || !/^\d+$/.test(known.id))
      fail('BOOK_IDENTITY_CHANGED', 'CWA 候选缺少有效 ID/UUID');
    const response = await this.read('/ajax/book/' + encodeURIComponent(known.uuid), signal);
    status(response, 'CWA');
    const result = detailSchema.safeParse(await jsonBody(response));
    if (!result.success) fail('UPSTREAM_INVALID', 'CWA 详情不兼容');
    const d = result.data;
    if (d.application_id !== known.id || d.uuid !== known.uuid)
      fail('BOOK_IDENTITY_CHANGED', 'CWA 记录已删除或身份发生变化');
    return bookSchema.parse({
      ...known,
      title: d.title,
      authors: d.authors,
      language: d.languages.some((s) => language(s) === 'zh') ? 'zh' : language(d.languages[0]),
      formats: d.formats.map((s) => s.toLowerCase()),
    });
  }
  async open(book: Book, format: Format, signal?: AbortSignal): Promise<Response> {
    if (!/^\d+$/.test(book.id) || !book.formats.includes(format))
      fail('FORMAT_UNAVAILABLE', 'CWA 尚无所需格式');
    const r = await this.http.send('/opds/download/' + book.id + '/' + format + '/', {
      headers: { authorization: this.basic },
      signal,
      cookies: false,
      timeoutMs: this.config.transferMs,
    });
    status(r, 'CWA');
    const type = String(r.headers['content-type'] || '')
      .split(';')[0]
      ?.toLowerCase();
    if (
      r.statusCode !== 200 ||
      ![
        'application/octet-stream',
        'application/zip',
        format === 'pdf' ? 'application/pdf' : 'application/epub+zip',
      ].includes(type || '') ||
      (r.headers['content-encoding'] && r.headers['content-encoding'] !== 'identity')
    ) {
      r.body.destroy();
      fail('CWA_FILE_INVALID', 'CWA 未返回完整目标格式文件');
    }
    return r;
  }
  async prepareUpload(signal?: AbortSignal): Promise<string> {
    const readForm = async (path: string) => {
      const r = await this.http.send(path, { signal });
      status(r, 'CWA');
      return formInfo(await textBody(r));
    };
    const current = await readForm('/me');
    if (!current.login && current.token) return current.token;
    const login = await readForm('/login');
    if (!login.token) fail('AUTH_REQUIRED', 'CWA 登录页没有 CSRF，请检查版本和登录方式');
    const r = await this.http.send('/login', {
      method: 'POST',
      signal,
      headers: { 'content-type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({
        username: this.config.cwaUser,
        password: this.config.cwaPassword,
        csrf_token: login.token,
      }).toString(),
    });
    status(r, 'CWA');
    await textBody(r);
    const verified = await readForm('/me');
    if (verified.login || !verified.token) fail('AUTH_EXPIRED', 'CWA 登录未通过受保护页面验证');
    return verified.token;
  }
  async upload(path: string, format: Format, token: string, signal?: AbortSignal): Promise<void> {
    const boundary = 'cwa-' + randomBytes(18).toString('hex');
    const prefix = Buffer.from(
      '--' +
        boundary +
        '\r\nContent-Disposition: form-data; name="csrf_token"\r\n\r\n' +
        token +
        '\r\n--' +
        boundary +
        '\r\nContent-Disposition: form-data; name="btn-upload"; filename="book.' +
        format +
        '"\r\nContent-Type: application/octet-stream\r\n\r\n',
    );
    const suffix = Buffer.from('\r\n--' + boundary + '--\r\n');
    const size = (await stat(path)).size;
    const body = Readable.from(
      (async function* () {
        yield prefix;
        yield* createReadStream(path);
        yield suffix;
      })(),
    );
    try {
      const r = await this.http.send('/upload', {
        method: 'POST',
        body,
        signal,
        timeoutMs: this.config.transferMs,
        headers: {
          'content-type': 'multipart/form-data; boundary=' + boundary,
          'content-length': String(size + prefix.length + suffix.length),
        },
      });
      // Even explicit error pages may follow ingestion. Once sent, reconciliation is mandatory.
      const data = await textBody(r);
      if (r.statusCode !== 200 || formInfo(data).login)
        fail('UPLOAD_OUTCOME_UNKNOWN', 'CWA 提交结果不明，需要核对书库');
      let result: unknown;
      try {
        result = JSON.parse(data);
      } catch {
        fail('UPLOAD_OUTCOME_UNKNOWN', 'CWA 提交响应不可确认，需要核对书库');
      }
      if (!z.object({ location: z.string().min(1) }).safeParse(result).success)
        fail('UPLOAD_OUTCOME_UNKNOWN', 'CWA 提交响应不可确认，需要核对书库');
    } catch {
      fail('UPLOAD_OUTCOME_UNKNOWN', 'CWA 提交可能已接收，需要核对书库');
    } finally {
      body.destroy();
    }
  }
  close(): Promise<void> {
    return this.http.close();
  }
}
