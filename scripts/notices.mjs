import { readFile, readdir, writeFile } from 'node:fs/promises';
import { join, dirname, parse } from 'node:path';
import { fileURLToPath } from 'node:url';
const root = fileURLToPath(new URL('../', import.meta.url));
const visited = new Set();
const blocks = [
  'Runtime dependency licenses for cwa-book-mcp. Generated from the installed locked dependency tree.\n' +
    'Z-Library EAPI/RPC compatibility was reimplemented with reference to heartleo/zlib (MIT); see heartleo-zlib-LICENSE.txt.',
];
async function locate(name, from) {
  for (let dir = from; dir !== parse(dir).root; dir = dirname(dir)) {
    const path = join(dir, 'node_modules', name);
    try {
      await readFile(join(path, 'package.json'));
      return path;
    } catch {
      /* continue to parent */
    }
  }
  throw Error('Dependency not installed: ' + name);
}
async function visit(name, from) {
  const dir = await locate(name, from),
    pkg = JSON.parse(await readFile(join(dir, 'package.json'), 'utf8'));
  const key = pkg.name + '@' + pkg.version;
  if (visited.has(key)) return;
  visited.add(key);
  const names = (await readdir(dir)).filter((n) => /^(licen[sc]e|copying|notice)(\.|$)/i.test(n));
  const texts = await Promise.all(names.map((n) => readFile(join(dir, n), 'utf8')));
  blocks.push(
    '\n===== ' +
      key +
      ' (' +
      pkg.license +
      ') =====\n' +
      (texts.join('\n') || 'See upstream package for license text.'),
  );
  for (const dep of Object.keys(pkg.dependencies || {}).sort()) await visit(dep, dir);
}
const pkg = JSON.parse(await readFile(join(root, 'package.json'), 'utf8'));
for (const name of Object.keys(pkg.dependencies).sort()) await visit(name, root);
await writeFile(join(root, 'third_party', 'NOTICE.txt'), blocks.join('\n') + '\n');
console.log('Collected ' + visited.size + ' runtime dependency notices');
