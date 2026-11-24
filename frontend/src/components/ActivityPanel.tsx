import { useCallback, useEffect, useState } from 'react';
import { api } from '../api/client';
import type { ActivityEntry } from '../api/types';
import { formatAgo } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';
import { SettingsGroup } from './SettingsGroup';

// The activity log: what was changed and by whom, on the dashboard, through Discord's buttons,
// or by Nightjar itself.

const when = (ts: number) => new Date(ts * 1000).toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: 'numeric', minute: '2-digit' });

// merge puts lists of entries together newest first, each entry once.
const merge = (...lists: ActivityEntry[][]) => [...new Map(lists.flat().map((a) => [a.id, a])).values()].sort((a, b) => b.id - a.id);

export function ActivityPanel() {
  const fetch = useCallback(() => api.activity(), []);
  const { data } = usePoll(fetch, 30000, true, 'activity');
  // kept is everything shown since older pages were loaded. The newest page keeps being polled, and
  // what slides off its end as new entries arrive stays here instead of falling into a gap before
  // the older pages.
  const [kept, setKept] = useState<ActivityEntry[]>([]);
  const [end, setEnd] = useState(false);
  const now = useNow(30000);
  const newest = data?.activity;
  useEffect(() => {
    if (newest) setKept((k) => (k.length ? merge(newest, k) : k));
  }, [newest]);
  const list = kept.length ? merge(newest ?? [], kept) : (newest ?? []);
  const more = async () => {
    const last = list[list.length - 1];
    if (!last) return;
    const r = await api.activity(last.id);
    setKept((k) => merge(list, k, r.activity));
    setEnd(r.activity.length < 100);
  };
  return (
    <SettingsGroup id="activity" name="Activity" aside={<span className="small ink-3">kept a year</span>}>
      {list.length === 0 ? (
        <p className="note" style={{ padding: '6px 0 14px' }}>
          Nothing yet. Changes made here, sign-ins, Discord buttons pressed and Nightjar's own repairs and removals are kept here.
        </p>
      ) : (
        <ul className="activity">
          {list.map((a) => (
            <li key={a.id} className={`who-${a.who === 'Nightjar' ? 'nightjar' : a.who.startsWith('Discord') ? 'discord' : 'person'}`}>
              <span className="activity-when num" title={when(a.ts)}>
                {formatAgo(a.ts, now)}
              </span>
              <span className="activity-who">{a.who}</span>
              <span className="min0 activity-what">
                {a.action}
                {a.detail && <span className="ink-3"> · {a.detail}</span>}
              </span>
            </li>
          ))}
        </ul>
      )}
      {list.length >= 100 && !end && (
        <button type="button" className="btn-ghost activity-more" onClick={more}>
          Older
        </button>
      )}
    </SettingsGroup>
  );
}
