import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { Readable } from 'node:stream';
import { pipeline } from 'node:stream/promises';
import { mkdtemp, rm, mkdir, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { randomUUID } from 'node:crypto';
import { monitorEventLoopDelay } from 'node:perf_hooks';
import { fetch } from 'undici';
import { createApp } from '../dist/app.js';
import { loadConfig } from '../dist/config.js';
import { uploadSchema, acquireSchema } from '../dist/tasks.js';
import { syntheticPdf } from './synthetic.mjs';

const size = Number(process.env.RESOURCE_FILE_BYTES || 256 * 1024 * 1024);
assert(Number.isSafeInteger(size) && size >= 1024);
const chunk = Buffer.alloc(64 * 1024, 120),
  prefix = syntheticPdf().subarray(0, -6),
  suffix = Buffer.from('\n%%EOF\n');
function stream(bytes) {
  return Readable.from(
    (async function* () {
      yield prefix;
      yield Buffer.from('%');
      let left = bytes - prefix.length - 1 - suffix.length;
      while (left > 0) {
        const n = Math.min(left, chunk.length);
        yield chunk.subarray(0, n);
        left -= n;
      }
      yield suffix;
    })(),
  );
}
let visible = false,
  servedSize = size,
  uploads = 0;
const uuid = randomUUID();
const fake = createServer((req, res) => {
  const path = new URL(req.url, 'http://localhost').pathname;
  if (path === '/opds/search') {
    res.end(
      '<feed xmlns="http://www.w3.org/2005/Atom"><title>synthetic</title>' +
        (visible
          ? '<entry><id>urn:uuid:' +
            uuid +
            '</id><title>CWA Synthetic PDF Compatibility</title><link rel="http://opds-spec.org/acquisition" href="/opds/download/1/pdf/"/></entry>'
          : '') +
        '</feed>',
    );
  } else if (path.startsWith('/ajax/book/')) {
    res.setHeader('content-type', 'application/json');
    res.end(
      JSON.stringify({
        application_id: 1,
        uuid,
        title: 'CWA Synthetic PDF Compatibility',
        authors: [],
        languages: [],
        formats: ['PDF'],
      }),
    );
  } else if (path.startsWith('/opds/download/')) {
    res.writeHead(200, { 'content-type': 'application/pdf', 'content-length': servedSize });
    void pipeline(stream(servedSize), res).catch(() => res.destroy());
  } else if (path === '/me') {
    res.end('<input name="csrf_token" value="synthetic-token"><input name="password">');
  } else if (path === '/upload') {
    void (async () => {
      for await (const _chunk of req) {
        /* Deliberately drain with backpressure. */
      }
      uploads++;
      visible = true;
      res.setHeader('content-type', 'application/json');
      res.end('{"location":"/book/1"}');
    })().catch(() => res.destroy());
  } else {
    res.writeHead(404);
    res.end();
  }
});
await new Promise((resolve) => fake.listen(0, '127.0.0.1', resolve));
const temp = await mkdtemp(join(tmpdir(), 'cwa-resources-'));
const config = loadConfig({
  CWA_BASE_URL: 'http://127.0.0.1:' + fake.address().port,
  CWA_USERNAME: 'synthetic',
  CWA_PASSWORD: 'synthetic',
  CWA_WRITE_ENABLED: 'true',
  STATE_DIR: temp,
  MAX_FILE_BYTES: String(size + 1024),
  MAX_STAGING_BYTES: String(size * 4 + 32 * 1024 * 1024),
});
config.pollMs = 25;
const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const eventLoop = monitorEventLoopDelay({ resolution: 20 });
eventLoop.enable();
let peak = process.memoryUsage().rss;
const sample = setInterval(() => {
  peak = Math.max(peak, process.memoryUsage().rss);
}, 20);
const start = performance.now(),
  app = await createApp(config);
async function ready(taskId) {
  const deadline = Date.now() + config.transferMs;
  for (;;) {
    const t = app.store.get(taskId);
    if (t.state === 'ready') return t;
    assert(!['blocked', 'needs_review', 'failed'].includes(t.state), JSON.stringify(t.error));
    assert(Date.now() < deadline, 'task timed out');
    await delay(25);
  }
}
try {
  const task = app.tasks.upload(
    uploadSchema.parse({
      request_id: 'large',
      title: 'CWA Synthetic PDF Compatibility',
      filename: 'synthetic.pdf',
      size_bytes: size,
      required_format: 'pdf',
    }),
  );
  await app.tasks.receive(task.id, stream(size), size);
  const verified = await ready(task.id);
  assert.equal(verified.verification.size_bytes, size);
  assert.equal(uploads, 1);
  const link = await app.gateway.downloadUrl(task.id, 'gateway');
  const response = await fetch(link.url);
  assert.equal(response.status, 200);
  let received = 0;
  for await (const bytes of response.body) received += bytes.length;
  assert.equal(received, size);
  const largeMs = performance.now() - start;
  servedSize = 64 * 1024;
  const candidate = app.catalog.expose(verified.cwa);
  for (let n = 0; n < 50; n++) {
    const t = app.tasks.acquire(
      acquireSchema.parse({
        request_id: 'sequential-' + n,
        candidate_id: candidate.candidate_id,
        required_format: 'pdf',
      }),
    );
    await ready(t.id);
  }
  for (let n = 0; n < 1000; n++) app.store.cache('book', verified.cwa);
  globalThis.gc?.();
  await delay(100);
  const report = {
    node: process.version,
    platform: process.platform,
    arch: process.arch,
    file_bytes: size,
    large_workflow_ms: Math.round(largeMs),
    sequential_tasks: 50,
    cached_records: 1000,
    peak_rss_bytes: peak,
    final_rss_bytes: process.memoryUsage().rss,
    event_loop_p99_ms: Number(eventLoop.percentile(99) / 1e6).toFixed(2),
    event_loop_max_ms: Number(eventLoop.max / 1e6).toFixed(2),
    active_resources: process.getActiveResourcesInfo(),
    uploads,
    verified: true,
  };
  await mkdir('.work', { recursive: true });
  await writeFile('.work/resources-current.json', JSON.stringify(report, null, 2) + '\n');
  console.log(JSON.stringify(report));
} finally {
  clearInterval(sample);
  eventLoop.disable();
  await app.close();
  fake.closeAllConnections();
  await new Promise((resolve) => fake.close(resolve));
  await rm(temp, { recursive: true, force: true });
}
