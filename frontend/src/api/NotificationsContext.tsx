import { createContext, useCallback, useContext, useMemo, type ReactNode } from 'react';
import { useToast } from '../components/Toasts';
import { muteSpan } from '../components/notifications';
import { usePoll } from '../hooks/usePoll';
import { api } from './client';
import type { Mute, NotificationSummary } from './types';

interface Notifications {
  summary: NotificationSummary | undefined;
  refresh: () => void;
  // mutedUntil is when a subject's mute ends (unix seconds), or undefined when it is not muted.
  mutedUntil: (subject: string) => number | undefined;
  mute: (subject: string, label: string, minutes: number) => Promise<void>;
}

// Outside the provider (a page rendered on its own) there is nothing to show: no summary, no mutes.
const Ctx = createContext<Notifications>({ summary: undefined, refresh: () => undefined, mutedUntil: () => undefined, mute: async () => undefined });

// NotificationsProvider polls the bell's summary (unread counts, mutes, quiet hours) for the whole
// app, and mutes and unmutes from anywhere with a toast to confirm.
export function NotificationsProvider({ children, enabled }: { children: ReactNode; enabled: boolean }) {
  const poll = usePoll(api.notificationSummary, 20000, enabled);
  const notify = useToast();
  const { data, refresh } = poll;

  const mutedUntil = useCallback(
    (subject: string) => {
      const m = data?.mutes.find((x: Mute) => x.subject === subject && x.until > Date.now() / 1000);
      return m?.until;
    },
    [data],
  );

  const mute = useCallback(
    async (subject: string, label: string, minutes: number) => {
      try {
        await api.mute(subject, label, minutes);
        notify(
          minutes > 0 ? { tone: 'ok', title: `${label} muted for ${muteSpan(minutes)}`, body: 'Its alerts still appear in notifications, but go nowhere.' } : { tone: 'ok', title: `${label} unmuted` },
        );
        refresh();
      } catch (e) {
        notify({ tone: 'error', title: 'Could not change the mute', body: (e as Error).message });
      }
    },
    [notify, refresh],
  );

  const value = useMemo(() => ({ summary: data, refresh, mutedUntil, mute }), [data, refresh, mutedUntil, mute]);
  return <Ctx.Provider value={value}>{children}</Ctx.Provider>;
}

export const useNotifications = () => useContext(Ctx);
