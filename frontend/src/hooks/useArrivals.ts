import { useRef } from 'react';

// useArrivals tells which rows of a live list arrived while it was on screen, so they can slide in
// and glow for a moment (the .arrive class, motion.css). Rows there when the list first showed are
// not new, and neither is a row brought back by scrolling to older ones: with `at`, only a row newer
// than every row seen so far counts. A new scope (a filter changed) starts over, as a first showing.
//
// It reads and updates a ref while rendering. That is safe here: a second render with the same rows
// (as React's strict mode does) finds them known and returns the same answer.
export function useArrivals(rows: readonly { key: string; at?: number }[], scope = ''): ReadonlySet<string> {
  const seen = useRef<{ scope: string; known: Set<string>; newest: number; arrived: Set<string> } | null>(null);
  let s = seen.current;
  if (!s || s.scope !== scope) {
    s = { scope, known: new Set(rows.map((r) => r.key)), newest: Math.max(0, ...rows.map((r) => r.at ?? 0)), arrived: new Set() };
    seen.current = s;
    return s.arrived;
  }
  for (const r of rows) {
    if (s.known.has(r.key)) continue;
    s.known.add(r.key);
    if (r.at === undefined || r.at > s.newest) s.arrived.add(r.key);
  }
  s.newest = Math.max(s.newest, ...rows.map((r) => r.at ?? 0));
  // Rows gone from the list are forgotten as arrivals; a returning one is then a known row.
  if (s.arrived.size) {
    const here = new Set(rows.map((r) => r.key));
    for (const k of s.arrived) if (!here.has(k)) s.arrived.delete(k);
  }
  return s.arrived;
}
