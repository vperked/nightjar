import { useEffect, useMemo, useState, type KeyboardEvent } from 'react';
import { api } from '../api/client';
import type { PublicDay, PublicService, PublicStatus, ServiceStatus } from '../api/types';
import { DemoBanner } from '../components/ui';
import { sanitizeHTML, scopeCSS } from '../customCode';
import { formatDuration, plural } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';

// The public status page: how things are now, then each service with its last 90 days, one bar a
// day. It shows only what its design lists, never targets, error details or response times.

const VERDICT = {
  operational: { title: 'All systems operational', sub: 'Everything is up and running.' },
  degraded: { title: 'Partial outage', sub: 'Some services are having trouble. The rest are running normally.' },
  outage: { title: 'Major outage', sub: 'Services are down. We are on it.' },
} as const;

const ICON = {
  operational: 'M8 12.5l2.7 2.7L16 9.5',
  degraded: 'M12 7.5v5.5M12 16.5h.01',
  outage: 'M9 9l6 6M15 9l-6 6',
} as const;

const STATE: Record<ServiceStatus, string> = { up: 'Operational', down: 'Down', unknown: 'Checking', paused: 'Paused', maintenance: 'Maintenance' };

// when is a time for visitors: "Sun 3 Nov, 02:00", or only the time when it is the same day as `same`.
function when(ts: number, same?: number): string {
  const d = new Date(ts * 1000);
  const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });
  if (same !== undefined && new Date(same * 1000).toDateString() === d.toDateString()) return time;
  return `${d.toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' })}, ${time}`;
}

// dayTone is a day's bar colour: no downtime, a little, a lot, or no data.
function dayTone(d: PublicDay): 'none' | 'good' | 'minor' | 'major' {
  if (d.uptime === null) return 'none';
  if (d.uptime >= 99.9) return 'good';
  if (d.uptime >= 95) return 'minor';
  return 'major';
}

const dayName = (t: number) => new Date(t * 1000).toLocaleDateString([], { weekday: 'short', month: 'short', day: 'numeric' });

// uptimeOf is the share of days' uptime, for the designer's preview (the page itself gets the
// monitor's figure, weighted by checks).
export function uptimeOf(days: PublicDay[]): number | null {
  const known = days.filter((d) => d.uptime !== null);
  return known.length ? known.reduce((n, d) => n + d.uptime!, 0) / known.length : null;
}

function percent(v: number): string {
  return v >= 99.995 ? '100%' : `${v.toFixed(v >= 99 ? 2 : 1)}%`;
}

function ago(seconds: number): string {
  if (seconds < 10) return 'just now';
  if (seconds < 60) return `${Math.floor(seconds / 10) * 10} seconds ago`;
  const m = Math.round(seconds / 60);
  return m === 1 ? 'a minute ago' : `${m} minutes ago`;
}

// DayBars is a service's last 90 days. Hovering a day, or moving along with the arrow keys, says
// how that day went.
function DayBars({ days, name }: { days: PublicDay[]; name: string }) {
  const [at, setAt] = useState<number | null>(null);
  const bad = days.filter((d) => d.uptime !== null && d.uptime < 99.9).length;
  const keys = (e: KeyboardEvent) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    e.preventDefault();
    setAt((i) => Math.max(0, Math.min(days.length - 1, (i ?? days.length) + (e.key === 'ArrowLeft' ? -1 : 1))));
  };
  const d = at === null ? null : days[at];
  return (
    <div className="day-bars-wrap">
      <div
        className="day-bars"
        role="img"
        tabIndex={0}
        aria-label={`${name}, the last ${days.length} days: ${bad ? plural(bad, 'day') + ' with downtime' : 'no downtime'}`}
        onKeyDown={keys}
        onBlur={() => setAt(null)}
        onPointerLeave={() => setAt(null)}
      >
        {days.map((day, i) => (
          <span key={day.t} className={`day ${dayTone(day)} ${i === at ? 'on' : ''}`} onPointerEnter={() => setAt(i)} />
        ))}
      </div>
      {d && at !== null && (
        <div className="day-tip" style={{ ['--at' as string]: `${((at + 0.5) / days.length) * 100}%` }} role="tooltip">
          <strong>{dayName(d.t)}</strong>
          {d.uptime === null ? (
            <span>No data</span>
          ) : (
            <>
              <span className={`day-tip-state ${dayTone(d)}`}>{d.down_s > 0 ? `${percent(d.uptime)} up` : 'No downtime'}</span>
              {d.down_s > 0 && <span className="day-tip-sub">about {formatDuration(d.down_s)} down</span>}
            </>
          )}
        </div>
      )}
    </div>
  );
}

