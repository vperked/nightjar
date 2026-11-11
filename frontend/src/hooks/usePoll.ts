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

// usePoll fetches immediately, then every intervalMs, pausing while the tab is hidden.
// A new fetcher (e.g. a different time range) refetches at once and discards replies to the old one,
// so fetchers must be stable: module-level functions or useCallback.
export function usePoll<T>(fetcher: () => Promise<T>, intervalMs: number, enabled = true): Poll<T> {
  // data remembers which fetcher it came from, for stale.
  const [got, setGot] = useState<{ data: T; from: () => Promise<T> }>();
  const [error, setError] = useState<Error>();
  const [unauthorized, setUnauthorized] = useState(false);
  const [loading, setLoading] = useState(true);
  const current = useRef(fetcher);
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
    try {
      const next = await f();
      // A reply that may predate a change waits for the one asked for after it.
      if (current.current !== f || again.current) return;
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
    current.current = fetcher;
    if (!enabled) return;
    run();
    const id = setInterval(() => {
      if (document.visibilityState !== 'hidden') run();
    }, intervalMs);
    return () => clearInterval(id);
  }, [fetcher, run, intervalMs, enabled]);

  // Data is kept when the fetcher changes, so pages that ignore stale do not flicker.
  return { data: got?.data, error, unauthorized, loading, stale: got !== undefined && got.from !== fetcher, refresh };
}
