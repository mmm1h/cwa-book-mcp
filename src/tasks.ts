import { Readable } from 'node:stream';
import { z } from 'zod';
import {
  bookSchema,
  fail,
  formatSchema,
  id,
  language,
  publicError,
  sameBook,
  terminal,
  type Book,
  type Format,
  type Task,
} from './domain.js';
import { type Config } from './config.js';
import { Catalog } from './catalog.js';
import { Cwa } from './cwa.js';
import { Source } from './source.js';
import { Files, hashStream, validateFile, type FileInfo } from './files.js';
import { Store } from './store.js';

const requestId = z.string().trim().min(1).max(128);
const reference = z.string().uuid();
export const acquireSchema = z
  .object({
    action: z.enum(['start', 'retry', 'resolve', 'cancel']).default('start'),
    request_id: requestId.optional(),
    candidate_id: reference.optional(),
    task_id: reference.optional(),
    required_format: formatSchema.optional(),
  })
  .strict();
export const uploadSchema = z
  .object({
    action: z.enum(['prepare', 'refresh_ticket']).default('prepare'),
    request_id: requestId.optional(),
    task_id: reference.optional(),
    title: z.string().trim().min(1).max(1024).optional(),
    authors: z.array(z.string().trim().min(1).max(256)).max(16).default([]),
    language: z.string().max(64).default('unknown'),
    filename: z.string().min(1).max(256).optional(),
    size_bytes: z.number().int().positive().optional(),
    sha256: z
      .string()
      .regex(/^[a-fA-F0-9]{64}$/)
      .transform((v) => v.toLowerCase())
      .optional(),
    required_format: formatSchema.optional(),
  })
  .strict();
export type AcquireInput = z.infer<typeof acquireSchema>;
export type UploadInput = z.infer<typeof uploadSchema>;

