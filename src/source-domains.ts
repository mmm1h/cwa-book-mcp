import { readFile, rename, writeFile } from 'node:fs/promises';
import { join } from 'node:path';
import { z } from 'zod';
import type { Config } from './config.js';
import { AppError, fail } from './domain.js';
import { Http, jsonBody, publicHttps, status, textBody } from './http.js';

export const domainPolicyUrl =
  'https://raw.githubusercontent.com/heartleo/zlib/main/domain_policy.go';
const seeds = [
  'z-lib.gd',
  'z-lib.gl',
  'zlib.bz',
  'article.sk',
  'articles.sk',
  '1lib.sk',
  'z-lib.fm',
  'z-lib.sk',
  'z-library.ec',
  'zliba.ru',
].map((host) => 'https://' + host);
const day = 24 * 60 * 60 * 1000;
const domainList = z.object({
  success: z.union([z.literal(1), z.literal(true)]),
  domains: z
    .array(
      z.object({
        domain: z.string().max(253),
        contentAvailable: z.boolean(),
        isRedirector: z.boolean(),
      }),
    )
    .min(1)
    .max(64),
});
const cacheSchema = z.object({
  version: z.literal(1),
  updatedAt: z.number().int().nonnegative(),
  roots: z.array(z.string()).min(1).max(32),
  advertised: z.array(z.string()).max(64),
  preferred: z.string().optional(),
});
function origin(raw: string): string | undefined {
  try {
    const url = new URL(raw.includes('://') ? raw : 'https://' + raw);
    if (
      publicHttps(url) &&
      url.pathname === '/' &&
      !url.search &&
      !url.hash &&
      /^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$/.test(url.hostname)
    )
      return url.origin;
  } catch {
    /* Unusable entries never become authentication targets. */
  }
  return undefined;
}
export function parseDomainPolicy(text: string): string[] {
  const block = /var\s+allowedDomainSuffixes\s*=\s*\[\]string\s*\{([^}]+)\}/.exec(text)?.[1];
  if (!block || !/^\s*(?:"[a-z0-9.-]+"\s*,\s*)+$/.test(block))
    fail('UPSTREAM_INVALID', '域名维护源格式不兼容');
  const result = [...block.matchAll(/"([a-z0-9.-]+)"/g)].map((m) => origin(m[1]!));
  if (!result.length || result.length > 32 || result.some((o) => !o))
    fail('UPSTREAM_INVALID', '域名维护源包含无效入口');
  return [...new Set(result as string[])];
}
function waitFor<T>(work: Promise<T>, signal?: AbortSignal): Promise<T> {
  if (!signal) return work;
  return new Promise((resolve, reject) => {
    const abort = () => reject(new AppError('CANCELLED', '书源请求已取消', true));
    if (signal.aborted) return abort();
    signal.addEventListener('abort', abort, { once: true });
    void work.then(resolve, reject).finally(() => signal.removeEventListener('abort', abort));
  });
}

