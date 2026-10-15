import { join } from 'node:path';
import { Store } from './store.js';
import { Files } from './files.js';
import { Cwa } from './cwa.js';
import { Source } from './source.js';
import { Catalog } from './catalog.js';
import { Tasks } from './tasks.js';
import { Gateway } from './gateway.js';
import type { Config } from './config.js';

export async function createApp(config: Config) {
  const store = new Store(config.stateDir, config.identity);
  const files = new Files(
    join(config.stateDir, 'staging'),
    config.maxFileBytes,
    config.maxStagingBytes,
  );
  const cwa = new Cwa(config),
    source = new Source(config);
  const catalog = new Catalog(store, cwa, source.configured ? source : undefined);
  const tasks = new Tasks(config, store, files, cwa, source, catalog);
  const gateway = new Gateway(config, tasks);
  let closed = false;
  const close = async () => {
    if (closed) return;
    closed = true;
    await tasks.stop();
    await gateway.close();
    await Promise.allSettled([cwa.close(), source.close()]);
    store.close();
  };
  try {
    await tasks.recover();
    await gateway.listen();
    tasks.start();
    return { config, store, files, cwa, source, catalog, tasks, gateway, close };
  } catch (error) {
    await close();
    throw error;
  }
}
export type App = Awaited<ReturnType<typeof createApp>>;
