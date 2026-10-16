import { createHash, randomUUID } from 'node:crypto';
import { z } from 'zod';

export const formatSchema = z.enum(['epub', 'pdf']);
export type Format = z.infer<typeof formatSchema>;
export const bookSchema = z.object({
  origin: z.enum(['cwa', 'zlibrary', 'upload']),
  id: z.string().max(128),
  uuid: z.string().max(128).optional(),
  hash: z.string().max(128).optional(),
  title: z.string().trim().min(1).max(1024),
  authors: z.array(z.string().max(256)).max(16).default([]),
  language: z.string().max(64).default('unknown'),
  formats: z.array(z.string().max(32)).max(16),
  acquisition_urls: z.partialRecord(formatSchema, z.string().url().max(8192)).optional(),
  isbn: z.string().max(64).optional(),
  size_bytes: z.number().int().positive().optional(),
});
export type Book = z.infer<typeof bookSchema>;
export function publicBook(book: Book): Omit<Book, 'acquisition_urls'> {
  const { acquisition_urls: _urls, ...metadata } = book;
  return metadata;
}
export interface Page {
  books: Book[];
  next?: string;
}
export const searchSchema = z
  .object({
    query: z.string().trim().min(1).max(512),
    author: z.string().trim().max(256).optional(),
    language: z.string().max(64).default('zh_preferred'),
    formats: z.array(formatSchema).max(2).default([]),
    limit: z.number().int().min(1).max(50).default(20),
    cursor: z.string().max(128).optional(),
  })
  .strict();
export type SearchInput = z.infer<typeof searchSchema>;
export interface Searcher {
  search(query: string, cursor?: string, signal?: AbortSignal): Promise<Page>;
}
export const taskStates = [
  'awaiting_upload',
  'queued',
  'downloading',
  'staged',
  'submitting',
  'reconciling',
  'verifying',
  'blocked',
  'needs_review',
  'ready',
  'download_available',
  'failed',
  'cancelled',
] as const;
export type TaskState = (typeof taskStates)[number];
export interface PublicError {
  code: string;
  message: string;
  retryable: boolean;
}
export class AppError extends Error {
  constructor(
    public code: string,
    message: string,
    public retryable = false,
  ) {
    super(message);
    this.name = 'AppError';
  }
  toJSON(): PublicError {
    return { code: this.code, message: this.message, retryable: this.retryable };
  }
}
export function fail(code: string, message: string, retryable = false): never {
  throw new AppError(code, message, retryable);
}
export function publicError(error: unknown): PublicError {
  if (error instanceof AppError) return error.toJSON();
  if (error instanceof z.ZodError)
    return {
      code: 'INVALID_ARGUMENT',
      message: '输入字段无效，请检查工具 schema',
      retryable: false,
    };
  return {
    code: 'INTERNAL_ERROR',
    message: '内部操作失败；检查本地权限、可用磁盘和运行环境',
    retryable: false,
  };
}
export function normalize(s: string): string {
  return s.normalize('NFC').trim().toLowerCase().replace(/\s+/gu, ' ');
}
export function language(s = ''): string {
  const n = normalize(s).replaceAll('_', '-');
  if (/^(zh(?:-.*)?|zho|chi|chinese(?: \(.*\))?|中文|简体中文|繁體中文|汉语|漢語)$/.test(n))
    return 'zh';
  if (['eng', 'english', 'en'].includes(n)) return 'en';
  return ['', 'und', 'unknown'].includes(n) ? 'unknown' : n;
}
export function sameBook(want: Book, got: Book): boolean {
  return (
    normalize(want.title) === normalize(got.title) &&
    (language(want.language) === 'unknown' || language(want.language) === language(got.language)) &&
    want.authors.every((a) => got.authors.some((b) => normalize(a) === normalize(b)))
  );
}
export function id(): string {
  return randomUUID();
}
function canonical(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonical);
  if (value && typeof value === 'object')
    return Object.fromEntries(
      Object.entries(value)
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([k, v]) => [k, canonical(v)]),
    );
  return value;
}
export function digest(value: unknown): string {
  return createHash('sha256')
    .update(JSON.stringify(canonical(value)))
    .digest('hex');
}
export interface Task {
  id: string;
  kind: 'acquire' | 'upload' | 'download';
  state: TaskState;
  required_format: Format;
  book: Book;
  created_at: number;
  updated_at: number;
  next_run_at: number;
  filename?: string;
  size_bytes?: number;
  expected_sha256?: string;
  staged_sha256?: string;
  submitted_at?: number;
  waiting_for?: string;
  deadline?: number;
  cwa?: Book;
  choices?: Book[];
  error?: PublicError;
  submission_error?: PublicError;
  verification?: { sha256: string; size_bytes: number; at: number };
  save_to_cwa?: boolean;
  source_requested_at?: number;
  source_download?: {
    url: string;
    format: Format;
    filename: string;
    available_until: number;
    expires_at?: number;
    verification?: { sha256: string; size_bytes: number; at: number };
  };
}
export const terminal = (t: Task): boolean =>
  ['ready', 'download_available', 'failed', 'cancelled'].includes(t.state);
