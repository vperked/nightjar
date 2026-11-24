import { useCallback, useEffect, useRef, useState } from 'react';
import { UnauthorizedError } from '../api/client';

export interface Poll<T> {
  data: T | undefined;
  error: Error | undefined;
  unauthorized: boolean;
  loading: boolean;
  // stale is true while data is the last fetcher's (a range just changed and its reply is not in), so a
  // page that labels data with its range can show it loading rather than old numbers under a new label.
  stale: boolean;
  refresh: () => void;
}

// remembered is each poll's last answer, by key, so a page opened again shows what it showed last at
// once while a fresh answer is fetched (it is then replaced, so nothing old stays for long). Answers
// older than rememberFor are not shown. It is emptied on signing in or out: one account must never
// see another's data, even for a moment.
const remembered = new Map<unknown, { data: unknown; at: number }>();
const rememberFor = 10 * 60_000;

export function forgetPolls() {
  remembered.clear();
}

const recall = <T>(key: unknown): T | undefined => {
  const hit = remembered.get(key);
  return hit && Date.now() - hit.at < rememberFor ? (hit.data as T) : undefined;
};

// usePoll fetches immediately, then every intervalMs, pausing while the tab is hidden and fetching at
// once when it is shown again after missing a turn. A new fetcher (e.g. a different time range)
// refetches at once and discards replies to the old one, so fetchers must be stable: module-level
// functions or useCallback.
//
// key names what is fetched, for remembering it between visits; it defaults to the fetcher, which
// suits a module-level function such as api.units. A fetcher made in a component (useCallback) is a
// new function each visit, so give it a key that says what it asks for, such as `host:${id}`.
export function usePoll<T>(fetcher: () => Promise<T>, intervalMs: number, enabled = true, key: unknown = fetcher): Poll<T> {
  // data remembers which fetcher it came from, for stale.
  const [got, setGot] = useState<{ data: T; from: () => Promise<T> } | undefined>(() => {
    const data = recall<T>(key);
    return data === undefined ? undefined : { data, from: fetcher };
  });
  const [error, setError] = useState<Error>();
  const [unauthorized, setUnauthorized] = useState(false);
  const [loading, setLoading] = useState(() => recall(key) === undefined);
  const current = useRef(fetcher);
  const keyOf = useRef(key);
  const lastRun = useRef(0);
  const inFlight = useRef<() => Promise<T>>();
  const again = useRef(false);

  // run fetches unless a request for this fetcher is already out. A refresh asked for meanwhile (after
  // a change, say) goes out once that one settles, since its reply may predate the change.
  const run = useCallback(async (refresh = false) => {
    const f = current.current;
    if (inFlight.current === f) {
      if (refresh) again.current = true;
      return;
    }
    inFlight.current = f;
    again.current = false;
    lastRun.current = Date.now();
    const k = keyOf.current;
    try {
      const next = await f();
      // A reply that may predate a change waits for the one asked for after it.
      if (current.current !== f || again.current) return;
      remembered.set(k, { data: next, at: Date.now() });
      setGot({ data: next, from: f });
      setError(undefined);
      setUnauthorized(false);
    } catch (e) {
      if (current.current !== f || again.current) return;
      if (e instanceof UnauthorizedError) setUnauthorized(true);
      setError(e as Error);
    } finally {
      if (inFlight.current === f) inFlight.current = undefined;
      if (current.current === f) {
        if (again.current) void run(true);
        else setLoading(false);
      }
    }
  }, []);
  const refresh = useCallback(() => void run(true), [run]);

  useEffect(() => {
    const changed = current.current !== fetcher;
    current.current = fetcher;
    keyOf.current = key;
    // A new fetcher whose answer was seen before shows it now, rather than the last one's as stale.
    if (changed) {
      const data = recall<T>(key);
      if (data !== undefined) setGot({ data, from: fetcher });
    }
    if (!enabled) return;
    run();
    const id = setInterval(() => {
      if (document.visibilityState !== 'hidden') run();
    }, intervalMs);
    // Coming back to the tab after missing a turn fetches at once, rather than at the next tick.
    const shown = () => {
      if (document.visibilityState === 'visible' && Date.now() - lastRun.current >= intervalMs) run();
    };
    document.addEventListener('visibilitychange', shown);
    return () => {
      clearInterval(id);
      document.removeEventListener('visibilitychange', shown);
    };
    // key goes with the fetcher: a new key alone (same fetcher) asks for nothing new.
  }, [fetcher, run, intervalMs, enabled]);

  // Data is kept when the fetcher changes, so pages that ignore stale do not flicker.
  return { data: got?.data, error, unauthorized, loading, stale: got !== undefined && got.from !== fetcher, refresh };
}
