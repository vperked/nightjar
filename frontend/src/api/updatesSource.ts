import { useEffect, useState } from 'react';
import { usePoll, type Poll } from '../hooks/usePoll';
import { api } from './client';
import type { UpdateSettings, UpdateSpec, UpdateTarget, UpdatesResponse } from './types';
import { useRunToasts, useUpdates } from './UpdatesContext';

// UpdatesSource is the machine whose updates the Updates page shows and acts on: this one, or a
// server through its agent (like FirewallTarget for the firewall). Every action goes through it,
// so a button on the page never reaches the wrong machine.
export interface UpdatesSource {
  // key is "local" or the server's id: it keys the page, and names the Update all kept in this browser.
  key: string;
  name: string;
  // host is the agent server's id; this machine has none.
  host?: string;
  load: () => Promise<UpdatesResponse>;
  save: (spec: UpdateSpec, isNew: boolean) => Promise<{ target: UpdateTarget }>;
  remove: (id: string) => Promise<void>;
  // check queues a check of every target, or of one.
  check: (id?: string) => Promise<{ queued: boolean }>;
  apply: (id: string) => Promise<{ queued: boolean }>;
  detect: () => Promise<{ added: UpdateTarget[] | null }>;
  // saveSettings is only an agent server's: this machine's are on the Settings page.
  saveSettings?: (s: UpdateSettings) => Promise<UpdatesResponse>;
}

export const localUpdates: UpdatesSource = {
  key: 'local',
  name: 'this machine',
  load: api.updates,
  save: api.saveUpdate,
  remove: api.deleteUpdate,
  check: api.checkUpdates,
  apply: api.applyUpdate,
  detect: api.detectUpdates,
};

// hostUpdates is a server's updates through its agent. Make one per server and keep it (useMemo):
// its load is what the page polls, which must stay the same function.
export const hostUpdates = (h: { id: string; name: string }): UpdatesSource => ({
  key: h.id,
  name: h.name,
  host: h.id,
  load: () => api.hostUpdates(h.id),
  save: (spec, isNew) => api.saveHostUpdate(h.id, spec, isNew),
  remove: (id) => api.deleteHostUpdate(h.id, id),
  check: (id) => api.checkHostUpdates(h.id, id),
  apply: (id) => api.applyHostUpdate(h.id, id),
  detect: () => api.detectHostUpdates(h.id),
  saveSettings: (s) => api.saveHostUpdateSettings(h.id, s),
});

// useUpdatesOf is a source's updates, kept current. This machine's come from the app-wide poll the
// nav badge already makes; a server's are polled here, every few seconds while something on it is
// checking or updating (its live log), otherwise twice a minute.
export function useUpdatesOf(source: UpdatesSource): Poll<UpdatesResponse> {
  const local = useUpdates();
  const remote = !!source.host;
  const [fast, setFast] = useState(false);
  const poll = usePoll(source.load, fast ? 5000 : 30000, remote);
  const busy = remote && !!poll.data && (poll.data.checking || poll.data.targets.some((t) => t.busy));
  useEffect(() => setFast(busy), [busy]);
  // This machine's finished updates are announced app-wide already.
  useRunToasts(remote ? poll.data : undefined, source.name);
  return remote ? poll : local;
}