/** Credential-free discovery. Each operation retains its selected Http instance. */
export class SourceDomains {
  private roots = [...seeds];
  private advertised: string[] = [];
  private preferred?: string;
  private updatedAt = 0;
  private attemptedAt = 0;
  private loaded = false;
  private current?: Http;
  private selecting?: Promise<Http>;
  private readonly clients = new Map<string, Http>();
  private readonly failed = new Map<string, number>();
  private readonly stopped = new AbortController();
  constructor(private readonly config: Config) {
    if (config.sourceUrl) this.current = this.client(config.sourceUrl);
  }
  get origins(): string[] {
    return [...new Set([...this.roots, ...this.advertised])];
  }
  private client(url: string): Http {
    let http = this.clients.get(url);
    if (!http) {
      http = new Http(url, 'SOURCE', this.config.metadataMs, !this.config.sourceUrl);
      this.clients.set(url, http);
    }
    return http;
  }
  private async save(): Promise<void> {
    const path = join(this.config.stateDir, 'source-domains.json');
    try {
      await writeFile(
        path + '.part',
        JSON.stringify({
          version: 1,
          updatedAt: this.updatedAt,
          roots: this.roots,
          advertised: this.advertised,
          preferred: this.preferred,
        }),
        { mode: 0o600 },
      );
      await rename(path + '.part', path);
    } catch {
      /* A cache failure must not prevent use of a verified online endpoint. */
    }
  }
  private async prepare(signal: AbortSignal, force = false): Promise<void> {
    if (!this.loaded) {
      this.loaded = true;
      try {
        const text = await readFile(join(this.config.stateDir, 'source-domains.json'), 'utf8');
        if (text.length <= 65536) {
          const cache = cacheSchema.parse(JSON.parse(text));
          const roots = cache.roots.map(origin),
            advertised = cache.advertised.map(origin);
          if (roots.every(Boolean) && advertised.every(Boolean) && cache.updatedAt <= Date.now()) {
            this.roots = roots as string[];
            this.advertised = advertised as string[];
            this.preferred = this.origins.includes(cache.preferred || '')
              ? cache.preferred
              : undefined;
            this.updatedAt = cache.updatedAt;
          }
        }
      } catch {
        /* Start with the bundled bootstrap list if the cache is absent or invalid. */
      }
    }
    if (
      (!force && Date.now() - this.updatedAt < day) ||
      Date.now() - this.attemptedAt < 5 * 60 * 1000
    )
      return;
    this.attemptedAt = Date.now();
    const http = new Http(
      new URL(domainPolicyUrl).origin,
      'SOURCE_DOMAINS',
      this.config.metadataMs,
      true,
    );
    try {
      const response = await http.send(domainPolicyUrl, {
        signal,
        cookies: false,
        timeoutMs: Math.min(8000, this.config.metadataMs),
      });
      status(response, 'SOURCE_DOMAINS');
      const previousRoots = this.roots;
      this.roots = parseDomainPolicy(await textBody(response, 65536));
      this.advertised = this.advertised.filter(
        (url) => !previousRoots.includes(url) || this.roots.includes(url),
      );
      this.updatedAt = Date.now();
      if (!this.origins.includes(this.preferred || '')) this.preferred = undefined;
      await this.save();
    } catch {
      /* Last known candidates and bundled roots remain usable while the maintainer is offline. */
      this.roots = [...new Set([...seeds, ...this.roots])].slice(0, 32);
    } finally {
      await http.close();
    }
  }
  async get(signal?: AbortSignal): Promise<Http> {
    if (signal?.aborted || this.stopped.signal.aborted) fail('CANCELLED', '书源请求已取消', true);
    if (this.config.sourceUrl) return this.current!;
    if (this.selecting) return waitFor(this.selecting, signal);
    if (
      this.current &&
      (Date.now() - this.updatedAt < day || Date.now() - this.attemptedAt < 5 * 60 * 1000)
    )
      return this.current;
    if (!this.selecting) {
      this.current = undefined;
      this.selecting = this.select().finally(() => {
        this.selecting = undefined;
      });
    }
    return waitFor(this.selecting, signal);
  }
  private async select(): Promise<Http> {
    const signal = AbortSignal.any([
      this.stopped.signal,
      AbortSignal.timeout(this.config.metadataMs),
    ]);
    await this.prepare(signal);
    for (const [url, until] of this.failed) if (until <= Date.now()) this.failed.delete(url);
    for (let refresh = 0; refresh < 2 && !signal.aborted; refresh++) {
      const candidates = [
        ...new Set([
          this.preferred,
          ...seeds.filter((s) => this.roots.includes(s)),
          ...this.origins,
        ]),
      ].filter((o): o is string => !!o && (this.failed.get(o) || 0) <= Date.now());
      for (let i = 0; i < Math.min(candidates.length, 12) && !signal.aborted; i += 3) {
        const wave = new AbortController();
        const probeSignal = AbortSignal.any([signal, wave.signal]);
        try {
          const selected = await Promise.any(
            candidates.slice(i, i + 3).map(async (url) => {
              try {
                const http = this.client(url);
                const response = await http.send('/eapi/info/domains', {
                  signal: probeSignal,
                  cookies: false,
                  timeoutMs: Math.min(6000, this.config.metadataMs),
                });
                status(response, 'SOURCE');
                const result = domainList.safeParse(await jsonBody(response));
                if (!result.success) fail('UPSTREAM_INVALID', '书源域名探测未通过');
                return {
                  http,
                  advertised: result.data.domains
                    .filter((d) => d.contentAvailable && !d.isRedirector)
                    .map((d) => origin(d.domain))
                    .filter((o): o is string => !!o),
                };
              } catch (error) {
                if (!probeSignal.aborted) this.failed.set(url, Date.now() + 60000);
                throw error;
              }
            }),
          );
          this.current = selected.http;
          this.preferred = selected.http.base;
          this.advertised = [...new Set(selected.advertised)];
          await this.save();
          return selected.http;
        } catch {
          /* Probe another bounded batch, without sending any account credentials. */
        } finally {
          wave.abort();
        }
      }
      if (Date.now() - this.attemptedAt < 5 * 60 * 1000) break;
      await this.prepare(signal, true);
    }
    fail('SOURCE_UNAVAILABLE', '未找到可用书源入口；已尝试自动维护清单和缓存', true);
  }
  reject(http: Http): void {
    if (this.config.sourceUrl) return;
    this.failed.set(http.base, Date.now() + 5 * 60 * 1000);
    if (this.current === http) this.current = undefined;
  }
  async close(): Promise<void> {
    this.stopped.abort();
    await this.selecting?.catch(() => {});
    await Promise.all([...this.clients.values()].map((http) => http.close()));
  }
}
