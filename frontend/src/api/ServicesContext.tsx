import { createContext, useContext, useEffect, useRef, type ReactNode } from 'react';
import { useToast } from '../components/Toasts';
import { usePoll, type Poll } from '../hooks/usePoll';
import { api } from './client';
import type { ServicesResponse } from './types';

const Ctx = createContext<Poll<ServicesResponse> | null>(null);

// ServicesProvider polls /api/services once for the whole app (nav pill, overview and services pages).
export function ServicesProvider({ children, enabled }: { children: ReactNode; enabled: boolean }) {
  const poll = usePoll(api.services, 10000, enabled);
  useStatusToasts(poll.data);
  return <Ctx.Provider value={poll}>{children}</Ctx.Provider>;
}

// useStatusToasts announces checks going down or recovering while the dashboard is open.
// The first answer only sets the baseline, so opening the page does not replay old outages.
function useStatusToasts(data: ServicesResponse | undefined) {
  const notify = useToast();
  const last = useRef<Map<string, string>>();
  useEffect(() => {
    if (!data) return;
    const now = new Map(data.services.map((s) => [s.name, s.status]));
    const before = last.current;
    last.current = now;
    if (!before) return;
    for (const s of data.services) {
      const was = before.get(s.name);
      if (s.status === 'down' && was === 'up') notify({ tone: 'error', title: `${s.name} is down`, body: s.detail });
      else if (s.status === 'up' && was === 'down') notify({ tone: 'ok', title: `${s.name} is back up`, body: s.detail });
    }
  }, [data, notify]);
}

export function useServices(): Poll<ServicesResponse> {
  const v = useContext(Ctx);
  if (!v) throw new Error('useServices outside ServicesProvider');
  return v;
}
