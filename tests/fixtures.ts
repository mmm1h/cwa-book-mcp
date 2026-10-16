import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { readFile, mkdtemp, rm } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';
import { loadConfig } from '../src/config.js';
import type { Book } from '../src/domain.js';
import CRC32 from 'crc-32';

export function storedZip(entries: Record<string, string>): Buffer {
  const local: Buffer[] = [],
    central: Buffer[] = [];
  let offset = 0;
  for (const [path, text] of Object.entries(entries)) {
    const name = Buffer.from(path),
      data = Buffer.from(text),
      crc = CRC32.buf(data) >>> 0;
    const head = Buffer.alloc(30);
    head.writeUInt32LE(0x04034b50);
    head.writeUInt16LE(20, 4);
    head.writeUInt16LE(0x800, 6);
    head.writeUInt32LE(crc, 14);
    head.writeUInt32LE(data.length, 18);
    head.writeUInt32LE(data.length, 22);
    head.writeUInt16LE(name.length, 26);
    const dir = Buffer.alloc(46);
    dir.writeUInt32LE(0x02014b50);
    dir.writeUInt16LE(20, 4);
    dir.writeUInt16LE(20, 6);
    dir.writeUInt16LE(0x800, 8);
    dir.writeUInt32LE(crc, 16);
    dir.writeUInt32LE(data.length, 20);
    dir.writeUInt32LE(data.length, 24);
    dir.writeUInt16LE(name.length, 28);
    dir.writeUInt32LE(offset, 42);
    local.push(head, name, data);
    central.push(dir, name);
    offset += head.length + name.length + data.length;
  }
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50);
  end.writeUInt16LE(Object.keys(entries).length, 8);
  end.writeUInt16LE(Object.keys(entries).length, 10);
  end.writeUInt32LE(
    central.reduce((n, b) => n + b.length, 0),
    12,
  );
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...local, ...central, end]);
}

export const epub = await readFile(new URL('../testdata/synthetic.epub', import.meta.url));
export const pdf = await readFile(new URL('../testdata/synthetic.pdf', import.meta.url));
export async function body(req: IncomingMessage): Promise<Buffer> {
  const chunks: Buffer[] = [];
  for await (const chunk of req) chunks.push(Buffer.from(chunk));
  return Buffer.concat(chunks);
}
export async function listen(
  handler: (req: IncomingMessage, res: ServerResponse) => Promise<void> | void,
) {
  const server = createServer((req, res) => {
    void Promise.resolve(handler(req, res)).catch(() => {
      res.writeHead(500);
      res.end();
    });
  });
  await new Promise<void>((resolve) => server.listen(0, '127.0.0.1', resolve));
  const port = (server.address() as { port: number }).port;
  return {
    server,
    url: 'http://127.0.0.1:' + port,
    close: async () => {
      server.closeAllConnections();
      await new Promise<void>((resolve) => server.close(() => resolve()));
    },
  };
}
const xml = (value: string) =>
  value.replaceAll('&', '&amp;').replaceAll('<', '&lt;').replaceAll('"', '&quot;');
