import { useEffect, useState } from 'react';

// Long lists show a page at a time: usePaged holds which page, and Pager moves between them.

export interface Paged<T> {
  items: T[]; // this page's items
  offset: number; // how many come before this page
  page: number;
  pages: number;
  total: number;
  size: number;
  setPage: (p: number) => void;
}

// usePaged slices items into pages of size. A change to reset (a filter, a tab) goes back to the
// first page; a list that shrinks under the page shown keeps to its last page.
export function usePaged<T>(all: T[], size: number, reset?: unknown): Paged<T> {
  const [page, setPage] = useState(0);
  useEffect(() => setPage(0), [reset]);
  const pages = Math.max(1, Math.ceil(all.length / size));
  const p = Math.min(page, pages - 1);
  return { items: all.slice(p * size, p * size + size), offset: p * size, page: p, pages, total: all.length, size, setPage };
}

// pageNumbers is which page buttons to show: the first, the last, and the pages either side of the
// current one, with a gap (null) where pages are left out. "1 … 4 5 6 … 12".
export function pageNumbers(page: number, pages: number): (number | null)[] {
  const keep = new Set([0, pages - 1, page - 1, page, page + 1].filter((n) => n >= 0 && n < pages));
  // A gap of one page is shown as that page rather than as "…".
  for (const n of [1, pages - 2]) if (n > 0 && n < pages - 1 && keep.has(n - 1) && keep.has(n + 1)) keep.add(n);
  const out: (number | null)[] = [];
  let last = -1;
  for (const n of [...keep].sort((a, b) => a - b)) {
    if (n - last > 1) out.push(null);
    out.push(n);
    last = n;
  }
  return out;
}

// Pager is the row under a long list: which items are shown, and buttons to the other pages. It
// shows nothing when everything fits on one page.
export function Pager<T>({ paged, label, noun = 'items' }: { paged: Paged<T>; label: string; noun?: string }) {
  const { page, pages, setPage, offset, total, items } = paged;
  if (pages <= 1) return null;
  return (
    <nav className="pager" aria-label={label}>
      <span className="pager-count num">
        {offset + 1}–{offset + items.length} of {total.toLocaleString()} {noun}
      </span>
      <span className="pager-pages">
        <button type="button" className="pager-btn" onClick={() => setPage(page - 1)} disabled={page === 0} aria-label="Previous page">
          ‹
        </button>
        {pageNumbers(page, pages).map((n, i) =>
          n === null ? (
            <span key={`gap${i}`} className="pager-gap" aria-hidden="true">
              …
            </span>
          ) : (
            <button key={n} type="button" className={`pager-btn num${n === page ? ' on' : ''}`} onClick={() => setPage(n)} aria-current={n === page ? 'page' : undefined} aria-label={`Page ${n + 1}`}>
              {n + 1}
            </button>
          ),
        )}
        <button type="button" className="pager-btn" onClick={() => setPage(page + 1)} disabled={page === pages - 1} aria-label="Next page">
          ›
        </button>
      </span>
    </nav>
  );
}
