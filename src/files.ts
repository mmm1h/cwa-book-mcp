import { createReadStream, createWriteStream } from 'node:fs';
import { mkdir, open, readdir, rename, rm, stat, statfs } from 'node:fs/promises';
import { join } from 'node:path';
import { createHash } from 'node:crypto';
import { Readable, Transform, Writable } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import yauzl, { type Entry } from 'yauzl';
import CRC32 from 'crc-32';
import { SaxesParser } from 'saxes';
import { AppError, fail, type Format } from './domain.js';

export interface FileInfo {
  size_bytes: number;
  sha256: string;
}
export async function hashStream(
  stream: Readable,
  max: number,
  expected?: number,
  signal?: AbortSignal,
  output?: Writable,
): Promise<FileInfo> {
  let count = 0;
  const hash = createHash('sha256');
  const meter = new Transform({
    transform(chunk: Buffer, _encoding, callback) {
      count += chunk.length;
      if (count > max || (expected !== undefined && count > expected))
        return callback(new AppError('FILE_TOO_LARGE', '文件超过声明或允许大小'));
      hash.update(chunk);
      callback(null, chunk);
    },
  });
  await pipeline(
    stream,
    meter,
    output ||
      new Writable({
        write(_chunk, _encoding, callback) {
          callback();
        },
      }),
    { signal },
  );
  if (expected !== undefined && count !== expected) fail('LENGTH_MISMATCH', '文件长度与声明不符');
  if (!count) fail('FILE_INVALID', '文件为空');
  return { size_bytes: count, sha256: hash.digest('hex') };
}
export async function validateFile(
  path: string,
  format: Format,
  max: number,
  signal?: AbortSignal,
): Promise<FileInfo> {
  signal?.throwIfAborted();
  const size = (await stat(path)).size;
  if (!size || size > max) fail('FILE_TOO_LARGE', '文件为空或超过大小上限');
  if (format === 'pdf') {
    const f = await open(path, 'r');
    try {
      const head = Buffer.alloc(8),
        tail = Buffer.alloc(Math.min(2048, size));
      await f.read(head, 0, head.length, 0);
      await f.read(tail, 0, tail.length, size - tail.length);
      if (!head.toString('ascii').startsWith('%PDF-') || !tail.includes(Buffer.from('%%EOF')))
        fail('FILE_INVALID', 'PDF 头部或结束标记无效');
    } finally {
      await f.close();
    }
  } else {
    await new Promise<void>((resolve, reject) => {
      yauzl.open(
        path,
        { lazyEntries: true, autoClose: false, validateEntrySizes: true, strictFileNames: true },
        (error, zip) => {
          if (error || !zip) return reject(new AppError('FILE_INVALID', 'EPUB ZIP 无效'));
          let expanded = 0,
            entries = 0,
            mimetype = false,
            container = false,
            rootfile = '';
          const names = new Set<string>();
          const zipEntries = new Map<string, Entry>();
          const abort = () => {
            zip.close();
            reject(new AppError('CANCELLED', '文件验证已取消'));
          };
          signal?.addEventListener('abort', abort, { once: true });
          const cleanup = () => signal?.removeEventListener('abort', abort);
          const bad = (e: unknown) => {
            cleanup();
            zip.close();
            reject(
              e instanceof AppError ? e : new AppError('FILE_INVALID', 'EPUB ZIP/CRC 校验失败'),
            );
          };
          zip.on('error', bad);
          zip.on('end', () => {
            if (!mimetype || !container || !rootfile || !names.has(rootfile))
              return bad(new AppError('FILE_INVALID', 'EPUB 缺少 mimetype、container 或包文件'));
            void checkPackage().then(() => {
              cleanup();
              zip.close();
              resolve();
            }, bad);
          });
          async function checkPackage(): Promise<void> {
            signal?.throwIfAborted();
            const entry = zipEntries.get(rootfile)!;
            if (entry.uncompressedSize > 4 * 1024 * 1024)
              fail('FILE_INVALID', 'EPUB 包描述文件过大');
            const stream = await new Promise<Readable>((ok, no) =>
              zip.openReadStream(entry, (e, s) => (e || !s ? no(e) : ok(s))),
            );
            const stop = () => stream.destroy(new AppError('CANCELLED', '验证已取消'));
            signal?.addEventListener('abort', stop, { once: true });
            const chunks: Buffer[] = [];
            try {
              for await (const chunk of stream) {
                signal?.throwIfAborted();
                chunks.push(Buffer.from(chunk));
              }
            } finally {
              signal?.removeEventListener('abort', stop);
              stream.destroy();
            }
            const parser = new SaxesParser({ xmlns: true });
            let depth = 0,
              isPackage = false;
            const items = new Set<string>(),
              spine: string[] = [];
            parser.on('doctype', () => fail('FILE_INVALID', 'EPUB 不允许 DTD'));
            parser.on('opentag', (tag) => {
              depth++;
              if (depth > 32) fail('FILE_INVALID', 'EPUB 包描述层级过深');
              if (depth === 1)
                isPackage = tag.local === 'package' && tag.uri === 'http://www.idpf.org/2007/opf';
              if (tag.uri === 'http://www.idpf.org/2007/opf') {
                if (tag.local === 'item' && tag.attributes.id?.value)
                  items.add(tag.attributes.id.value);
                if (tag.local === 'itemref') spine.push(tag.attributes.idref?.value || '');
              }
            });
            parser.on('closetag', () => {
              depth--;
            });
            parser.write(Buffer.concat(chunks).toString('utf8')).close();
            if (!isPackage || !items.size || !spine.length || spine.some((ref) => !items.has(ref)))
              fail('FILE_INVALID', 'EPUB 包描述或阅读顺序无效');
          }
          async function check(entry: Entry): Promise<void> {
            signal?.throwIfAborted();
            entries++;
            const name = entry.fileName;
            if (
              entries > 10000 ||
              name.startsWith('/') ||
              name.includes('\\') ||
              name.split('/').includes('..') ||
              name.includes(':') ||
              names.has(name) ||
              entry.isEncrypted() ||
              ((entry.externalFileAttributes >>> 16) & 0xf000) === 0xa000
            )
              fail('FILE_INVALID', 'EPUB 包含不安全或重复条目');
            names.add(name);
            zipEntries.set(name, entry);
            expanded += entry.uncompressedSize;
            if (expanded > Math.min(max * 4, 8 * 1024 ** 3))
              fail('FILE_TOO_LARGE', 'EPUB 展开大小超过上限');
            if (name.endsWith('/')) return;
            const stream = await new Promise<Readable>((ok, no) =>
              zip.openReadStream(entry, (e, s) => (e || !s ? no(e) : ok(s))),
            );
            const capture = name === 'mimetype' || name === 'META-INF/container.xml';
            if (capture && entry.uncompressedSize > 65536) {
              stream.destroy();
              fail('FILE_INVALID', 'EPUB 描述文件过大');
            }
            let crc = 0,
              length = 0;
            const chunks: Buffer[] = [];
            const stop = () => stream.destroy(new AppError('CANCELLED', '验证已取消'));
            signal?.addEventListener('abort', stop, { once: true });
            try {
              for await (const data of stream) {
                const chunk = Buffer.from(data);
                length += chunk.length;
                if (length > entry.uncompressedSize) fail('FILE_INVALID', 'ZIP 展开长度不符');
                crc = CRC32.buf(chunk, crc);
                if (capture) chunks.push(chunk);
              }
            } finally {
              signal?.removeEventListener('abort', stop);
              stream.destroy();
            }
            if (length !== entry.uncompressedSize || crc >>> 0 !== entry.crc32)
              fail('FILE_INVALID', 'ZIP 长度或 CRC 不符');
            if (name === 'mimetype') {
              mimetype =
                Buffer.concat(chunks).toString('utf8') === 'application/epub+zip' &&
                entry.compressionMethod === 0 &&
                entries === 1;
            }
            if (name === 'META-INF/container.xml') {
              const parser = new SaxesParser({ xmlns: true });
              parser.on('doctype', () => fail('FILE_INVALID', 'EPUB 不允许 DTD'));
              parser.on('opentag', (tag) => {
                if (
                  tag.local === 'container' &&
                  tag.uri === 'urn:oasis:names:tc:opendocument:xmlns:container'
                )
                  container = true;
                if (
                  tag.local === 'rootfile' &&
                  tag.uri === 'urn:oasis:names:tc:opendocument:xmlns:container'
                )
                  rootfile ||= tag.attributes['full-path']?.value || '';
              });
              parser.write(Buffer.concat(chunks).toString('utf8')).close();
            }
          }
          zip.on('entry', (entry) => {
            void check(entry).then(() => zip.readEntry(), bad);
          });
          zip.readEntry();
        },
      );
    });
  }
  return hashStream(createReadStream(path), max, size, signal);
}
export class Files {
  private reservations = new Map<string, number>();
  constructor(
    readonly dir: string,
    readonly maxFile: number,
    readonly maxTotal: number,
  ) {}
  async init(): Promise<void> {
    await mkdir(this.dir, { recursive: true, mode: 0o700 });
  }
  path(key: string): string {
    if (!/^[a-zA-Z0-9-]{1,100}$/.test(key)) fail('INVALID_ARGUMENT', '文件标识无效');
    return join(this.dir, key + '.book');
  }
  async reserve(key: string, bytes: number): Promise<() => void> {
    if (this.reservations.has(key)) fail('UPLOAD_IN_PROGRESS', '同一文件正在传输', true);
    // Reserve before awaits so simultaneous transfers cannot overcommit the logical quota.
    this.reservations.set(key, bytes);
    try {
      let used = 0;
      for (const file of await readdir(this.dir)) {
        try {
          used += (await stat(join(this.dir, file))).size;
        } catch {
          /* concurrent cleanup */
        }
      }
      const reserved = [...this.reservations.values()].reduce((a, b) => a + b, 0);
      const disk = await statfs(this.dir);
      if (used + reserved > this.maxTotal || disk.bavail * disk.bsize < reserved + 16 * 1024 * 1024)
        fail('STORAGE_FULL', '暂存配额或可用磁盘不足');
      return () => {
        this.reservations.delete(key);
      };
    } catch (error) {
      this.reservations.delete(key);
      throw error;
    }
  }
  async save(
    key: string,
    input: Readable,
    format: Format,
    size?: number,
    sha256?: string,
    signal?: AbortSignal,
  ): Promise<FileInfo> {
    const path = this.path(key),
      part = path + '.part';
    const release = await this.reserve(key, size ?? this.maxFile);
    try {
      const info = await hashStream(
        input,
        this.maxFile,
        size,
        signal,
        createWriteStream(part, { flags: 'wx', mode: 0o600 }),
      );
      if (sha256 && info.sha256 !== sha256) fail('HASH_MISMATCH', '文件 SHA-256 不符');
      await validateFile(part, format, this.maxFile, signal);
      const handle = await open(part, 'r+');
      try {
        await handle.sync();
      } finally {
        await handle.close();
      }
      await rename(part, path);
      return info;
    } finally {
      input.destroy();
      await rm(part, { force: true });
      release();
    }
  }
  async remove(key: string): Promise<void> {
    await rm(this.path(key), { force: true });
    await rm(this.path(key) + '.part', { force: true });
  }
  async exists(key: string): Promise<boolean> {
    try {
      await stat(this.path(key));
      return true;
    } catch {
      return false;
    }
  }
  async recoverParts(): Promise<void> {
    for (const name of await readdir(this.dir))
      if (/^[a-zA-Z0-9-]+\.book\.part$/.test(name) || /^verify-[a-zA-Z0-9-]+\.book$/.test(name))
        await rm(join(this.dir, name), { force: true });
  }
}
