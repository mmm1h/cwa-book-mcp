import {
  digest,
  language,
  normalize,
  publicError,
  publicBook,
  type Book,
  type Searcher,
  type SearchInput,
} from './domain.js';
import { fail } from './domain.js';
import { Store } from './store.js';

interface Cursor {
  hash: string;
  tier: number;
  next?: string;
  pending: Book[];
  selected: boolean;
  searched: string[];
  warnings: string[];
}
export class Catalog {
  constructor(
    readonly store: Store,
    readonly cwa: Searcher,
    readonly source?: Searcher,
  ) {}
  candidate(key: string): Book {
    return this.store.cached<Book>('book', key);
  }
  expose(book: Book): Book & { candidate_id: string } {
    return { ...publicBook(book), candidate_id: this.store.cache('book', book) };
  }
  async search(input: SearchInput, signal?: AbortSignal): Promise<Record<string, unknown>> {
    const hash = digest({
      query: normalize(input.query),
      author: normalize(input.author || ''),
      language: language(input.language),
      formats: [...input.formats].sort(),
    });
    const state: Cursor = input.cursor
      ? this.store.cached<Cursor>('search', input.cursor)
      : { hash, tier: 0, pending: [], selected: false, searched: [], warnings: [] };
    if (state.hash !== hash) fail('CURSOR_INVALID', '游标不属于本次查询');
    const tiers =
      input.language === 'zh_preferred'
        ? ['cwa_zh', 'source_zh', 'cwa_other', 'source_other']
        : ['cwa', 'source'];
    let reads = 0;
    const result = (
      books: Book[],
      more: boolean,
      incomplete: boolean,
    ): Record<string, unknown> => ({
      candidates: books.map((b) => this.expose(b)),
      selected_tier: state.selected ? tiers[state.tier] : null,
      searched: state.searched,
      warnings: state.warnings,
      partial: incomplete || state.warnings.length > 0,
      next_cursor: more ? this.store.cache('search', state) : undefined,
    });
    for (; state.tier < tiers.length;) {
      const tier = tiers[state.tier]!;
      const provider = tier.startsWith('cwa') ? this.cwa : this.source;
      if (!state.searched.includes(tier)) state.searched.push(tier);
      if (!provider) {
        if (!state.warnings.includes('SOURCE_NOT_CONFIGURED'))
          state.warnings.push('SOURCE_NOT_CONFIGURED');
        state.tier++;
        continue;
      }
      if (state.pending.length) {
        const books = state.pending.splice(0, input.limit);
        return result(books, state.pending.length > 0 || !!state.next, false);
      }
      if (reads >= 3) return result([], true, true);
      try {
        const previous = state.next;
        const page = await provider.search(input.query, state.next, signal);
        reads++;
        if (page.next && page.next === previous) fail('SEARCH_INCOMPLETE', '上游分页未前进');
        state.next = page.next;
        state.pending = page.books.filter((b) => {
          const lang = language(b.language);
          return (
            (!input.author ||
              b.authors.some((a) => normalize(a).includes(normalize(input.author!)))) &&
            (!input.formats.length || input.formats.some((f) => b.formats.includes(f))) &&
            (input.language === 'any' ||
              input.language === 'zh_preferred' ||
              lang === language(input.language)) &&
            (!tier.endsWith('_zh') || lang === 'zh') &&
            (!tier.endsWith('_other') || lang !== 'zh')
          );
        });
        if (state.pending.length) {
          state.selected = true;
          continue;
        }
        if (state.next) continue;
        if (state.selected) return result([], false, false);
        state.tier++;
        state.next = undefined;
      } catch (error) {
        const code = publicError(error).code;
        return { ...result([], true, true), warnings: [...state.warnings, code] };
      }
    }
    return result([], false, false);
  }
}