function Row({ s, data }: { s: PublicService; data: PublicStatus }) {
  const days = data.show_history !== false ? (s.days ?? []) : [];
  const uptime = data.show_uptime !== false ? (s.uptime_90d ?? s.uptime_30d) : null;
  return (
    <div className="status-row">
      <div className="status-row-head">
        <span className="status-name">{s.name}</span>
        <span className={`status-state ${s.status}`}>
          <span className="status-dot" aria-hidden="true" />
          {STATE[s.status]}
        </span>
      </div>
      {days.length > 0 && (
        <>
          <DayBars days={days} name={s.name} />
          <div className="status-scale">
            <span>
              <span className="long">{days.length} days ago</span>
              <span className="short">30 days ago</span>
            </span>
            <span className="status-scale-rule" aria-hidden="true" />
            {uptime !== null && uptime !== undefined && <span className="status-scale-uptime">{percent(uptime)} uptime</span>}
            <span className="status-scale-rule" aria-hidden="true" />
            <span>Today</span>
          </div>
        </>
      )}
      {days.length === 0 && uptime !== null && uptime !== undefined && <div className="status-scale-uptime solo">{percent(uptime)} uptime</div>}
    </div>
  );
}

function Group({ name, services, data }: { name: string; services: PublicService[]; data: PublicStatus }) {
  const watched = services.filter((s) => s.status !== 'paused');
  const up = watched.filter((s) => s.status === 'up').length;
  const down = watched.some((s) => s.status === 'down');
  return (
    <section className="status-group">
      <div className="status-group-head">
        <h2>{name || 'Services'}</h2>
        <span className={`status-group-count ${down ? 'down' : ''}`}>{watched.length ? (up === watched.length ? 'All operational' : `${up} of ${watched.length} operational`) : ''}</span>
      </div>
      <div className="status-list">
        {services.map((s) => (
          <Row key={s.name} s={s} data={data} />
        ))}
      </div>
    </section>
  );
}

// Custom is the owner's own HTML, cleaned of anything that could run code.
function Custom({ html, where }: { html?: string; where: 'top' | 'bottom' }) {
  const clean = useMemo(() => sanitizeHTML(html ?? ''), [html]);
  return clean ? <div className={`status-custom status-custom-${where}`} dangerouslySetInnerHTML={{ __html: clean }} /> : null;
}

