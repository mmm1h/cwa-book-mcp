import { McpServer } from '@modelcontextprotocol/server';
import { z } from 'zod';
import { publicError, searchSchema } from './domain.js';
import { acquireSchema, uploadSchema } from './tasks.js';
import type { App } from './app.js';
import { VERSION } from './version.js';

export function createMcp(app: App): McpServer {
  const server = new McpServer({ name: 'cwa-book-mcp', version: VERSION });
  const output = z.record(z.string(), z.unknown());
  const run = async (fn: () => Promise<Record<string, unknown>> | Record<string, unknown>) => {
    try {
      const value = await fn();
      return {
        content: [{ type: 'text' as const, text: JSON.stringify(value) }],
        structuredContent: value,
      };
    } catch (error) {
      const value = { error: publicError(error) };
      return {
        isError: true,
        content: [{ type: 'text' as const, text: JSON.stringify(value) }],
        structuredContent: value,
      };
    }
  };
  server.registerTool(
    'search_books',
    {
      description:
        '搜索书库与配置的书源。默认优先 CWA 中文、书源中文、CWA 其他、书源其他。partial 或 warnings 表示不能断言无结果；使用 next_cursor 继续。',
      inputSchema: searchSchema,
      outputSchema: output,
      annotations: { readOnlyHint: true, openWorldHint: true },
    },
    (input, ctx) => run(() => app.catalog.search(input, ctx.mcpReq.signal)),
  );
  server.registerTool(
    'acquire_book',
    {
      description:
        'start 用 request_id、candidate_id、required_format 创建持久任务。retry 只核对已提交任务，不重新上传。resolve 用 task_id/candidate_id 选择匹配。cancel 不删除 CWA 记录。用 get_task 查询结果。',
      inputSchema: acquireSchema,
      outputSchema: output,
      annotations: {
        readOnlyHint: false,
        destructiveHint: false,
        idempotentHint: true,
        openWorldHint: true,
      },
    },
    (input) => run(() => app.tasks.acquire(input)),
  );
  server.registerTool(
    'upload_book',
    {
      description:
        'prepare 用 request_id、title、filename、size_bytes、required_format 准备上传。文件真实元数据应与 title/authors/language 对应。按返回链接 PUT 完整文件；refresh_ticket 用 task_id 更新授权。禁止通过 MCP 传文件或任意服务端路径。',
      inputSchema: uploadSchema,
      outputSchema: output,
      annotations: {
        readOnlyHint: false,
        destructiveHint: false,
        idempotentHint: true,
        openWorldHint: false,
      },
    },
    (input) => run(() => app.gateway.upload(app.tasks.upload(input))),
  );
  server.registerTool(
    'get_task',
    {
      description:
        '查询持久任务状态。只有 ready 表示已从 CWA 验证目标格式文件；needs_review 时从 choices 中 resolve，或在修复后 retry 核对。',
      inputSchema: z.object({ task_id: z.string().uuid() }).strict(),
      outputSchema: output,
      annotations: { readOnlyHint: true, openWorldHint: false },
    },
    (input) => run(() => app.tasks.get(input.task_id)),
  );
  server.registerTool(
    'get_download_url',
    {
      description:
        '为 ready 任务重新核验 CWA 身份和文件，返回限时 GET 地址。仅从 CWA 下载；不支持 Range。',
      inputSchema: z.object({ task_id: z.string().uuid() }).strict(),
      outputSchema: output,
      annotations: { readOnlyHint: true, openWorldHint: true },
    },
    (input, ctx) => run(() => app.gateway.downloadUrl(input.task_id, ctx.mcpReq.signal)),
  );
  return server;
}
