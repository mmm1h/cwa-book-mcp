import Database from 'better-sqlite3';
import {
  chmodSync,
  closeSync,
  fsyncSync,
  mkdirSync,
  openSync,
  readFileSync,
  writeFileSync,
} from 'node:fs';
import { randomBytes } from 'node:crypto';
import { join } from 'node:path';
import { digest, fail, id, type Task } from './domain.js';

export class Store {
  readonly db!: Database.Database;
  private owner: Database.Database;
  readonly key: Buffer;
  constructor(
    readonly dir: string,
    identity: string,
  ) {
    mkdirSync(dir, { recursive: true, mode: 0o700 });
    if (process.platform !== 'win32') chmodSync(dir, 0o700);
    try {
      this.owner = new Database(join(dir, 'owner.sqlite'), { timeout: 0 });
    } catch {
      fail(
        'SQLITE_UNAVAILABLE',
        'SQLite 无法打开；检查状态目录权限、磁盘和平台兼容性（Node ≥22.19；Linux glibc ≥2.34 或 musl），无需本机编译',
      );
    }
    try {
      // A separate rollback-journal database holds an OS lock for the process lifetime.
      // It does not hold task transactions open, and SIGKILL releases it immediately.
      this.owner.exec('BEGIN EXCLUSIVE');
    } catch {
      this.owner.close();
      fail(
        'STATE_IN_USE',
        '该状态目录已有进程使用；MCPHub 请共享进程，独立客户端请设置 INSTANCE_ID',
      );
    }
    try {
      this.db = new Database(join(dir, 'tasks.sqlite'));
      this.db.pragma('journal_mode = WAL');
      this.db.pragma('synchronous = FULL');
      this.db.exec(`CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY,value TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS tasks (id TEXT PRIMARY KEY,state TEXT NOT NULL,due INTEGER NOT NULL,updated INTEGER NOT NULL,data TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS tasks_due ON tasks(state,due);
        CREATE TABLE IF NOT EXISTS requests (key TEXT PRIMARY KEY,hash TEXT NOT NULL,task_id TEXT NOT NULL);
        CREATE TABLE IF NOT EXISTS cache (id TEXT PRIMARY KEY,kind TEXT NOT NULL,expires INTEGER NOT NULL,data TEXT NOT NULL);
        CREATE INDEX IF NOT EXISTS cache_expiry ON cache(expires);`);
      const existing = this.db.prepare('SELECT value FROM meta WHERE key = ?').get('identity') as
        { value: string } | undefined;
      const version = this.db
        .prepare('SELECT value FROM meta WHERE key = ?')
        .get('schema_version') as { value: string } | undefined;
      if (version && version.value !== '1')
        fail('STATE_VERSION_UNSUPPORTED', '状态版本不兼容；请使用对应应用版本或独立目录');
      if (existing && existing.value !== identity)
        fail('STATE_IDENTITY_MISMATCH', '状态目录属于其他 CWA/账号/实例，请使用独立 STATE_DIR');
      this.db.prepare('INSERT OR IGNORE INTO meta VALUES (?,?)').run('identity', identity);
      this.db.prepare('INSERT OR IGNORE INTO meta VALUES (?,?)').run('schema_version', '1');
      const keyPath = join(dir, 'ticket.key');
      try {
        this.key = readFileSync(keyPath);
        if (this.key.length !== 32) fail('KEY_INVALID', '票据密钥损坏；请从备份恢复，不会自动替换');
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== 'ENOENT') throw error;
        const hadKey = this.db.prepare("SELECT value FROM meta WHERE key = 'key_created'").get();
        if (hadKey) fail('KEY_INVALID', '票据密钥丢失；请从备份恢复');
        this.key = randomBytes(32);
        const fd = openSync(keyPath, 'wx', 0o600);
        try {
          writeFileSync(fd, this.key);
          fsyncSync(fd);
        } finally {
          closeSync(fd);
        }
      }
      this.db.prepare('INSERT OR IGNORE INTO meta VALUES (?,?)').run('key_created', '1');
    } catch (error) {
      this.db?.close();
      this.owner.close();
      throw error;
    }
  }
  close(): void {
    this.db.close();
    this.owner.close();
  }
  get(taskId: string): Task {
    const row = this.db.prepare('SELECT data FROM tasks WHERE id = ?').get(taskId) as
      { data: string } | undefined;
    return row
      ? (JSON.parse(row.data) as Task)
      : fail('TASK_NOT_FOUND', '任务不存在或已超过保留期');
  }
  save(task: Task): Task {
    task.updated_at = Date.now();
    this.db
      .prepare(
        'INSERT INTO tasks VALUES (?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET state=excluded.state,due=excluded.due,updated=excluded.updated,data=excluded.data',
      )
      .run(task.id, task.state, task.next_run_at, task.updated_at, JSON.stringify(task));
    return task;
  }
  mutate(taskId: string, fn: (task: Task) => void): Task {
    return this.db.transaction(() => {
      const t = this.get(taskId);
      fn(t);
      return this.save(t);
    })();
  }
  existing(requestId: string, input: unknown): Task | undefined {
    const row = this.db
      .prepare('SELECT hash,task_id FROM requests WHERE key = ?')
      .get(requestId) as { hash: string; task_id: string } | undefined;
    if (!row) return undefined;
    if (row.hash !== digest(input)) fail('IDEMPOTENCY_CONFLICT', 'request_id 已用于不同参数');
    return this.get(row.task_id);
  }
  create(requestId: string, input: unknown, factory: () => Task): Task {
    return this.db.transaction(() => {
      const existing = this.existing(requestId, input);
      if (existing) return existing;
      const count = (
        this.db
          .prepare(
            "SELECT count(*) AS n FROM tasks WHERE state NOT IN ('ready','failed','cancelled')",
          )
          .get() as { n: number }
      ).n;
      if (count >= 1000) fail('TASK_LIMIT', '未完成任务过多，请先处理或取消旧任务');
      const t = this.save(factory());
      this.db.prepare('INSERT INTO requests VALUES (?,?,?)').run(requestId, digest(input), t.id);
      return t;
    })();
  }
  all(): Task[] {
    return (this.db.prepare('SELECT data FROM tasks').all() as { data: string }[]).map(
      (r) => JSON.parse(r.data) as Task,
    );
  }
  due(): Task | undefined {
    const row = this.db
      .prepare(
        "SELECT data FROM tasks WHERE state IN ('queued','staged','reconciling','verifying') AND due <= ? ORDER BY due,updated LIMIT 1",
      )
      .get(Date.now()) as { data: string } | undefined;
    return row ? (JSON.parse(row.data) as Task) : undefined;
  }
  cache(kind: string, data: unknown, ttl = 30 * 60 * 1000): string {
    this.db.prepare('DELETE FROM cache WHERE expires <= ?').run(Date.now());
    const key = id();
    this.db
      .prepare('INSERT INTO cache VALUES (?,?,?,?)')
      .run(key, kind, Date.now() + ttl, JSON.stringify(data));
    this.db
      .prepare(
        'DELETE FROM cache WHERE id IN (SELECT id FROM cache ORDER BY expires DESC LIMIT -1 OFFSET 10000)',
      )
      .run();
    return key;
  }
  cached<T>(kind: string, key: string): T {
    const row = this.db
      .prepare('SELECT data FROM cache WHERE id = ? AND kind = ? AND expires > ?')
      .get(key, kind, Date.now()) as { data: string } | undefined;
    return row
      ? (JSON.parse(row.data) as T)
      : fail('REFERENCE_EXPIRED', '候选或游标已失效，请重新搜索');
  }
  prune(before: number): string[] {
    return this.db.transaction(() => {
      const ids = (
        this.db
          .prepare(
            "SELECT id FROM tasks WHERE state IN ('ready','failed','cancelled') AND updated < ?",
          )
          .all(before) as { id: string }[]
      ).map((r) => r.id);
      for (const key of ids) {
        this.db.prepare('DELETE FROM requests WHERE task_id = ?').run(key);
        this.db.prepare('DELETE FROM tasks WHERE id = ?').run(key);
      }
      this.db.prepare('DELETE FROM cache WHERE expires <= ?').run(Date.now());
      return ids;
    })();
  }
}