export function book(overrides: Partial<Book> = {}): Book {
  return {
    origin: 'cwa',
    id: '1',
    uuid: randomUUID(),
    title: '合成测试书',
    authors: ['测试作者'],
    language: 'zh',
    formats: ['epub'],
    ...overrides,
  };
}
export async function mockCwa(basePath = '') {
  const state = {
    books: [] as Book[],
    uploads: 0,
    downloadCount: 0,
    downloadStatus: 200,
    downloadType: undefined as string | undefined,
    downloadRedirect: undefined as string | undefined,
    acquisitionHref: undefined as string | undefined,
    opdsLength: epub.length,
    dropUpload: false,
    hangUpload: false,
    rejectUpload: false,
    acceptDespiteRejection: false,
    uploadFeedbackShown: false,
    nextBook: book(),
    badFile: false,
    identityChanged: false,
    failSearch: false,
    headers: [] as IncomingMessage['headers'][],
    bodies: [] as Buffer[],
  };
  const endpoint = await listen(async (req, res) => {
    const u = new URL(req.url!, 'http://localhost');
    if (!u.pathname.startsWith(basePath + '/')) {
      res.writeHead(404);
      res.end();
      return;
    }
    u.pathname = u.pathname.slice(basePath.length);
    if (u.pathname.startsWith('/opds/') || u.pathname.startsWith('/ajax/')) {
      if (
        req.headers.authorization !==
        'Basic ' + Buffer.from('reader:synthetic-password').toString('base64')
      ) {
        res.writeHead(401);
        res.end();
        return;
      }
    }
    if (u.pathname === '/opds/search') {
      if (state.failSearch) {
        res.writeHead(503);
        res.end();
        return;
      }
      const query = (u.searchParams.get('query') || '').toLowerCase();
      const entries = state.books
        .filter((b) => b.title.toLowerCase().includes(query))
        .map(
          (b) =>
            '<entry><id>urn:uuid:' +
            xml(b.uuid!) +
            '</id><title>' +
            xml(b.title) +
            '</title>' +
            b.authors.map((a) => '<author><name>' + xml(a) + '</name></author>').join('') +
            '<dc:language>' +
            b.language +
            '</dc:language><link rel="http://opds-spec.org/image" href="' +
            basePath +
            '/opds/cover/' +
            b.id +
            '"/>' +
            b.formats
              .map(
                (f) =>
                  '<link rel="http://opds-spec.org/acquisition" href="' +
                  xml(
                    state.acquisitionHref || basePath + '/opds/download/' + b.id + '/' + f + '/',
                  ) +
                  '" length="' +
                  state.opdsLength +
                  '"/>',
              )
              .join('') +
            '</entry>',
        );
      res.setHeader('content-type', 'application/atom+xml');
      res.end(
        '<feed xmlns="http://www.w3.org/2005/Atom" xmlns:dc="http://purl.org/dc/terms/">' +
          entries.join('') +
          '</feed>',
      );
      return;
    }
    if (u.pathname.startsWith('/ajax/book/')) {
      const b = state.books.find(
        (b) => b.uuid === decodeURIComponent(u.pathname.split('/').at(-1)!),
      );
      if (!b) {
        res.writeHead(404);
        res.end();
        return;
      }
      res.setHeader('content-type', 'application/json');
      res.end(
        JSON.stringify({
          application_id: state.identityChanged ? 999 : Number(b.id),
          uuid: b.uuid,
          title: b.title,
          authors: b.authors,
          languages: [b.language],
          formats: b.formats.map((f) => f.toUpperCase()),
        }),
      );
      return;
    }
    if (u.pathname.startsWith('/opds/download/')) {
      state.downloadCount++;
      if (state.downloadRedirect || state.downloadStatus !== 200) {
        res.writeHead(
          state.downloadRedirect ? 302 : state.downloadStatus,
          state.downloadRedirect ? { location: state.downloadRedirect } : {},
        );
        res.end();
        return;
      }
      const f = u.pathname.split('/')[4];
      const data = state.badFile ? Buffer.from('not a valid book') : f === 'pdf' ? pdf : epub;
      res.writeHead(200, {
        'content-type':
          state.downloadType || (f === 'pdf' ? 'application/pdf' : 'application/epub+zip'),
        'content-length': data.length,
      });
      res.end(data);
      return;
    }
    const logged = req.headers.cookie?.includes('session=synthetic');
    if (u.pathname === '/me') {
      res.setHeader('content-type', 'text/html');
      const feedback =
        logged && state.rejectUpload && state.uploads > 0 && !state.uploadFeedbackShown
          ? '<div class="alert alert-danger">上传任务未能成功排队等待处理: private server detail</div>'
          : '';
      if (feedback) state.uploadFeedbackShown = true;
      res.end(
        (logged
          ? '<input name="csrf_token" value="verified-token"><input name="password">'
          : '<input name="username"><input name="password"><input name="csrf_token" value="login-token">') +
          feedback,
      );
      return;
    }
    if (u.pathname === '/login') {
      if (req.method === 'POST') {
        const form = new URLSearchParams((await body(req)).toString());
        if (
          form.get('csrf_token') !== 'login-token' ||
          form.get('password') !== 'synthetic-password'
        ) {
          res.writeHead(403);
          res.end();
          return;
        }
        res.writeHead(302, {
          'set-cookie': 'session=synthetic; Path=/; HttpOnly',
          location: basePath + '/me',
        });
        res.end();
        return;
      }
      res.end(
        '<input name="username"><input name="password"><input name="csrf_token" value="login-token">',
      );
      return;
    }
    if (u.pathname === '/upload') {
      const bytes = await body(req);
      if (
        !logged ||
        !bytes.includes(Buffer.from('verified-token')) ||
        !bytes.includes(Buffer.from('name="btn-upload"')) ||
        bytes.includes(Buffer.from('btn-upload-format'))
      ) {
        res.writeHead(403);
        res.end();
        return;
      }
      state.uploads++;
      state.headers.push(req.headers);
      state.bodies.push(bytes);
      if (
        (!state.rejectUpload || state.acceptDespiteRejection) &&
        !state.books.some((b) => b.uuid === state.nextBook.uuid)
      )
        state.books.push(state.nextBook);
      if (state.dropUpload || state.hangUpload) {
        if (state.dropUpload) res.destroy();
        return;
      }
      res.setHeader('content-type', 'application/json');
      res.end(JSON.stringify({ location: '/book/' + state.nextBook.id }));
      return;
    }
    res.writeHead(404);
    res.end();
  });
  return { ...endpoint, url: endpoint.url + basePath, state };
}
export async function testConfig(base: string, extra: NodeJS.ProcessEnv = {}) {
  const dir = await mkdtemp(join(tmpdir(), 'cwa 测试 space-'));
  const config = loadConfig({
    CWA_BASE_URL: base,
    CWA_USERNAME: 'reader',
    CWA_PASSWORD: 'synthetic-password',
    STATE_DIR: dir,
    CWA_WRITE_ENABLED: 'true',
    ...extra,
  });
  config.pollMs = 10;
  return { config, cleanup: () => rm(dir, { recursive: true, force: true }) };
}
export async function until(
  predicate: () => boolean | Promise<boolean>,
  ms = 10000,
): Promise<void> {
  const deadline = Date.now() + ms;
  while (!(await predicate())) {
    if (Date.now() > deadline) throw new Error('condition timed out');
    await new Promise((resolve) => setTimeout(resolve, 25));
  }
}

