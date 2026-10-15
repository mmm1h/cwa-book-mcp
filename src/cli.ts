#!/usr/bin/env node
import { StdioServerTransport } from '@modelcontextprotocol/server/stdio';
import { loadConfig } from './config.js';
import { createApp } from './app.js';
import { createMcp } from './mcp.js';
import { Cwa } from './cwa.js';
import { fail, publicError } from './domain.js';
import { VERSION } from './version.js';

process.umask(0o077);
async function main(): Promise<void> {
  const [major, minor] = process.versions.node.split('.').map(Number);
  if (major! < 22 || (major === 22 && minor! < 19))
    fail('NODE_UNSUPPORTED', '需要 Node.js ≥22.19；推荐 Node.js 24 LTS');
  const args = process.argv.slice(2);
  if (args.length === 1 && args[0] === '--version') {
    process.stdout.write(VERSION + '\n');
    return;
  }
  if (args.length === 1 && ['--help', '-h'].includes(args[0]!)) {
    process.stdout.write(
      'cwa-book-mcp [diagnose | --help | --version]\n默认：stdio MCP + 同进程授权文件 HTTP。\n需要：CWA_BASE_URL、CWA_USERNAME、CWA_PASSWORD（凭据支持 _FILE）。\n可选：CWA_WRITE_ENABLED、STATE_DIR、INSTANCE_ID、FILE_HOST、FILE_PORT、PUBLIC_BASE_URL。\n书源：ZLIBRARY_BASE_URL + EMAIL/PASSWORD 或 USER_ID/USER_KEY（均带 ZLIBRARY_ 前缀）。\n详见包内 docs/configuration.md。\n',
    );
    return;
  }
  if (args.length > 1 || (args.length === 1 && args[0] !== 'diagnose'))
    fail('INVALID_ARGUMENT', '仅支持默认 stdio、diagnose、--help、--version');
  const config = loadConfig();
  if (args[0] === 'diagnose') {
    const cwa = new Cwa(config);
    try {
      await cwa.search('cwa-book-mcp-readiness-check');
      process.stdout.write(
        JSON.stringify({
          cwa_read: true,
          write_enabled: config.writeEnabled,
          source_configured: !!config.sourceUrl,
        }) + '\n',
      );
    } finally {
      await cwa.close();
    }
    return;
  }
  const app = await createApp(config);
  const server = createMcp(app);
  let shutting: Promise<void> | undefined;
  const shutdown = () => {
    shutting ||= Promise.resolve().then(async () => {
      await server.close();
      await app.close();
    });
    return shutting;
  };
  const signal = () => {
    void shutdown().catch(() => {
      process.exitCode = 1;
    });
  };
  process.once('SIGINT', signal);
  process.once('SIGTERM', signal);
  server.server.onclose = signal;
  server.server.onerror = () => {
    process.stderr.write('MCP_PROTOCOL_ERROR: 协议消息无效\n');
  };
  try {
    await server.connect(
      new StdioServerTransport(process.stdin, process.stdout, { maxBufferSize: 1024 * 1024 }),
    );
  } catch (error) {
    await shutdown();
    throw error;
  }
}
void main().catch((error) => {
  const e = publicError(error);
  process.stderr.write(e.code + ': ' + e.message + '\n');
  process.exitCode = 1;
});
