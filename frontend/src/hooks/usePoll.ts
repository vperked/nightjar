import { useCallback, useEffect, useRef, useState } from 'react';
import { UnauthorizedError } from '../api/client';

export interface Poll<T> {
  data: T | undefined;
  error: Error | undefined;
  unauthorized: boolean;
  loading: boolean;
  refresh: () => void;
}

// usePoll fetches immediately, then every intervalMs, pausing while the tab is hidden.
// A new fetcher (e.g. a different time range) refetches at once and discards replies to the old one,
// so fetchers must be stable: module-level functions or useCallback.
export function usePoll<T>(fetcher: () => Promise<T>, intervalMs: number, enabled = true): Poll<T> {
  const [data, setData] = useState<T>();
  const [error, setError] = useState<Error>();
  const [unauthorized, setUnauthorized] = useState(false);
  const [loading, setLoading] = useState(true);
  const current = useRef(fetcher);
  const inFlight = useRef<() => Promise<T>>();

  const run = useCallback(async () => {
    const f = current.current;
    if (inFlight.current === f) return;
    inFlight.current = f;
    try {
      const next = await f();
      if (current.current !== f) return;
      setData(next);
      setError(undefined);
      setUnauthorized(false);
    } catch (e) {
      if (current.current !== f) return;
      if (e instanceof UnauthorizedError) setUnauthorized(true);
      setError(e as Error);
    } finally {
      if (inFlight.current === f) inFlight.current = undefined;
      if (current.current === f) setLoading(false);
    }
  }, []);

  useEffect(() => {
    current.current = fetcher;
    if (!enabled) return;
    run();
    const id = setInterval(() => {
      if (document.visibilityState !== 'hidden') run();
    }, intervalMs);
    return () => clearInterval(id);
  }, [fetcher, run, intervalMs, enabled]);

  return { data, error, unauthorized, loading, refresh: run };
}
