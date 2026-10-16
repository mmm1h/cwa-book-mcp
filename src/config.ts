import { readFileSync } from 'node:fs';
import { homedir } from 'node:os';
import { isAbsolute, join, resolve } from 'node:path';
import { digest, fail } from './domain.js';

export interface Config {
  cwaUrl: string;
  cwaUser: string;
  cwaPassword: string;
  writeEnabled: boolean;
  stateDir: string;
  identity: string;
  fileHost: string;
  filePort: number;
  publicUrl?: string;
  sourceUrl?: string;
  sourceEmail?: string;
  sourcePassword?: string;
  sourceUserId?: string;
  sourceUserKey?: string;
  sourceOrigins: string[];
  maxFileBytes: number;
  maxStagingBytes: number;
  transferMs: number;
  metadataMs: number;
  confirmMs: number;
  pollMs: number;
  ticketSeconds: number;
  sourceLinkSeconds: number;
  retentionDays: number;
}
export function sourceConfigured(config: Config): boolean {
  return !!(config.sourceUrl || config.sourceEmail || config.sourceUserId);
}
export function urlSetting(raw: string, name: string): string {
  try {
    const u = new URL(raw);
    if (!['http:', 'https:'].includes(u.protocol) || u.username || u.password || u.search || u.hash)
      throw Error();
    return u.href.replace(/\/+$/, '');
  } catch {
    return fail('CONFIG_INVALID', name + ' 必须为不含凭据、查询或片段的 HTTP(S) URL');
  }
}
export function loadConfig(env: NodeJS.ProcessEnv = process.env): Config {
  function secret(name: string): string | undefined {
    if (env[name] && env[name + '_FILE']) fail('CONFIG_INVALID', name + ' 与 _FILE 不可同时配置');
    if (env[name + '_FILE']) {
      try {
        const data = readFileSync(env[name + '_FILE']!);
        if (data.length > 65536) throw Error();
        return data.toString('utf8').replace(/[\r\n]+$/, '');
      } catch {
        return fail('CONFIG_INVALID', name + '_FILE 无法读取或过大');
      }
    }
    return env[name] || undefined;
  }
  function required(name: string): string {
    return secret(name) || fail('CONFIG_REQUIRED', '需要设置 ' + name);
  }
  function number(name: string, fallback: number, min: number, max: number): number {
    const raw = env[name];
    const value = raw === undefined ? fallback : Number(raw);
    if (
      (raw !== undefined && !/^\d+$/.test(raw)) ||
      !Number.isSafeInteger(value) ||
      value < min ||
      value > max
    )
      fail('CONFIG_INVALID', name + ' 超出允许范围');
    return value;
  }
  const cwaUrl = urlSetting(required('CWA_BASE_URL'), 'CWA_BASE_URL');
  const cwaUser = required('CWA_USERNAME'),
    cwaPassword = required('CWA_PASSWORD');
  const identity = digest({ cwaUrl, cwaUser, instance: env.INSTANCE_ID || 'default' });
  const root =
    process.platform === 'win32'
      ? env.LOCALAPPDATA || join(homedir(), 'AppData', 'Local')
      : process.platform === 'darwin'
        ? join(homedir(), 'Library', 'Application Support')
        : env.XDG_DATA_HOME && isAbsolute(env.XDG_DATA_HOME)
          ? env.XDG_DATA_HOME
          : join(homedir(), '.local', 'share');
  const stateDir = resolve(
    env.STATE_DIR || join(root, 'cwa-book-mcp', 'v1', identity.slice(0, 20)),
  );
  const fileHost = env.FILE_HOST || '127.0.0.1';
  const filePort = number('FILE_PORT', 0, 0, 65535);
  const publicUrl = env.PUBLIC_BASE_URL
    ? urlSetting(env.PUBLIC_BASE_URL, 'PUBLIC_BASE_URL')
    : undefined;
  if (!['127.0.0.1', '::1', 'localhost'].includes(fileHost) && !publicUrl)
    fail('CONFIG_INVALID', '非本机监听必须配置 PUBLIC_BASE_URL');
  if (publicUrl && filePort === 0) fail('CONFIG_INVALID', 'PUBLIC_BASE_URL 需要固定 FILE_PORT');
  const sourceUrl = env.ZLIBRARY_BASE_URL
    ? urlSetting(env.ZLIBRARY_BASE_URL, 'ZLIBRARY_BASE_URL')
    : undefined;
  const sourceEmail = secret('ZLIBRARY_EMAIL'),
    sourcePassword = secret('ZLIBRARY_PASSWORD');
  const sourceUserId = secret('ZLIBRARY_USER_ID'),
    sourceUserKey = secret('ZLIBRARY_USER_KEY');
  if (
    !!sourceEmail !== !!sourcePassword ||
    !!sourceUserId !== !!sourceUserKey ||
    (sourceEmail && sourceUserId)
  )
    fail('CONFIG_INVALID', '书源邮箱/密码与用户 ID/KEY 为两种完整且互斥的认证方式');
  const sourceOrigins = (env.ZLIBRARY_FILE_ORIGINS || '')
    .split(',')
    .map((s) => s.trim())
    .filter(Boolean)
    .map((s) => {
      const u = urlSetting(s, 'ZLIBRARY_FILE_ORIGINS');
      if (new URL(u).origin !== u) fail('CONFIG_INVALID', '下载允许列表只能填写精确 origin');
      return u;
    });
  if (env.CWA_WRITE_ENABLED && !['true', 'false'].includes(env.CWA_WRITE_ENABLED))
    fail('CONFIG_INVALID', 'CWA_WRITE_ENABLED 只接受 true/false');
  return {
    cwaUrl,
    cwaUser,
    cwaPassword,
    identity,
    stateDir,
    fileHost,
    filePort,
    publicUrl,
    writeEnabled: env.CWA_WRITE_ENABLED === 'true',
    sourceUrl,
    sourceEmail,
    sourcePassword,
    sourceUserId,
    sourceUserKey,
    sourceOrigins,
    maxFileBytes: number('MAX_FILE_BYTES', 2 * 1024 ** 3, 1024, 16 * 1024 ** 3),
    maxStagingBytes: number('MAX_STAGING_BYTES', 8 * 1024 ** 3, 1024, 128 * 1024 ** 3),
    transferMs: number('TRANSFER_TIMEOUT_SECONDS', 1800, 1, 86400) * 1000,
    metadataMs: number('METADATA_TIMEOUT_SECONDS', 30, 1, 300) * 1000,
    confirmMs: number('CONFIRM_TIMEOUT_SECONDS', 600, 1, 86400) * 1000,
    pollMs: number('POLL_INTERVAL_SECONDS', 5, 1, 300) * 1000,
    ticketSeconds: number('TICKET_TTL_SECONDS', 900, 1, 86400),
    sourceLinkSeconds: number('SOURCE_DOWNLOAD_TTL_SECONDS', 900, 1, 86400),
    retentionDays: number('RETENTION_DAYS', 30, 1, 365),
  };
}
