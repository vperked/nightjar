import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { useToast } from '../components/Toasts';
import { usePoll, type Poll } from '../hooks/usePoll';
import { api } from './client';
import type { UpdatesResponse } from './types';

const Ctx = createContext<Poll<UpdatesResponse> | null>(null);

// UpdatesProvider polls /api/updates for the whole app (the nav badge and the Updates page):
// every few seconds while something is being checked or installed, otherwise twice a minute.
export function UpdatesProvider({ children, enabled }: { children: ReactNode; enabled: boolean }) {
  const [fast, setFast] = useState(false);
  const poll = usePoll(api.updates, fast ? 2000 : 30000, enabled);
  const busy = !!poll.data && (poll.data.checking || poll.data.targets.some((t) => t.busy));
  useEffect(() => setFast(busy), [busy]);
  useRunToasts(poll.data);
  return <Ctx.Provider value={poll}>{children}</Ctx.Provider>;
}

// useRunToasts announces updates that finish while the dashboard is open.
function useRunToasts(data: UpdatesResponse | undefined) {
  const notify = useToast();
  const last = useRef<Map<string, number>>();
  useEffect(() => {
    if (!data) return;
    const now = new Map(data.targets.map((t) => [t.id, t.runs?.[0]?.at ?? 0]));
    const before = last.current;
    last.current = now;
    if (!before) return;
    for (const t of data.targets) {
      const run = t.runs?.[0];
      if (!run || run.at === before.get(t.id)) continue;
      const change = run.from && run.to && run.from !== run.to ? `${run.from} → ${run.to}` : undefined;
      if (run.ok) notify({ tone: 'ok', title: `${t.name} updated`, body: change ?? run.detail });
      else notify({ tone: 'error', title: `${t.name} update failed`, body: run.detail });
    }
  }, [data, notify]);
}

export function useUpdates(): Poll<UpdatesResponse> {
  const v = useContext(Ctx);
  if (!v) throw new Error('useUpdates outside UpdatesProvider');
  return v;
}
