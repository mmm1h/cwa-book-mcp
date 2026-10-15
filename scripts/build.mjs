import { rm } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { spawnSync } from 'node:child_process';

// Only the repository's generated output directory is removed.
const output = fileURLToPath(new URL('../dist/', import.meta.url));
await rm(output, { recursive: true, force: true });
const compiler = fileURLToPath(new URL('../node_modules/typescript/bin/tsc', import.meta.url));
const result = spawnSync(process.execPath, [compiler, '-p', 'tsconfig.json'], {
  stdio: 'inherit',
  cwd: fileURLToPath(new URL('../', import.meta.url)),
});
process.exitCode = result.status ?? 1;