export class Tasks {
  private timer?: NodeJS.Timeout;
  private stopping = false;
  private running?: Promise<void>;
  private active?: { id: string; abort: AbortController };
  private receiving = new Set<string>();
  private receiverAborts = new Map<string, AbortController>();
  private cleanedAt = 0;
  constructor(
    readonly config: Config,
    readonly store: Store,
    readonly files: Files,
    readonly cwa: Cwa,
    readonly source: Source,
    readonly catalog: Catalog,
  ) {}
  private writeAllowed(): void {
    if (!this.config.writeEnabled)
      fail('WRITE_DISABLED', '确认 CWA 设置后由操作者启用 CWA_WRITE_ENABLED');
  }
  private newTask(kind: Task['kind'], book: Book, format: Format, state: Task['state']): Task {
    const now = Date.now();
    return {
      id: id(),
      kind,
      book,
      required_format: format,
      state,
      created_at: now,
      updated_at: now,
      next_run_at: now,
    };
  }
  private publicTask(t: Task): Record<string, unknown> {
    const { filename: _filename, expected_sha256: _expected, staged_sha256: _staged, ...rest } = t;
    return { ...rest, choices: t.choices?.map((b) => this.catalog.expose(b)) };
  }
  get(taskId: string): Record<string, unknown> {
    return this.publicTask(this.store.get(taskId));
  }
  acquire(input: AcquireInput): Record<string, unknown> {
    if (input.action === 'start') {
      if (!input.request_id || !input.candidate_id || !input.required_format || input.task_id)
        fail('INVALID_ARGUMENT', 'start 需要 request_id、candidate_id、required_format');
      const existing = this.store.existing(input.request_id, input);
      if (existing) return this.publicTask(existing);
      const book = this.catalog.candidate(input.candidate_id);
      if (book.origin !== 'cwa') this.writeAllowed();
      const t = this.store.create(input.request_id, input, () =>
        this.newTask('acquire', book, input.required_format!, 'queued'),
      );
      this.wake();
      return this.publicTask(t);
    }
    if (!input.task_id || input.request_id || input.required_format)
      fail('INVALID_ARGUMENT', '操作需要 task_id；仅 start 接受 request_id/required_format');
    let t = this.store.get(input.task_id);
    if (input.action === 'cancel') {
      if (t.state === 'cancelled') return this.publicTask(t);
      if (this.active?.id === t.id) this.active.abort.abort();
      this.receiverAborts.get(t.id)?.abort();
      t = this.store.mutate(t.id, (current) => {
        current.state = 'cancelled';
        current.error = current.submitted_at
          ? {
              code: 'CANCELLED_AFTER_SUBMISSION',
              message: '已取消跟踪；CWA 可能已接收，未删除书库记录',
              retryable: false,
            }
          : undefined;
      });
      this.wake();
      return this.publicTask(t);
    }
    if (input.action === 'retry') {
      if (!['blocked', 'needs_review', 'failed'].includes(t.state))
        fail('INVALID_STATE', '只有受阻、待核对或失败任务可以重试');
      if (t.kind === 'upload' || t.book.origin !== 'cwa') this.writeAllowed();
      t = this.store.mutate(t.id, (current) => {
        current.state = current.submitted_at
          ? 'reconciling'
          : current.staged_sha256
            ? 'staged'
            : current.kind === 'upload'
              ? 'awaiting_upload'
              : 'queued';
        current.error = undefined;
        current.waiting_for = undefined;
        current.next_run_at = Date.now();
        current.deadline = Date.now() + this.config.confirmMs;
      });
    } else {
      if (!input.candidate_id || t.state !== 'needs_review')
        fail('INVALID_STATE', 'resolve 需要 needs_review 任务与所列 CWA 候选');
      const book = this.catalog.candidate(input.candidate_id);
      if (
        book.origin !== 'cwa' ||
        !t.choices?.some((b) => b.id === book.id && b.uuid === book.uuid) ||
        (language(t.book.language) !== 'unknown' &&
          language(book.language) !== language(t.book.language))
      )
        fail('INVALID_ARGUMENT', '选择不属于当前任务或语言不匹配');
      t = this.store.mutate(t.id, (current) => {
        current.cwa = book;
        current.state = 'verifying';
        current.error = undefined;
        current.next_run_at = Date.now();
        current.deadline = Date.now() + this.config.confirmMs;
      });
    }
    this.wake();
    return this.publicTask(t);
  }
  upload(input: UploadInput): Task {
    this.writeAllowed();
    if (input.action === 'refresh_ticket') {
      if (!input.task_id) fail('INVALID_ARGUMENT', 'refresh_ticket 需要 task_id');
      const t = this.store.get(input.task_id);
      if (t.kind !== 'upload' || t.state !== 'awaiting_upload')
        fail('INVALID_STATE', '任务当前不接受上传');
      return t;
    }
    if (
      !input.request_id ||
      !input.title ||
      !input.filename ||
      !input.size_bytes ||
      !input.required_format ||
      input.task_id
    )
      fail(
        'INVALID_ARGUMENT',
        'prepare 需要 request_id、title、filename、size_bytes、required_format',
      );
    if (input.size_bytes > this.config.maxFileBytes) fail('FILE_TOO_LARGE', '文件超过大小上限');
    if (/[\\/]/.test(input.filename) || [...input.filename].some((c) => c.charCodeAt(0) < 32))
      fail('INVALID_ARGUMENT', 'filename 必须为无路径的文件名');
    const format = formatSchema.safeParse(input.filename.split('.').at(-1)?.toLowerCase());
    if (!format.success) fail('UNSUPPORTED_FORMAT', '仅接收 EPUB/PDF');
    const book = bookSchema.parse({
      origin: 'upload',
      id: '',
      title: input.title,
      authors: input.authors,
      language: language(input.language),
      formats: [format.data],
    });
    return this.store.create(input.request_id, input, () => ({
      ...this.newTask('upload', book, input.required_format!, 'awaiting_upload'),
      filename: input.filename,
      size_bytes: input.size_bytes,
      expected_sha256: input.sha256,
    }));
  }
  async receive(
    taskId: string,
    body: Readable,
    length: number,
    signal?: AbortSignal,
  ): Promise<Record<string, unknown>> {
    this.writeAllowed();
    const t = this.store.get(taskId);
    if (this.receiving.has(taskId)) fail('UPLOAD_IN_PROGRESS', '同一任务正在上传', true);
    if (t.kind !== 'upload' || t.state === 'cancelled' || t.state === 'failed')
      fail('INVALID_STATE', '任务当前不接受上传');
    if (length !== t.size_bytes) fail('LENGTH_MISMATCH', 'Content-Length 与任务声明不符');
    this.receiving.add(taskId);
    const abort = new AbortController();
    this.receiverAborts.set(taskId, abort);
    const combined = signal ? AbortSignal.any([signal, abort.signal]) : abort.signal;
    try {
      if (t.staged_sha256) {
        const info = await hashStream(body, this.config.maxFileBytes, length, combined);
        if (info.sha256 !== t.staged_sha256) fail('IDEMPOTENCY_CONFLICT', '重复上传的文件内容不同');
        return { task_id: t.id, accepted: true, replayed: true };
      }
      if (t.state !== 'awaiting_upload') fail('INVALID_STATE', '任务当前不接受上传');
      const info = await this.files.save(
        t.id,
        body,
        t.book.formats[0] as Format,
        length,
        t.expected_sha256,
        combined,
      );
      this.store.mutate(t.id, (current) => {
        if (current.state === 'cancelled') return;
        current.staged_sha256 = info.sha256;
        current.state = 'staged';
        current.next_run_at = Date.now();
      });
      this.wake();
      return { task_id: t.id, accepted: true, replayed: false };
    } finally {
      this.receiving.delete(taskId);
      this.receiverAborts.delete(taskId);
    }
  }
  async recover(): Promise<void> {
    await this.files.init();
    await this.files.recoverParts();
    for (const t of this.store.all()) {
      if (terminal(t)) {
        await this.files.remove(t.id);
        continue;
      }
      if (
        t.submitted_at &&
        ['submitting', 'reconciling', 'verifying', 'downloading'].includes(t.state)
      ) {
        t.state = 'reconciling';
        t.next_run_at = Date.now();
        this.store.save(t);
        await this.files.remove(t.id);
        continue;
      }
      if (
        ['awaiting_upload', 'downloading', 'staged', 'queued'].includes(t.state) &&
        (await this.files.exists(t.id))
      ) {
        try {
          const info = await validateFile(
            this.files.path(t.id),
            t.book.formats[0] as Format,
            this.config.maxFileBytes,
          );
          if (t.expected_sha256 && info.sha256 !== t.expected_sha256)
            fail('HASH_MISMATCH', '恢复文件校验失败');
          if (t.kind === 'upload' && info.size_bytes !== t.size_bytes)
            fail('LENGTH_MISMATCH', '恢复文件长度不符');
          t.staged_sha256 = info.sha256;
          t.state = 'staged';
        } catch {
          await this.files.remove(t.id);
          t.state = 'blocked';
          t.error = {
            code: 'INPUT_INVALID',
            message: '恢复时暂存文件无效，请检查输入后重试',
            retryable: false,
          };
        }
        this.store.save(t);
      } else if (t.state === 'downloading' || t.state === 'verifying') {
        t.state = 'queued';
        t.next_run_at = Date.now();
        this.store.save(t);
      }
    }
    await this.cleanup();
  }
  private async cleanup(): Promise<void> {
    const before = Date.now() - this.config.retentionDays * 86400000;
    for (const t of this.store.all()) {
      if (terminal(t)) await this.files.remove(t.id);
      else if (t.submitted_at) await this.files.remove(t.id);
      else if (t.updated_at < before && !t.submitted_at) {
        this.store.mutate(t.id, (current) => {
          current.state = 'failed';
          current.error = {
            code: 'TASK_EXPIRED',
            message: '未完成任务已超过保留期',
            retryable: false,
          };
        });
        await this.files.remove(t.id);
      }
    }
    for (const key of this.store.prune(before)) await this.files.remove(key);
  }
  start(): void {
    this.wake();
  }
  wake(): void {
    if (this.stopping || this.running) return;
    if (this.timer) clearTimeout(this.timer);
    this.timer = setTimeout(() => {
      this.running = this.tick()
        .catch(() => {
          process.stderr.write('WORKER_ERROR: 后台任务调度失败\n');
        })
        .finally(() => {
          this.running = undefined;
          if (!this.stopping) this.wake();
        });
    }, 100);
    this.timer.unref();
  }
  async stop(): Promise<void> {
    this.stopping = true;
    if (this.timer) clearTimeout(this.timer);
    this.active?.abort.abort();
    for (const controller of this.receiverAborts.values()) controller.abort();
    await this.running;
  }
  async tick(): Promise<void> {
    if (Date.now() - this.cleanedAt > 60000) {
      this.cleanedAt = Date.now();
      await this.cleanup();
    }
    const t = this.store.due();
    if (!t) return;
    const abort = new AbortController();
    this.active = { id: t.id, abort };
    try {
      await this.step(t, abort.signal);
    } catch (error) {
      if (this.stopping || this.store.get(t.id).state === 'cancelled') return;
      const e = publicError(error);
      this.store.mutate(t.id, (current) => {
        current.error = e;
        if (
          current.submitted_at &&
          (e.retryable || e.code === 'UPLOAD_OUTCOME_UNKNOWN' || e.code === 'FORMAT_UNAVAILABLE')
        ) {
          current.state = 'reconciling';
          current.next_run_at = Date.now() + this.config.pollMs;
          if (Date.now() > (current.deadline || 0)) current.state = 'needs_review';
        } else current.state = 'blocked';
      });
    } finally {
      this.active = undefined;
      if (terminal(this.store.get(t.id))) await this.files.remove(t.id);
    }
  }
  private async matches(want: Book, signal: AbortSignal): Promise<Book[]> {
    const matches: Book[] = [];
    let cursor: string | undefined;
    const seen = new Set<string>();
    for (let page = 0; page < 20; page++) {
      const result = await this.cwa.search(want.title, cursor, signal);
      for (const b of result.books)
        if (sameBook(want, b) && !matches.some((m) => m.id === b.id && m.uuid === b.uuid))
          matches.push(b);
      if (matches.length > 50) fail('SEARCH_INCOMPLETE', '匹配数量过多，请缩小书名和作者');
      if (!result.next) return matches;
      if (seen.has(result.next)) fail('SEARCH_INCOMPLETE', '书库分页未前进');
      seen.add(result.next);
      cursor = result.next;
    }
    return fail('SEARCH_INCOMPLETE', '书库核对尚未穷尽分页，未进行提交');
  }
  async verifiedFile(
    book: Book,
    format: Format,
    signal?: AbortSignal,
  ): Promise<{ book: Book; key: string; info: FileInfo }> {
    const current = await this.cwa.book(book, signal);
    if (!sameBook(book, current))
      fail('BOOK_IDENTITY_CHANGED', 'CWA 元数据与任务身份不符，请重新搜索');
    const response = await this.cwa.open(current, format, signal);
    const raw = response.headers['content-length'];
    const size = typeof raw === 'string' && /^\d+$/.test(raw) ? Number(raw) : undefined;
    if (size && (size > this.config.maxFileBytes || !Number.isSafeInteger(size))) {
      response.body.destroy();
      fail('FILE_TOO_LARGE', 'CWA 文件超过大小上限');
    }
    const key = 'verify-' + id();
    try {
      const info = await this.files.save(key, response.body, format, size, undefined, signal);
      return { book: current, key, info };
    } catch (error) {
      await this.files.remove(key);
      throw error;
    } finally {
      response.body.destroy();
    }
  }
  private async verify(t: Task, book: Book, signal: AbortSignal): Promise<void> {
    this.store.mutate(t.id, (current) => {
      if (current.state !== 'cancelled') {
        current.state = 'verifying';
        current.cwa = book;
      }
    });
    const verified = await this.verifiedFile(book, t.required_format, signal);
    try {
      this.store.mutate(t.id, (current) => {
        if (current.state === 'cancelled') return;
        current.cwa = verified.book;
        current.verification = { ...verified.info, at: Date.now() };
        current.waiting_for = undefined;
        current.state = 'ready';
        current.error = undefined;
        current.choices = undefined;
      });
    } finally {
      await this.files.remove(verified.key);
    }
  }
  private async step(t: Task, signal: AbortSignal): Promise<void> {
    if (t.book.origin === 'cwa') {
      await this.verify(t, t.book, signal);
      return;
    }
    if (t.cwa) {
      await this.verify(t, t.cwa, signal);
      return;
    }
    const matches = await this.matches(t.book, signal);
    signal.throwIfAborted();
    if (matches.length > 1) {
      this.store.mutate(t.id, (current) => {
        if (current.state !== 'cancelled') {
          current.state = 'needs_review';
          current.choices = matches;
          current.error = {
            code: 'AMBIGUOUS_MATCH',
            message: '存在多个匹配，请选择 CWA 候选',
            retryable: false,
          };
        }
      });
      return;
    }
    if (matches[0]) {
      await this.verify(t, matches[0], signal);
      return;
    }
    if (t.submitted_at) {
      this.store.mutate(t.id, (current) => {
        if (current.state === 'cancelled') return;
        current.next_run_at = Date.now() + this.config.pollMs;
        current.state = Date.now() > (current.deadline || 0) ? 'needs_review' : 'reconciling';
        if (current.state === 'needs_review')
          current.error = {
            code: 'RECONCILIATION_REQUIRED',
            message: '未找到可验证书籍；检查 CWA 处理状态，再重试核对，系统不会重新提交',
            retryable: false,
          };
      });
      return;
    }
    this.writeAllowed();
    const pending = this.store
      .all()
      .find(
        (other) =>
          other.id !== t.id &&
          other.submitted_at &&
          !terminal(other) &&
          (sameBook(t.book, other.book) || sameBook(other.book, t.book)),
      );
    if (pending) {
      this.store.mutate(t.id, (current) => {
        if (current.state === 'cancelled') return;
        current.state = 'blocked';
        current.waiting_for = pending.id;
        current.error = {
          code: 'RELATED_SUBMISSION_PENDING',
          message: '同一书籍已有未确认提交，先处理 waiting_for 任务，再重试核对',
          retryable: true,
        };
      });
      return;
    }
    if (!t.staged_sha256) {
      if (t.kind === 'upload') fail('INPUT_MISSING', '暂存文件缺失，请重新上传');
      const sourceFormat = formatSchema.parse(t.book.formats[0]);
      this.store.mutate(t.id, (current) => {
        current.state = 'downloading';
      });
      const response = await this.source.open(t.book, signal);
      const raw = response.headers['content-length'];
      const size = typeof raw === 'string' && /^\d+$/.test(raw) ? Number(raw) : undefined;
      let info: FileInfo;
      try {
        info = await this.files.save(t.id, response.body, sourceFormat, size, undefined, signal);
      } finally {
        response.body.destroy();
      }
      signal.throwIfAborted();
      this.store.mutate(t.id, (current) => {
        if (current.state === 'cancelled') return;
        current.state = 'staged';
        current.staged_sha256 = info.sha256;
        current.size_bytes = info.size_bytes;
        current.next_run_at = Date.now();
      });
      return; // Recheck the library before the subsequent submission.
    }
    if (!(await this.files.exists(t.id))) fail('INPUT_MISSING', '完整暂存文件丢失');
    const token = await this.cwa.prepareUpload(signal);
    signal.throwIfAborted();
    this.store.mutate(t.id, (current) => {
      if (current.state === 'cancelled') fail('CANCELLED', '任务已取消');
      current.state = 'submitting';
      current.submitted_at = Date.now();
      current.deadline = Date.now() + this.config.confirmMs;
    });
    try {
      await this.cwa.upload(this.files.path(t.id), t.book.formats[0] as Format, token, signal);
    } catch (error) {
      this.store.mutate(t.id, (current) => {
        if (current.state !== 'cancelled') current.error = publicError(error);
      });
    }
    this.store.mutate(t.id, (current) => {
      if (current.state === 'cancelled') return;
      current.state = 'reconciling';
      current.next_run_at = Date.now() + this.config.pollMs;
    });
    await this.files.remove(t.id);
  }
}
