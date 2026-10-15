import { createHmac, timingSafeEqual } from 'node:crypto';
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http';
import { createReadStream } from 'node:fs';
import { pipeline } from 'node:stream/promises';
import { z } from 'zod';
import { fail, formatSchema, publicError, type Task } from './domain.js';
import type { Config } from './config.js';
import type { Tasks } from './tasks.js';

const ticketSchema = z
  .object({
    purpose: z.enum(['upload', 'download']),
    task_id: z.string().uuid(),
    format: formatSchema,
    expires: z.number().int(),
    book_id: z.string().optional(),
    uuid: z.string().optional(),
  })
  .strict();
type Ticket = z.infer<typeof ticketSchema>;
export class Tickets {
  constructor(private key: Buffer) {}
  sign(ticket: Ticket): string {
    const payload = Buffer.from(JSON.stringify(ticket)).toString('base64url');
    return payload + '.' + createHmac('sha256', this.key).update(payload).digest('base64url');
  }
  verify(raw: string): Ticket {
    if (raw.length > 2048) fail('TICKET_INVALID', '授权票据无效');
    const [payload, mac, ...extra] = raw.split('.');
    if (!payload || !mac || extra.length) fail('TICKET_INVALID', '授权票据无效');
    const expected = createHmac('sha256', this.key).update(payload).digest();
    const got = Buffer.from(mac, 'base64url');
    if (got.length !== expected.length || !timingSafeEqual(got, expected))
      fail('TICKET_INVALID', '授权票据无效');
    let ticket: Ticket;
    try {
      ticket = ticketSchema.parse(JSON.parse(Buffer.from(payload, 'base64url').toString('utf8')));
    } catch {
      return fail('TICKET_INVALID', '授权票据无效');
    }
    if (ticket.expires <= Math.floor(Date.now() / 1000))
      fail('TICKET_EXPIRED', '授权票据已过期，请刷新链接');
    return ticket;
  }
}
export class Gateway {
  readonly tickets: Tickets;
  readonly server;
  baseUrl = '';
  private active = 0;
  private requests = new Set<Promise<void>>();
  private abort = new AbortController();
  constructor(
    readonly config: Config,
    readonly tasks: Tasks,
  ) {
    this.tickets = new Tickets(tasks.store.key);
    this.server = createServer(
      { maxHeaderSize: 8192, requestTimeout: config.transferMs, headersTimeout: 30000 },
      (req, res) => {
        const promise = this.handle(req, res)
          .catch((error) => {
            if (res.headersSent) {
              res.destroy();
              return;
            }
            const e = publicError(error);
            const status = e.code.startsWith('TICKET_')
              ? 403
              : e.code === 'RANGE_UNSUPPORTED'
                ? 416
                : e.code === 'UPLOAD_IN_PROGRESS' || e.code === 'IDEMPOTENCY_CONFLICT'
                  ? 409
                  : e.code === 'FILE_TOO_LARGE'
                    ? 413
                    : e.code === 'TRANSFER_LIMIT'
                      ? 503
                      : 400;
            res.writeHead(status, {
              'content-type': 'application/json',
              'cache-control': 'no-store',
              connection: 'close',
            });
            res.end(JSON.stringify({ error: e }));
          })
          .finally(() => {
            this.requests.delete(promise);
          });
        this.requests.add(promise);
      },
    );
    this.server.on('clientError', (_error, socket) => {
      socket.end('HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n');
    });
  }
  async listen(): Promise<void> {
    await new Promise<void>((resolve, reject) => {
      this.server.once('error', reject);
      this.server.listen(this.config.filePort, this.config.fileHost, () => {
        this.server.removeListener('error', reject);
        resolve();
      });
    }).catch((error: NodeJS.ErrnoException) => {
      fail(
        error.code === 'EADDRINUSE' ? 'FILE_PORT_IN_USE' : 'LISTEN_FAILED',
        error.code === 'EADDRINUSE'
          ? 'FILE_PORT 已被占用，请为此实例配置独立端口'
          : '文件接口监听失败，请检查 FILE_HOST/FILE_PORT',
      );
    });
    const address = this.server.address();
    if (!address || typeof address === 'string') fail('LISTEN_FAILED', '文件接口无法监听');
    const host = this.config.fileHost.includes(':')
      ? '[' + this.config.fileHost + ']'
      : this.config.fileHost;
    this.baseUrl = this.config.publicUrl || 'http://' + host + ':' + address.port;
  }
  upload(t: Task): Record<string, unknown> {
    if (t.state !== 'awaiting_upload') return { task_id: t.id, state: t.state };
    const expires = Math.floor(Date.now() / 1000) + this.config.ticketSeconds;
    const ticket = this.tickets.sign({
      purpose: 'upload',
      task_id: t.id,
      format: t.book.formats[0] as 'epub' | 'pdf',
      expires,
    });
    return {
      task_id: t.id,
      state: t.state,
      upload: {
        method: 'PUT',
        url: this.baseUrl + '/files/upload/' + t.id + '?ticket=' + ticket,
        headers: {
          'Content-Length': String(t.size_bytes),
          'Content-Type': t.book.formats[0] === 'pdf' ? 'application/pdf' : 'application/epub+zip',
        },
        expires_at: new Date(expires * 1000).toISOString(),
      },
    };
  }
  async downloadUrl(taskId: string, signal?: AbortSignal): Promise<Record<string, unknown>> {
    const task = this.tasks.store.get(taskId);
    if (task.state !== 'ready' || !task.cwa) fail('TASK_NOT_READY', '任务尚未验证完成');
    const verified = await this.tasks.verifiedFile(task.cwa, task.required_format, signal);
    try {
      if (this.tasks.store.get(taskId).state !== 'ready')
        fail('TASK_NOT_READY', '任务已取消或不再可下载');
      const expires = Math.floor(Date.now() / 1000) + this.config.ticketSeconds;
      const ticket = this.tickets.sign({
        purpose: 'download',
        task_id: task.id,
        book_id: task.cwa.id,
        uuid: task.cwa.uuid,
        format: task.required_format,
        expires,
      });
      return {
        task_id: task.id,
        url: this.baseUrl + '/files/download/' + task.id + '?ticket=' + ticket,
        expires_at: new Date(expires * 1000).toISOString(),
        ...verified.info,
      };
    } finally {
      await this.tasks.files.remove(verified.key);
    }
  }
  private async handle(req: IncomingMessage, res: ServerResponse): Promise<void> {
    if (this.active >= 4) fail('TRANSFER_LIMIT', '文件并发上限已达到，请稍后重试', true);
    const url = new URL(req.url || '/', 'http://localhost');
    let pathname = url.pathname;
    const prefix = this.config.publicUrl
      ? new URL(this.config.publicUrl).pathname.replace(/\/$/, '')
      : '';
    if (prefix && pathname.startsWith(prefix + '/')) pathname = pathname.slice(prefix.length);
    const match = /^\/files\/(upload|download)\/([a-zA-Z0-9-]+)$/.exec(pathname);
    if (!match) {
      res.writeHead(404);
      res.end();
      return;
    }
    const ticket = this.tickets.verify(url.searchParams.get('ticket') || '');
    if (ticket.purpose !== match[1] || ticket.task_id !== match[2])
      fail('TICKET_INVALID', '授权用途或任务不符');
    if (req.headers.range) fail('RANGE_UNSUPPORTED', '不支持 Range，请重新传输完整文件');
    if (
      (ticket.purpose === 'upload' && req.method !== 'PUT') ||
      (ticket.purpose === 'download' && req.method !== 'GET')
    ) {
      res.writeHead(405, { Allow: ticket.purpose === 'upload' ? 'PUT' : 'GET' });
      res.end();
      return;
    }
    this.active++;
    const clientAbort = new AbortController();
    const onClose = () => {
      if (!res.writableFinished) clientAbort.abort();
    };
    res.once('close', onClose);
    const signal = AbortSignal.any([
      this.abort.signal,
      clientAbort.signal,
      AbortSignal.timeout(this.config.transferMs),
    ]);
    try {
      if (ticket.purpose === 'upload') {
        const raw = req.headers['content-length'];
        if (
          !raw ||
          !/^\d+$/.test(raw) ||
          !Number.isSafeInteger(Number(raw)) ||
          req.headers['transfer-encoding'] ||
          (req.headers['content-encoding'] && req.headers['content-encoding'] !== 'identity')
        )
          fail('LENGTH_REQUIRED', 'PUT 需要准确 Content-Length 和未压缩整文件');
        const task = this.tasks.store.get(ticket.task_id);
        if (ticket.format !== task.book.formats[0]) fail('TICKET_INVALID', '授权格式不符');
        const result = await this.tasks.receive(ticket.task_id, req, Number(raw), signal);
        res.writeHead(200, { 'content-type': 'application/json', 'cache-control': 'no-store' });
        res.end(JSON.stringify(result));
      } else {
        const task = this.tasks.store.get(ticket.task_id);
        if (
          task.state !== 'ready' ||
          !task.cwa ||
          task.cwa.id !== ticket.book_id ||
          task.cwa.uuid !== ticket.uuid ||
          task.required_format !== ticket.format
        )
          fail('TICKET_INVALID', '下载授权与当前任务身份不符');
        const verified = await this.tasks.verifiedFile(task.cwa, ticket.format, signal);
        try {
          if (this.tasks.store.get(task.id).state !== 'ready') fail('TASK_NOT_READY', '任务已取消');
          res.writeHead(200, {
            'content-type': ticket.format === 'pdf' ? 'application/pdf' : 'application/epub+zip',
            'content-length': verified.info.size_bytes,
            'cache-control': 'no-store',
            'x-content-type-options': 'nosniff',
            'content-disposition': 'attachment; filename="book.' + ticket.format + '"',
            etag: '"' + verified.info.sha256 + '"',
            'accept-ranges': 'none',
          });
          await pipeline(createReadStream(this.tasks.files.path(verified.key)), res, { signal });
        } finally {
          await this.tasks.files.remove(verified.key);
        }
      }
    } finally {
      this.active--;
      res.removeListener('close', onClose);
    }
  }
  async close(): Promise<void> {
    this.abort.abort();
    await new Promise<void>((resolve) => {
      this.server.close(() => resolve());
      this.server.closeAllConnections();
    });
    await Promise.allSettled(this.requests);
  }
}
