import { McpServer } from '@modelcontextprotocol/server';
import { z } from 'zod';
import { publicError, searchSchema } from './domain.js';
import { acquireSchema, downloadBookSchema, uploadSchema } from './tasks.js';
import { downloadSchema } from './gateway.js';
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
        'start 用 request_id、candidate_id、required_format 创建持久任务。retry 只核对已提交任务，不重新上传。resolve 用 task_id/candidate_id 明确选择 CWA 匹配，提交后的元数据差异可通过重新搜索再选择解决，语言仍须一致。cancel 不删除 CWA 记录。',
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
    'download_book',
    {
      description:
        '从显式选择的 Z 候选下载 EPUB/PDF 原文件并在 MCP 内校验。start 需要 request_id、candidate_id、required_format，默认 save_to_cwa:true，将同一文件自动交给 CWA；仅下载用 false。get_task.download.status 为 available 后可用 get_download_url 获取无需账号认证的书源链接，由 Agent 的 HTTP 工具保存并读取。collection 独立报告入库结果。collect 用 task_id 将未过期缓存补入 CWA；retry/cancel 使用 task_id。取票不明或链接过期不自动重新消耗额度。不交付 CWA 转换后的文件。',
      inputSchema: downloadBookSchema,
      outputSchema: output,
      annotations: {
        readOnlyHint: false,
        destructiveHint: false,
        idempotentHint: true,
        openWorldHint: true,
      },
    },
    (input) => run(() => app.tasks.download(input)),
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
        '查询持久任务状态。ready 只表示 CWA 目标格式已验证；download_book 的 download.status:available 独立表示原文件已校验，可取链接，collection 报告入库进度。都不表示 Agent 已保存文件。needs_review 时选择 choices，已提交任务也可重新搜索 CWA 后 resolve。blocked 且有 waiting_for 时先处理关联任务；修复后用 retry 核对。',
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
        'download_book 任务默认返回已在 MCP 内验证的书源原文件链接 delivery:source，无需客户端账号认证，可在 CWA 入库完成前获取；status:link_issued 仅表示链接提供，Agent 仍需 HTTP 保存并读取文件。重复取链接不重新取票，expires_at 为可识别的上游到期时间（未知为 null），available_until 同时受本地复用窗口与上游到期约束。其他任务默认返回需要 CWA Basic 认证的 OPDS 链接；delivery:gateway 显式取得限时签名 GET，需要可达的本程序文件入口。verification 仅记录服务端观察，无需客户端重复校验。',
      inputSchema: downloadSchema,
      outputSchema: output,
      annotations: { readOnlyHint: true, openWorldHint: true },
    },
    (input, ctx) =>
      run(() => app.gateway.downloadUrl(input.task_id, input.delivery, ctx.mcpReq.signal)),
  );
  return server;
}