export async function mockSource(format: 'epub' | 'pdf' = 'epub') {
  const data = format === 'pdf' ? pdf : epub;
  const state = {
    tickets: 0,
    downloads: 0,
    privateLink: false,
    authRequired: false,
    badFile: false,
    dropTicket: false,
    expires: undefined as number | undefined,
    headers: [] as IncomingMessage['headers'][],
  };
  const endpoint = await listen(async (req, res) => {
    const url = new URL(req.url!, 'http://localhost');
    if (url.pathname === '/file' || url.pathname === '/redirect') {
      state.headers.push(req.headers);
      if (state.authRequired && !req.headers['remix-userkey']) {
        res.writeHead(401);
        res.end();
        return;
      }
      if (url.pathname === '/redirect') {
        res.writeHead(302, {
          location:
            '/file?ticket=single-file-capability' +
            (state.expires ? '&expires=' + state.expires : ''),
        });
        res.end();
        return;
      }
      state.downloads++;
      const content = state.badFile ? Buffer.from('broken') : data;
      res.writeHead(200, {
        'content-type': format === 'pdf' ? 'application/pdf' : 'application/epub+zip',
        'content-length': content.length,
      });
      res.end(content);
      return;
    }
    res.setHeader('content-type', 'application/json');
    if (
      req.headers['remix-userid'] !== '7' ||
      req.headers['remix-userkey'] !== 'private-session-key'
    ) {
      res.end(JSON.stringify({ success: 0, error: 'Session expired' }));
      return;
    }
    const raw = {
      id: 25,
      hash: 'hash25',
      title: '合成测试书',
      author: '测试作者',
      language: 'chinese',
      extension: format,
      filesize: data.length,
    };
    if (url.pathname === '/eapi/book/search') {
      await body(req);
      res.end(JSON.stringify({ success: 1, books: [raw], pagination: { total_pages: 1 } }));
    } else if (url.pathname === '/eapi/book/25/hash25/file') {
      state.tickets++;
      if (state.dropTicket) {
        res.destroy();
        return;
      }
      res.end(
        JSON.stringify({
          success: 1,
          file: {
            downloadLink: state.privateLink
              ? '/file?remix_userkey=private-session-key'
              : '/redirect',
          },
        }),
      );
    } else res.end(JSON.stringify({ success: 1, book: raw, user: { id: 7 } }));
  });
  return { ...endpoint, state, data };
}
