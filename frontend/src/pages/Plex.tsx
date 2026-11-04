import { useMemo, useState } from 'react';
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
const KIND: Record<string, string> = { movie: 'movies', show: 'shows', artist: 'artists', photo: 'photos' };

function Recent({ plays, now }: { plays: PlexPlay[]; now: number }) {
  return (
    <table className="table">
      <tbody>
        {plays.slice(0, 15).map((p) => (
          <tr key={`${p.started}${p.user}${p.title}`}>
            <td className="num ink-3 shrink">{formatAgo(p.ended, now)}</td>
            <td className="shrink">{p.user}</td>
            <td className="fill">
              <span className="truncate" style={{ display: 'block' }} title={`${p.title} ${p.subtitle}`}>
                {p.title} <span className="ink-3">{p.subtitle}</span>
              </span>
            </td>
            <td className="shrink hide-sm">
              <Decision stream={{ decision: p.decision, hw_transcode: false }} />
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
          <MediaTabs current="/plex" />
          <div className="svc-target truncate">
            {srv ? `Plex Media Server ${srv.version} · ${srv.platform} · ` : ''}
            {p.url}
          </div>
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
              peak {peakTotal} at once since {p.samples.length ? new Date(p.samples[0].ts * 1000).toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : '—'}
            </div>
          </Section>
          <Section label="Bandwidth" aside={<span className="small ink-3">Mbps</span>}>
            <TrendChart series={bandwidth} height={120} format={mbps} tip={(v) => `${mbps(v)} Mbps`} emptyText="collecting data…" />
          </Section>
          <Section label="Recently finished" aside={<span className="small ink-3">since the monitor started</span>}>
            {p.recent.length ? (
              <Recent plays={p.recent} now={now} />
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
      {managing && <PlexStreamSheet stream={p.streams.find((s) => s.id === managing)} onClose={() => setManaging(undefined)} onStopped={onChange} />}
    </>
  );
}

const fetchFull = () => api.plex('full');

export function PlexPage() {
  const plex = usePoll(fetchFull, 5000);
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
