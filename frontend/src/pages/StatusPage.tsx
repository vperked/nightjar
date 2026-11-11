import { useEffect, useMemo, useState, type KeyboardEvent } from 'react';
import { bootDone } from '../boot';
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

const ICON = { operational: 'M8 12.5l2.7 2.7L16 9.5', degraded: 'M12 7.5v5.5M12 16.5h.01', outage: 'M9 9l6 6M15 9l-6 6' } as const;

const STATE: Record<ServiceStatus, string> = { up: 'Operational', down: 'Down', unknown: 'Checking', paused: 'Paused', maintenance: 'Maintenance' };

// when is a time for visitors: "Sun 3 Nov, 02:00", or only the time when it is the same day as `same`.
function when(ts: number, same?: number): string {
  const d = new Date(ts * 1000);
  const time = d.toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
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

// names lists services for a sentence: "plex", "plex and sonarr", "plex, sonarr and 3 more".
function names(list: string[]): string {
  if (list.length <= 2) return list.join(' and ');
  if (list.length === 3) return `${list[0]}, ${list[1]} and ${list[2]}`;
  return `${list[0]}, ${list[1]} and ${list.length - 2} more`;
}

// heroLine says what is wrong, by name, so a visitor need not hunt the list for the red dot.
function heroLine(data: PublicStatus): string {
  const named = (status: ServiceStatus) => [...new Set(data.services.filter((s) => s.status === status).map((s) => s.name))];
  const down = named('down');
  const resting = named('maintenance');
  const verb = (l: string[]) => (l.length === 1 ? 'is' : 'are');
  if (data.overall === 'degraded' && down.length) return `${names(down)} ${verb(down)} down. Everything else is running normally.`;
  if (data.overall === 'operational' && resting.length) return `${names(resting)} ${verb(resting)} under planned maintenance. Everything else is running normally.`;
  return VERDICT[data.overall].sub;
}

// overallUptime is the mean of the listed services' figures, over 90 days (30 from an older monitor),
// for the headline; null when the design hides uptime or there is none yet.
function overallUptime(data: PublicStatus): { value: number; days: number } | null {
  if (data.show_uptime === false) return null;
  const listed = data.services.filter((s) => s.status !== 'paused');
  const long = listed.map((s) => s.uptime_90d).filter((v): v is number => typeof v === 'number');
  const short = listed.map((s) => s.uptime_30d).filter((v): v is number => typeof v === 'number');
  const [values, days] = long.length ? [long, 90] : [short, 30];
  return values.length ? { value: values.reduce((a, b) => a + b, 0) / values.length, days } : null;
}

function ago(seconds: number): string {
  if (seconds < 10) return 'just now';
  if (seconds < 60) return `${Math.floor(seconds / 10) * 10} seconds ago`;
  const m = Math.round(seconds / 60);
  return m === 1 ? 'a minute ago' : `${m} minutes ago`;
}

// PHONE matches the stylesheet's phone width, where the bars show only the last 30 days.
const PHONE = '(max-width: 640px)';
const PHONE_DAYS = 30;

// usePhone follows whether the page is at phone width.
function usePhone(): boolean {
  const [phone, setPhone] = useState(() => typeof window !== 'undefined' && !!window.matchMedia?.(PHONE).matches);
  useEffect(() => {
    const mq = window.matchMedia?.(PHONE);
    if (!mq) return;
    const change = () => setPhone(mq.matches);
    change();
    mq.addEventListener?.('change', change);
    return () => mq.removeEventListener?.('change', change);
  }, []);
  return phone;
}

// DayBars is a service's last 90 days, or 30 on a phone. Hovering a day, or moving along with the
// arrow keys, says how that day went.
function DayBars({ days: all, name }: { days: PublicDay[]; name: string }) {
  // A phone draws only the days it shows, so the tooltip sits over the bar it describes and the
  // arrow keys cannot walk into days that are not there.
  const phone = usePhone();
  const days = phone ? all.slice(-PHONE_DAYS) : all;
  const [at, setAt] = useState<number | null>(null);
  const bad = days.filter((d) => d.uptime !== null && d.uptime < 99.9).length;
  const keys = (e: KeyboardEvent) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return;
    e.preventDefault();
    setAt((i) => Math.max(0, Math.min(days.length - 1, (i ?? days.length) + (e.key === 'ArrowLeft' ? -1 : 1))));
  };
  const d = at === null ? undefined : days[at];
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

// Uptime is a service's figure over the days its bars show: 90 days, or 30 on a phone, which draws
// only the last 30 bars.
function Uptime({ long, short }: { long: number; short: number | null | undefined }) {
  if (short === null || short === undefined || percent(short) === percent(long)) return <span className="status-row-uptime num">{percent(long)} uptime</span>;
  return (
    <span className="status-row-uptime num">
      <span className="long">{percent(long)} uptime</span>
      <span className="short">{percent(short)} uptime</span>
    </span>
  );
}

function Row({ s, data }: { s: PublicService; data: PublicStatus }) {
  const days = data.show_history !== false ? (s.days ?? []) : [];
  // An older monitor sends no days; the page still says how the service has done.
  const uptime = data.show_uptime !== false ? (s.uptime_90d ?? s.uptime_30d) : null;
  return (
    <div className={`status-row ${days.length ? 'has-days' : ''}`}>
      <div className="status-row-head">
        <span className="status-name">{s.name}</span>
        {uptime !== null && uptime !== undefined && <Uptime long={uptime} short={days.length ? s.uptime_30d : null} />}
        <span className={`status-state ${s.status}`}>
          <span className="status-dot" aria-hidden="true" />
          {STATE[s.status]}
        </span>
      </div>
      {days.length > 0 && <DayBars days={days} name={s.name} />}
    </div>
  );
}

function Group({ name, services, data }: { name: string; services: PublicService[]; data: PublicStatus }) {
  const watched = services.filter((s) => s.status !== 'paused');
  const up = watched.filter((s) => s.status === 'up').length;
  const down = watched.filter((s) => s.status === 'down').length;
  // Red when all are down, amber when some are, grey when some only rest (maintenance, checking).
  const tone = down ? (down === watched.length ? 'down' : 'some') : up < watched.length ? 'quiet' : '';
  // One scale under the whole list, rather than under every row.
  const span = data.show_history !== false ? Math.max(0, ...services.map((s) => (s.days ?? []).length)) : 0;
  return (
    <section className="status-group">
      <div className="status-group-head">
        <h2>{name || 'Services'}</h2>
        <span className={`status-group-count ${tone}`}>{watched.length ? (up === watched.length ? 'All operational' : `${up} of ${watched.length} operational`) : ''}</span>
      </div>
      <div className="status-list">
        {services.map((s) => (
          <Row key={s.name} s={s} data={data} />
        ))}
        {span > 0 && (
          <div className="status-scale" aria-hidden="true">
            <span>
              <span className="long">{plural(span, 'day')} ago</span>
              <span className="short">{plural(Math.min(span, PHONE_DAYS), 'day')} ago</span>
            </span>
            <span className="status-scale-rule" />
            <span>Today</span>
          </div>
        )}
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
  // Ongoing incidents are news, so they sit under the headline; settled ones go at the foot.
  const incidents = data.incidents ?? [];
  const ongoing = incidents.filter((n) => n.ended === null);
  const settled = incidents.filter((n) => n.ended !== null);
  const overall = overallUptime(data);
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

      {/* Only the verdict is a live region: a screen reader hears when it changes, not the
          "Updated" time beside it, which moves on every few seconds. */}
      <div className={`status-hero ${data.overall}`}>
        <div className="status-hero-main" role="status">
          <span className="status-hero-icon" aria-hidden="true">
            <svg viewBox="0 0 24 24">
              <path d={ICON[data.overall]} />
            </svg>
          </span>
          <div className="min0">
            <strong>{verdict.title}</strong>
            <span>{heroLine(data)}</span>
          </div>
        </div>
        <div className="status-hero-facts">
          {overall && (
            <span className="status-fact">
              <b className="num">{percent(overall.value)}</b> uptime over {overall.days} days
            </span>
          )}
          <span className="status-fact">{plural(data.services.length, 'service')}</span>
          <span className="status-hero-when num">
            <i className="status-live" aria-hidden="true" />
            Updated {ago(Math.max(0, now - data.updated))}
          </span>
        </div>
      </div>

      {(ongoing.length > 0 || (data.maintenance ?? []).length > 0) && (
        <section className="status-notices" aria-label="Incidents and planned maintenance">
          {ongoing.map((n) => (
            <article key={n.id} className="status-notice incident">
              <span className="status-notice-tag">Ongoing incident</span>
              <div className="min0">
                <strong>{n.title}</strong>
                <span className="status-notice-when num">Since {when(n.started)}</span>
                {n.text && <p>{n.text}</p>}
              </div>
            </article>
          ))}
          {(data.maintenance ?? []).map((m) => (
            <div key={m.title + m.start} className={`status-notice maint ${m.on ? 'on' : ''}`}>
              <span className="status-notice-tag">{m.on ? 'Maintenance now' : 'Planned'}</span>
              <div className="min0">
                <strong>{m.title}</strong>
                <span className="status-notice-when num">
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

      {settled.length > 0 && (
        <section className="status-incidents">
          <h2>Recent incidents</h2>
          {settled.map((n) => (
            <article key={n.id} className="status-incident">
              <div className="status-incident-head">
                <strong>{n.title}</strong>
                <span className="status-incident-when num">
                  <span className="status-resolved">Resolved</span>
                  {when(n.started)} – {when(n.ended!, n.started)}
                </span>
              </div>
              {n.text && <p>{n.text}</p>}
            </article>
          ))}
        </section>
      )}
      {data.services.length === 0 && <p className="note">No services are listed yet.</p>}

      <Custom html={data.footer_html} where="bottom" />
      <footer className="status-footer">
        <span>{data.footer || 'Nightjar'}</span>
        <span>Refreshes every 30 seconds</span>
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
  // The loading screen index.html opens with stays up until the first answer, or the first failure.
  const answered = Boolean(status.data || status.error);
  useEffect(() => {
    if (answered) bootDone();
  }, [answered]);

  if (status.error && !status.data) {
    return (
      <main className="status-page status-unavailable">
        <header className="status-top">
          <h1>Status</h1>
        </header>
        <div className="status-hero unknown" role="status">
          <div className="status-hero-main">
            <span className="status-hero-icon" aria-hidden="true">
              <svg viewBox="0 0 24 24">
                <path d="M12 7.5v5.5M12 16.5h.01" />
              </svg>
            </span>
            <div className="min0">
              <strong>The status page is not available</strong>
              <span>It could not be reached just now. This page tries again every 30 seconds.</span>
            </div>
          </div>
        </div>
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
