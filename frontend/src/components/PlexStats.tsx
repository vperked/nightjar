import { useCallback, useState } from 'react';
import { api } from '../api/client';
import type { PlexCount, PlexStats } from '../api/types';
import { plural } from '../format';
import { usePoll } from '../hooks/usePoll';
import { Section, Seg } from './ui';

// Plex over the last days: from Nightjar's record of every finished stream, and from Plex's own
// watch history, which also covers what played while Nightjar was not running.

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];
const hours = (h: number) => (h < 1 ? `${Math.round(h * 60)} min` : `${h.toFixed(h < 10 ? 1 : 0)} h`);
const when = (ts: number) => new Date(ts * 1000).toLocaleString(undefined, { weekday: 'short', hour: 'numeric', minute: '2-digit' });

function Ranked({ list, unit }: { list: PlexCount[]; unit: 'hours' | 'plays' }) {
  if (!list.length) return <p className="note plex-stats-none">Nothing yet.</p>;
  const most = Math.max(...list.map((c) => (unit === 'hours' ? (c.hours ?? 0) : c.n)), 1e-9);
  return (
    <ol className="plex-ranked">
      {list.map((c) => {
        const v = unit === 'hours' ? (c.hours ?? 0) : c.n;
        return (
          <li key={c.name}>
            <span className="truncate" title={c.name}>
              {c.name}
            </span>
            <span className="num ink-3">{unit === 'hours' ? `${hours(v)} · ${plural(c.n, 'stream')}` : plural(c.n, 'time')}</span>
            <span className="bar" role="presentation">
              <span style={{ width: `${(v / most) * 100}%` }} />
            </span>
          </li>
        );
      })}
    </ol>
  );
}

export function PlexStatsBody({ st }: { st: PlexStats }) {
  const busiest = Math.max(...st.weekdays, 1e-9);
  return (
    <>
      <div className="stats-row plex-stats-row">
        <div>
          <span className="label">Streams</span>
          <div className="readout-value">{st.plays}</div>
          <div className="readout-sub">{st.watched ? `${st.watched} watched to the end` : 'seen by Nightjar'}</div>
        </div>
        <div>
          <span className="label">Watched</span>
          <div className="readout-value">{hours(st.hours)}</div>
          <div className="readout-sub">{st.users.length ? `most by ${st.users[0].name}` : '—'}</div>
        </div>
        <div>
          <span className="label">Most at once</span>
          <div className="readout-value">{st.peak || '—'}</div>
          <div className="readout-sub">{st.peak ? when(st.peak_at) : 'no streams'}</div>
        </div>
        <div>
          <span className="label">Transcoded</span>
          <div className="readout-value">{st.transcoded !== null ? `${Math.round(st.transcoded)}%` : '—'}</div>
          <div className="readout-sub">{st.remote !== null ? `${Math.round(st.remote)}% from outside` : '—'}</div>
        </div>
      </div>
      <div className="plex-stats-grid">
        <div>
          <span className="label plex-stats-label">Who watched</span>
          <Ranked list={st.users} unit="hours" />
        </div>
        <div>
          <span className="label plex-stats-label">Most played</span>
          <Ranked list={st.titles} unit="hours" />
        </div>
        <div>
          <span className="label plex-stats-label">By day</span>
          <div className="cores plex-days" role="img" aria-label="Hours watched each day of the week">
            {st.weekdays.map((h, i) => (
              <div key={i} className="core" title={`${DAYS[i]}: ${hours(h)}`}>
                <span style={{ height: `${Math.max(h ? 4 : 0, (h / busiest) * 100)}%` }} />
              </div>
            ))}
          </div>
          <div className="core-axis plex-days-axis">
            {DAYS.map((d) => (
              <span key={d}>{d[0]}</span>
            ))}
          </div>
        </div>
        <div>
          <span className="label plex-stats-label">Finished, from Plex’s history</span>
          {st.history ? <p className="note plex-stats-none">Plex’s history could not be read: {st.history}</p> : <Ranked list={st.finished} unit="plays" />}
        </div>
      </div>
      {(st.reasons ?? []).length > 0 && (
        <p className="plex-added">
          <span className="label plex-stats-label">Why converted</span> {st.reasons!.map((r) => `${r.name} (${plural(r.n, 'stream')})`).join(' · ')}
        </p>
      )}
      {st.added.length > 0 && (
        <p className="plex-added">
          <span className="label plex-stats-label">Added</span> {st.added.map((a) => `${a.items.toLocaleString()} ${a.label} to ${a.library}`).join(' · ')}
        </p>
      )}
    </>
  );
}

// PlexStatsSection is the Plex page's look back over the last week or month.
export function PlexStatsSection() {
  const [days, setDays] = useState(7);
  const fetch = useCallback(() => api.plexStats(days), [days]);
  const { data } = usePoll(fetch, 60000);
  const st = data?.stats;
  return (
    <Section
      label={`The last ${days === 7 ? 'week' : `${days} days`}`}
      aside={
        <Seg
          label="Range"
          value={days}
          onChange={setDays}
          options={[
            { value: 7, label: '7d' },
            { value: 30, label: '30d' },
          ]}
        />
      }
    >
      {st ? <PlexStatsBody st={st} /> : <p className="note plex-stats-none">Counting…</p>}
    </Section>
  );
}
