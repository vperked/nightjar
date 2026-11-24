import { type KeyboardEvent, useCallback, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import type { PlexPlay, PlexResponse, PlexState } from '../api/types';
import { Decision, NowPlaying, plexState } from '../components/PlexNow';
import { PlexStatsSection } from '../components/PlexStats';
import { PlexStreamSheet } from '../components/PlexStreamSheet';
import { Skeleton } from '../components/Skeleton';
import { Legend, TrendChart } from '../components/TrendChart';
import { MediaTabs, Section, State } from '../components/ui';
import { formatAgo, formatKbps, plural } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';

const count = (v: number) => String(Math.round(v));
const mbps = (v: number) => (v === 0 ? '0' : v.toFixed(v < 10 ? 1 : 0));
// sinceWhen is when the charts start: "3:15 PM" today, "Nov 10, 3:15 PM" on an earlier day.
const sinceWhen = (ts: number) => {
  const d = new Date(ts * 1000);
  const time = d.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
  return d.toDateString() === new Date().toDateString() ? time : `${d.toLocaleDateString([], { month: 'short', day: 'numeric' })}, ${time}`;
};
const KIND: Record<string, string> = { movie: 'movies', show: 'shows', artist: 'artists', photo: 'photos' };

// Recent lists the streams that finished; each opens how it was delivered.
function Recent({ plays, now, onOpen }: { plays: PlexPlay[]; now: number; onOpen: (p: PlexPlay) => void }) {
  const open = (e: KeyboardEvent, p: PlexPlay) => {
    if (e.key !== 'Enter' && e.key !== ' ') return;
    e.preventDefault();
    onOpen(p);
  };
  return (
    <table className="table">
      <tbody>
        {plays.slice(0, 15).map((p) => (
          <tr key={`${p.started}${p.user}${p.title}`} className="link" tabIndex={0} aria-label={`${p.title}, watched by ${p.user}`} onClick={() => onOpen(p)} onKeyDown={(e) => open(e, p)}>
            <td className="num ink-3 shrink">{formatAgo(p.ended, now)}</td>
            <td className="shrink">{p.user}</td>
            <td className="fill">
              <span className="truncate" style={{ display: 'block' }} title={`${p.title} ${p.subtitle}`}>
                {p.title} <span className="ink-3">{p.subtitle}</span>
              </span>
            </td>
            <td className="shrink hide-sm">
              <Decision stream={{ decision: p.decision, hw_transcode: p.last?.hw_transcode ?? false }} />
            </td>
            <td className="r num ink-3 shrink">{Math.round(p.progress)}%</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}

function Body({ p, onChange }: { p: PlexState; onChange: (r: PlexResponse) => void }) {
  const now = useNow(5000);
  const [managing, setManaging] = useState<string>();
  const [viewing, setViewing] = useState<PlexPlay>();
  const streams = useMemo(
    () => [
      { label: 'Direct', color: 'var(--series-1)', points: p.samples.map((s) => ({ t: s.ts, v: s.direct })) },
      { label: 'Transcode', color: 'var(--series-2)', points: p.samples.map((s) => ({ t: s.ts, v: s.transcode })) },
    ],
    [p.samples],
  );
  const bandwidth = useMemo(() => [{ label: 'Mbps', color: 'var(--series-1)', points: p.samples.map((s) => ({ t: s.ts, v: s.bandwidth_kbps / 1000 })) }], [p.samples]);
  const peak = p.samples.reduce((m, s) => Math.max(m, s.direct, s.transcode), 0);
  const peakTotal = p.samples.reduce((m, s) => Math.max(m, s.direct + s.transcode), 0);
  // Stream counts are whole numbers: an even max keeps the midpoint tick on an integer.
  const countMax = Math.max(2, Math.ceil((peak + 1) / 2) * 2);
  const items = p.libraries.reduce((n, l) => n + l.count, 0);
  const srv = p.server;
  const total = p.direct + p.transcode;

  return (
    <>
      <div className="page-title">
        <div className="min0">
          <h1 className="truncate">{srv?.name || 'Plex'}</h1>
          <div className="svc-target truncate">
            {srv ? `Plex Media Server ${srv.version} · ${srv.platform} · ` : ''}
            {p.url}
          </div>
          <MediaTabs current="/plex" />
        </div>
        <State status={plexState(p.status)} />
      </div>

      {p.status === 'down' && (
        <div className="alert" role="alert">
          Plex is unreachable: {p.detail}. Showing the last known libraries and history.
        </div>
      )}
      {p.status !== 'down' && p.detail && <div className="alert">Last check failed: {p.detail}. Retrying.</div>}
      {srv?.update_available && (
        <div className="notice">
          Server update available: <span className="mono">{srv.update_available}</span>
        </div>
      )}

      <div className="stats-row">
        <div>
          <span className="label">Streams</span>
          <div className="readout-value">{total}</div>
          <div className="readout-sub num">
            {p.direct} direct · {p.transcode} transcode
          </div>
        </div>
        <div>
          <span className="label">Bandwidth</span>
          <div className="readout-value">{formatKbps(p.bandwidth_kbps)}</div>
          <div className="readout-sub num">{formatKbps(p.wan_kbps)} remote</div>
        </div>
        <div>
          <span className="label">Response</span>
          <div className="readout-value">{p.latency_ms !== null ? `${Math.round(p.latency_ms)}ms` : '—'}</div>
          <div className="readout-sub">checked {formatAgo(p.checked_at, now)}</div>
        </div>
        <div>
          <span className="label">Library</span>
          <div className="readout-value">{p.libraries.length ? items.toLocaleString() : '—'}</div>
          <div className="readout-sub">{p.libraries.length ? `items in ${p.libraries.length} ${p.libraries.length === 1 ? 'library' : 'libraries'}` : 'counting…'}</div>
        </div>
      </div>

      <PlexStatsSection />
      <div className="columns">
        <div className="col">
          <Section label="Now playing" aside={<span className="small ink-3 num">{plural(total, 'stream')}</span>}>
            <NowPlaying streams={p.streams} onManage={(s) => setManaging(s.id)} />
          </Section>
          <Section label="Concurrent streams" aside={<Legend series={streams} />}>
            <TrendChart series={streams} raw height={150} max={countMax} format={count} tip={(v) => `${Math.round(v)} ${Math.round(v) === 1 ? 'stream' : 'streams'}`} emptyText="collecting data…" />
            <div className="chart-note small ink-3 num">
              peak {peakTotal} at once since {p.samples.length ? sinceWhen(p.samples[0].ts) : '—'}
            </div>
          </Section>
          <Section label="Bandwidth" aside={<span className="small ink-3">Mbps</span>}>
            <TrendChart series={bandwidth} height={120} format={mbps} tip={(v) => `${mbps(v)} Mbps`} emptyText="collecting data…" />
          </Section>
          <Section label="Recently finished" aside={<span className="small ink-3">since the monitor started</span>}>
            {p.recent.length ? (
              <Recent plays={p.recent} now={now} onOpen={setViewing} />
            ) : (
              <p className="note" style={{ padding: '4px 0 0' }}>
                No finished streams yet.
              </p>
            )}
          </Section>
        </div>
        <div className="col">
          <Section label="Libraries" aside={p.libraries_at ? <span className="small ink-3">counted {formatAgo(p.libraries_at, now)}</span> : undefined}>
            {p.libraries.length ? (
              <table className="table">
                <tbody>
                  {p.libraries.map((l) => (
                    <tr key={l.key}>
                      <td className="fill">
                        <span className="truncate" style={{ display: 'block' }}>
                          {l.title}
                        </span>
                        <div className="sub">{l.refreshing ? 'scanning…' : (KIND[l.type] ?? l.type)}</div>
                      </td>
                      <td className="r num">{l.count.toLocaleString()}</td>
                      <td className="r num ink-3 small">{l.child_label ? `${(l.child_count ?? 0).toLocaleString()} ${l.child_label}` : ''}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            ) : (
              <p className="note" style={{ padding: '4px 0 0' }}>
                {p.status === 'up' ? 'Counting library items…' : 'Not counted yet.'}
              </p>
            )}
          </Section>
          <Section label="Server">
            <dl className="kv">
              <dt>Name</dt>
              <dd>{srv?.name || '—'}</dd>
              <dt>Version</dt>
              <dd>{srv?.version || '—'}</dd>
              <dt>Platform</dt>
              <dd>{srv ? `${srv.platform} ${srv.platform_version}` : '—'}</dd>
              <dt>Address</dt>
              <dd title={p.url}>{p.url}</dd>
              <dt>In this state</dt>
              <dd>{p.since ? formatAgo(p.since, now).replace(' ago', '') : '—'}</dd>
            </dl>
          </Section>
        </div>
      </div>
      {viewing && <PlexStreamSheet play={viewing} onClose={() => setViewing(undefined)} />}
      {managing && <PlexStreamSheet stream={p.streams.find((s) => s.id === managing)} onClose={() => setManaging(undefined)} onStopped={onChange} />}
    </>
  );
}

// FULL_EVERY is how often the page fetches the whole state: the chart samples, finished plays and
// libraries. In between it polls the light summary (the streams and the readings, a few KB instead of
// hundreds of samples every 5 seconds), and adds each new check's counts to the charts itself.
const FULL_EVERY = 60_000;

const ids = (p: PlexState) => p.streams.map((s) => s.id).join();

// usePlex polls Plex every 5 seconds: the summary, and the whole state every FULL_EVERY, when a
// stream starts or ends (so it shows under Recently finished at once), and when the libraries were
// counted again.
function usePlex() {
  const full = useRef<PlexState>();
  const fullAt = useRef(0);
  // recheck asks for the whole state once more after the next check: a stream stopped from here
  // leaves the list at once, but reaches the finished plays only when Plex is checked again.
  const recheck = useRef(0);
  const fetchPlex = useCallback(async (): Promise<PlexResponse> => {
    const have = full.current;
    const whole = async () => {
      const r = await api.plex('full');
      full.current = r.plex;
      fullAt.current = Date.now();
      return r;
    };
    if (!have || Date.now() - fullAt.current >= FULL_EVERY) return whole();
    const r = await api.plex('summary');
    const now = r.plex;
    if (!r.enabled || !now) {
      full.current = undefined;
      return r;
    }
    const changed = ids(now) !== ids(have);
    if (changed || now.libraries_at > have.libraries_at || (recheck.current && now.checked_at > recheck.current)) {
      recheck.current = changed ? now.checked_at : 0;
      return whole();
    }
    // A successful check recorded one sample of these counts; failed checks record none.
    const last = have.samples.at(-1)?.ts ?? 0;
    if (now.status === 'up' && !now.detail && now.checked_at > last) {
      full.current = { ...have, samples: [...have.samples, { ts: now.checked_at, direct: now.direct, transcode: now.transcode, bandwidth_kbps: now.bandwidth_kbps }] };
    }
    const kept = full.current ?? have;
    return { ...r, plex: { ...now, samples: kept.samples, recent: kept.recent, libraries: kept.libraries } };
  }, []);
  return usePoll(fetchPlex, 5000, true, 'plex');
}

export function PlexPage() {
  const plex = usePlex();
  return (
    <main>
      {plex.error && !plex.unauthorized && (
        <div className="alert" role="alert">
          Can't reach the monitor ({plex.error.message}). Retrying.
        </div>
      )}
      {!plex.data ? (
        <Skeleton kind="dashboard" />
      ) : plex.data.enabled && plex.data.plex ? (
        <Body p={plex.data.plex} onChange={() => plex.refresh()} />
      ) : (
        <p className="note">
          Plex monitoring is off. Set <code>PLEX_MONITOR=true</code>, <code>PLEX_URL</code> and <code>PLEX_TOKEN</code>, then restart. <Link to="/">Back to overview</Link>
        </p>
      )}
    </main>
  );
}