// StatusView draws a status page from its data; the designer previews with it too, with the
// owner's CSS kept inside scope so it styles only the preview.
export function StatusView({ data, scope }: { data: PublicStatus; scope?: string }) {
  const now = useNow(5000);
  const css = useMemo(() => (data.css ? (scope ? scopeCSS(data.css, scope) : data.css) : ''), [data.css, scope]);
  const groups = (data.groups ?? [{ name: '', services: data.services }]).filter((g) => g.services.length);
  const verdict = VERDICT[data.overall];
  const history = data.show_history !== false && data.services.some((s) => (s.days ?? []).length > 0);
  return (
    <main className="status-page">
      {css && <style>{css}</style>}
      <Custom html={data.header_html} where="top" />
      <header className="status-top">
        <h1>{data.title}</h1>
        {data.description && <p className="status-description">{data.description}</p>}
      </header>

      {data.announcement && (
        <div className={`status-announcement ${data.announcement.tone}`} role="note">
          <svg viewBox="0 0 24 24" aria-hidden="true">
            <circle cx="12" cy="12" r="9" />
            <path d={data.announcement.tone === 'info' ? 'M12 11v5M12 7.5h.01' : 'M12 7.5v5.5M12 16.5h.01'} />
          </svg>
          <p>{data.announcement.text}</p>
        </div>
      )}

      <div className={`status-hero ${data.overall}`} role="status">
        <span className="status-hero-icon" aria-hidden="true">
          <svg viewBox="0 0 24 24">
            <path d={ICON[data.overall]} />
          </svg>
        </span>
        <div className="min0">
          <strong>{verdict.title}</strong>
          <span>{verdict.sub}</span>
        </div>
        <span className="status-hero-when num">Updated {ago(Math.max(0, now - data.updated))}</span>
      </div>

      {(data.maintenance ?? []).length > 0 && (
        <section className="status-maint" aria-label="Planned maintenance">
          {data.maintenance!.map((m) => (
            <div key={m.title + m.start} className={`status-maint-row ${m.on ? 'on' : ''}`}>
              <span className="status-maint-tag">{m.on ? 'Maintenance now' : 'Planned'}</span>
              <div className="min0">
                <strong>{m.title}</strong>
                <span className="status-maint-when num">
                  {when(m.start)} – {when(m.end, m.start)}
                  {m.all ? ' · all services' : m.covers.length ? ` · ${m.covers.join(', ')}` : ''}
                </span>
                {m.note && <p>{m.note}</p>}
              </div>
            </div>
          ))}
        </section>
      )}

      {groups.map((g, i) => (
        <Group key={g.name + i} name={g.name} services={g.services} data={data} />
      ))}

      {(data.incidents ?? []).length > 0 && (
        <section className="status-incidents">
          <h2>Recent incidents</h2>
          {data.incidents!.map((n) => (
            <article key={n.id} className={`status-incident ${n.ended === null ? 'open' : ''}`}>
              <div className="status-incident-head">
                <strong>{n.title}</strong>
                <span className="num">
                  {when(n.started)}
                  {n.ended === null ? ' · ongoing' : ''}
                </span>
              </div>
              {n.text && <p>{n.text}</p>}
            </article>
          ))}
        </section>
      )}
      {data.services.length === 0 && <p className="note">No services are listed yet.</p>}

      {history && (
        <div className="status-legend" aria-label="What the bars mean">
          <span>
            <i className="day good" /> No downtime
          </span>
          <span>
            <i className="day minor" /> Some downtime
          </span>
          <span>
            <i className="day major" /> Major outage
          </span>
          <span>
            <i className="day none" /> No data
          </span>
        </div>
      )}

      <Custom html={data.footer_html} where="bottom" />
      <footer className="status-footer">
        <span>{data.footer || 'Nightjar'}</span>
        <span>{plural(data.services.length, 'service')} · refreshes every 30 seconds</span>
      </footer>
    </main>
  );
}

// StatusPage is public: it shows only what its design lists, never targets or error details.
export function StatusPage() {
  const status = usePoll(api.status, 30000);
  useEffect(() => {
    if (!status.data) return;
    const { title, overall } = status.data;
    document.title = overall === 'operational' ? title : `${VERDICT[overall].title} · ${title}`;
  }, [status.data]);

  if (status.error && !status.data) {
    return (
      <main className="status-page">
        <header className="status-top">
          <h1>Status</h1>
        </header>
        <p className="note">The status page is not available.</p>
      </main>
    );
  }
  if (!status.data) return <main className="status-page loading">loading…</main>;
  return (
    <>
      <DemoBanner />
      <StatusView data={status.data} />
    </>
  );
}
