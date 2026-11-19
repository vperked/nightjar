import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react';
import { useToast } from '../components/Toasts';
import { usePoll, type Poll } from '../hooks/usePoll';
import { api } from './client';
import type { HostUpdatesSummary, UpdatesResponse } from './types';

const Ctx = createContext<Poll<UpdatesResponse> | null>(null);
const HostsCtx = createContext<Poll<{ hosts: HostUpdatesSummary[] }> | null>(null);

// UpdatesProvider polls /api/updates for the whole app (the nav badge and the Updates page):
// every few seconds while something is being checked or installed, otherwise twice a minute.
// Beside it, the agent servers' summary, which is the main Nightjar's cache of its own polls of
// them, so asking for it costs the agents nothing.
export function UpdatesProvider({ children, enabled }: { children: ReactNode; enabled: boolean }) {
  const [fast, setFast] = useState(false);
  const poll = usePoll(api.updates, fast ? 2000 : 30000, enabled);
  const hosts = usePoll(api.hostUpdatesSummary, 30000, enabled);
  const busy = !!poll.data && (poll.data.checking || poll.data.targets.some((t) => t.busy));
  useEffect(() => setFast(busy), [busy]);
  useRunToasts(poll.data);
  return (
    <Ctx.Provider value={poll}>
      <HostsCtx.Provider value={hosts}>{children}</HostsCtx.Provider>
    </Ctx.Provider>
  );
}

// useRunToasts announces updates that finish while the dashboard is open; on an agent server's,
// each is named with the server (machine), as its alerts are.
export function useRunToasts(data: UpdatesResponse | undefined, machine?: string) {
  const notify = useToast();
  const last = useRef<Map<string, number>>();
  useEffect(() => {
    if (!data) return;
    const now = new Map(data.targets.map((t) => [t.id, t.runs?.[0]?.at ?? 0]));
    const before = last.current;
    last.current = now;
    if (!before) return;
    const on = machine ? `${machine}: ` : '';
    for (const t of data.targets) {
      const run = t.runs?.[0];
      if (!run || run.at === before.get(t.id)) continue;
      const change = run.from && run.to && run.from !== run.to ? `${run.from} → ${run.to}` : undefined;
      if (run.ok) notify({ tone: 'ok', title: `${on}${t.name} updated`, body: change ?? run.detail });
      else notify({ tone: 'error', title: `${on}${t.name} update failed`, body: run.detail });
    }
  }, [data, notify, machine]);
}

export function useUpdates(): Poll<UpdatesResponse> {
  const v = useContext(Ctx);
  if (!v) throw new Error('useUpdates outside UpdatesProvider');
  return v;
}

// useHostUpdates is the agent servers' updates summary: an empty list until it answers, and when
// it cannot (a Nightjar too old for it), so pages simply offer no servers.
export function useHostUpdates(): { hosts: HostUpdatesSummary[]; loaded: boolean; refresh: () => void } {
  const v = useContext(HostsCtx);
  if (!v) throw new Error('useHostUpdates outside UpdatesProvider');
  const hosts = v.data?.hosts;
  return { hosts: Array.isArray(hosts) ? hosts : [], loaded: v.data !== undefined || v.error !== undefined, refresh: v.refresh };
}

// useWaitingUpdates counts the updates waiting to be installed, here and on the agent servers,
// for the Apps tab's badge and the overview. at is where to look first: this machine when it has
// some, else the first server that does.
export function useWaitingUpdates(): { total: number; here: number; at: string } {
  const { data } = useUpdates();
  const { hosts } = useHostUpdates();
  const here = data?.targets.filter((t) => t.available && !t.paused).length ?? 0;
  const first = hosts.find((h) => h.available > 0);
  return { total: here + hosts.reduce((n, h) => n + (h.available > 0 ? h.available : 0), 0), here, at: here || !first ? '/updates' : `/updates?host=${encodeURIComponent(first.id)}` };
}
