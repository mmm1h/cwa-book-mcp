import assert from 'node:assert/strict';
import { mkdtemp, readFile, rm } from 'node:fs/promises';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { Readable } from 'node:stream';
import { loadConfig } from '../dist/config.js';
import { createApp } from '../dist/app.js';
import { uploadSchema } from '../dist/tasks.js';
import { fetch } from 'undici';
import { syntheticPdf } from './synthetic.mjs';

assert.equal(process.env.CWA_INTEGRATION, '1', 'Explicit isolated CWA opt-in required');
const dir = await mkdtemp(join(tmpdir(), 'cwa-original-'));
const config = loadConfig({
  ...process.env,
  STATE_DIR: dir,
  CWA_WRITE_ENABLED: 'true',
  CONFIRM_TIMEOUT_SECONDS: '420',
  POLL_INTERVAL_SECONDS: '2',
});
const app = await createApp(config);
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
try {
  const deadline = Date.now() + 150000;
  for (;;) {
    try {
      await app.cwa.search('readiness');
      break;
    } catch (error) {
      if (Date.now() > deadline) throw error;
      await delay(1000);
    }
  }
  for (const format of ['epub', 'pdf']) {
    const data =
      format === 'epub'
        ? await readFile(new URL('../testdata/synthetic.epub', import.meta.url))
        : syntheticPdf();
    const title = format === 'epub' ? '合成测试书' : 'CWA Synthetic PDF Compatibility';
    const task = app.tasks.upload(
      uploadSchema.parse({
        request_id: format,
        title,
        filename: 'synthetic.' + format,
        size_bytes: data.length,
        required_format: 'epub',
      }),
    );
    await app.tasks.receive(task.id, Readable.from(data), data.length);
    const until = Date.now() + 450000;
    let result;
    for (;;) {
      result = app.store.get(task.id);
      if (
        ['ready', 'blocked', 'needs_review', 'failed'].includes(result.state) ||
        Date.now() > until
      )
        break;
      await delay(500);
    }
    assert.equal(result.state, 'ready', JSON.stringify(result.error));
    assert(result.submitted_at, 'Isolated acceptance must exercise actual ingestion');
    const link = await app.gateway.downloadUrl(task.id, 'gateway');
    const response = await fetch(link.url);
    assert.equal(response.status, 200);
    let bytes = 0;
    for await (const chunk of response.body) bytes += chunk.length;
    assert.equal(bytes, link.size_bytes);
    console.log(
      JSON.stringify({
        input: format,
        output: 'epub',
        state: result.state,
        cwa_id: result.cwa.id,
        bytes,
        sha256: link.sha256,
      }),
    );
  }
} finally {
  await app.close();
  await rm(dir, { recursive: true, force: true });
}
