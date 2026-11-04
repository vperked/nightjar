import { useCallback, useMemo, useState } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import type { AnalyticsPoint, AnalyticsRangeName, HostAnalytics } from '../api/types';
import { Skeleton } from '../components/Skeleton';
import { TrendChart, withGaps, type Point } from '../components/TrendChart';
import { Section, Seg, ServersTabs } from '../components/ui';
import { formatBytes, formatDuration } from '../format';
import { usePoll } from '../hooks/usePoll';

// Analytics: every server, this one included, over a day, a week or a month. Each is kept as one
// row a minute, so a range reads back as about 300 points of means and peaks.

const RANGES: { value: AnalyticsRangeName; label: string }[] = [
  { value: '24h', label: '24 hours' },
  { value: '7d', label: '7 days' },
  { value: '30d', label: '30 days' },
];

type Metric = 'cpu' | 'temp' | 'mem' | 'disk';
const METRICS: { value: Metric; label: string; unit: string; max?: number }[] = [
  { value: 'cpu', label: 'CPU', unit: '%', max: 100 },
  { value: 'temp', label: 'Temperature', unit: '°C' },
  { value: 'mem', label: 'Memory', unit: '%', max: 100 },
  { value: 'disk', label: 'Fullest disk', unit: '%', max: 100 },
];

// line is one metric of a server's points, with a gap wherever buckets of `step` seconds are missing.
const line = (points: AnalyticsPoint[], pick: (p: AnalyticsPoint) => number | null, step: number): Point[] =>
  withGaps(
    points.map((p) => ({ t: p.t, v: pick(p) })),
    step,
  );
const n0 = (v: number | null | undefined, unit = '') => (v === null || v === undefined ? '—' : `${Math.round(v)}${unit}`);
const kindLabel = (h: HostAnalytics) => (h.kind === 'local' ? 'this server' : 'agent');
const link = (h: HostAnalytics) => (h.id === 'local' ? '/' : `/servers/${h.id}`);

function uptimeText(v: number | null): string {
  if (v === null) return '—';
  return v >= 99.995 ? '100%' : `${v.toFixed(v >= 99 ? 2 : 1)}%`;
}

function when(ts: number): string {
  return new Date(ts * 1000).toLocaleString([], { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
}

// Spark is a table cell's trend line, on its own scale.
function Spark({ points, unit }: { points: Point[]; unit: string }) {
  if (points.filter((p) => p.v !== null).length < 2) return null;
  return <TrendChart series={[{ label: unit, color: 'var(--series-1)', points }]} height={24} compact format={(v) => `${Math.round(v)}${unit}`} emptyText="" />;
}

// Tile is one of the headline numbers across every server.
function Tile({ label, value, sub, to }: { label: string; value: string; sub: string; to?: string }) {
  const body = (
    <>
      <span className="label">{label}</span>
      <div className="readout-value">{value}</div>
      <div className="readout-sub truncate">{sub}</div>
    </>
  );
  return to ? (
    <Link to={to} className="readout analytics-tile">
      {body}
    </Link>
  ) : (
    <div className="readout analytics-tile">{body}</div>
  );
}

function Tiles({ hosts, label }: { hosts: HostAnalytics[]; label: string }) {
  const watched = hosts.filter((h) => h.summary.uptime !== null);
  const minutes = (h: HostAnalytics) => h.points.reduce((n, p) => n + p.minutes, 0);
  const total = watched.reduce((n, h) => n + minutes(h), 0);
  const uptime = total ? watched.reduce((n, h) => n + (h.summary.uptime ?? 0) * minutes(h), 0) / total : null;
  const outages = watched.reduce((n, h) => n + h.summary.incidents, 0);
  const down = watched.reduce((n, h) => n + h.summary.downtime_s, 0);
  const hottest = [...hosts].filter((h) => h.summary.temp_peak !== null).sort((a, b) => b.summary.temp_peak! - a.summary.temp_peak!)[0];
  const busiest = [...hosts].filter((h) => h.summary.cpu_avg !== null).sort((a, b) => b.summary.cpu_avg! - a.summary.cpu_avg!)[0];
  const rx = hosts.reduce((n, h) => n + h.summary.rx_bytes, 0);
  const tx = hosts.reduce((n, h) => n + h.summary.tx_bytes, 0);
  return (
    <div className="readouts analytics-tiles">
      <Tile
        label="Uptime"
        value={uptimeText(uptime)}
        sub={watched.length === 0 ? 'no other servers yet' : outages ? `${outages} ${outages === 1 ? 'outage' : 'outages'}, ${formatDuration(down)} down` : `no outages in ${label}`}
      />
      <Tile
        label="Hottest"
        value={hottest ? n0(hottest.summary.temp_peak, '°C') : '—'}
        sub={hottest ? `${hottest.name}, ${n0(hottest.summary.temp_avg, '°C')} on average` : 'no temperature readings'}
        to={hottest && link(hottest)}
      />
      <Tile
        label="Busiest"
        value={busiest ? n0(busiest.summary.cpu_avg, '%') : '—'}
        sub={busiest ? `${busiest.name}'s average CPU, peak ${n0(busiest.summary.cpu_peak, '%')}` : 'no CPU readings'}
        to={busiest && link(busiest)}
      />
      <Tile label="Data moved" value={formatBytes(rx + tx)} sub={`↓ ${formatBytes(rx)} in · ↑ ${formatBytes(tx)} out`} />
    </div>
  );
}

// fullIn says when a disk fills at the rate it is filling.
export function fullIn(days: number): string {
  if (days < 1) return 'full within a day';
  if (days > 365) return 'full in over a year';
  return `full in ~${Math.round(days)} days`;
}

function DiskCell({ h }: { h: HostAnalytics }) {
  const s = h.summary;
  if (s.disk_now === null) return <span className="ink-3">—</span>;
  const soon = s.disk_full_days !== null && s.disk_full_days < 30;
  return (
    <span className="analytics-disk">
      <span className="num">{n0(s.disk_now, '%')}</span>
      <span className={`small ${soon ? 'pct-poor' : 'ink-3'}`}>{s.disk_full_days !== null ? fullIn(s.disk_full_days) : s.disk_change !== null && s.disk_change <= 0.05 ? 'steady' : ''}</span>
    </span>
  );
}

function HostsTable({ hosts, step }: { hosts: HostAnalytics[]; step: number }) {
  return (
    <div className="analytics-scroll">
      <table className="table analytics-table">
        <thead>
          <tr>
            <th>Server</th>
            <th className="r">Uptime</th>
            <th>CPU avg · peak</th>
            <th>Temp avg · peak</th>
            <th className="r">Memory</th>
            <th>Fullest disk</th>
            <th className="r">Data</th>
          </tr>
        </thead>
        <tbody>
          {hosts.map((h) => {
            const s = h.summary;
            return (
              <tr key={h.id}>
                <td>
                  <Link to={link(h)} className="analytics-name">
                    <strong className="truncate">{h.name}</strong>
                    <span className="small ink-3">
                      {kindLabel(h)}
                      {s.coverage < 0.9 && h.points.length > 0 ? ` · readings for ${Math.max(1, Math.round(s.coverage * 100))}% of the range` : ''}
                      {h.points.length === 0 ? ' · no readings yet' : ''}
                    </span>
                  </Link>
                </td>
                <td className={`r num ${s.uptime !== null && s.uptime < 99 ? 'pct-poor' : s.uptime !== null && s.uptime < 99.9 ? 'pct-fair' : ''}`}>
                  {uptimeText(s.uptime)}
                  {s.incidents > 0 && <div className="small ink-3">{`${s.incidents} down`}</div>}
                </td>
                <td>
                  <div className="analytics-cell">
                    <span className="num">
                      {s.cpu_avg === null ? (
                        <span className="ink-3">—</span>
                      ) : (
                        <>
                          {n0(s.cpu_avg, '%')} <span className="ink-3">· {n0(s.cpu_peak, '%')}</span>
                        </>
                      )}
                    </span>
                    <Spark points={line(h.points, (p) => p.cpu, step)} unit="%" />
                  </div>
                </td>
                <td>
                  <div className="analytics-cell">
                    <span className="num">
                      {s.temp_avg === null ? (
                        <span className="ink-3">—</span>
                      ) : (
                        <>
                          {n0(s.temp_avg, '°')} <span className="ink-3">· {n0(s.temp_peak, '°')}</span>
                        </>
                      )}
                    </span>
                    <Spark points={line(h.points, (p) => p.temp, step)} unit="°" />
                  </div>
                </td>
                <td className="r num">{n0(s.mem_avg, '%')}</td>
                <td>
                  <DiskCell h={h} />
                </td>
                <td className="r num small">{s.rx_bytes + s.tx_bytes > 0 ? formatBytes(s.rx_bytes + s.tx_bytes) : '—'}</td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}

// Trends draws one metric for every server that has it, side by side on the same scale.
function Trends({ hosts, step }: { hosts: HostAnalytics[]; step: number }) {
  const [metric, setMetric] = useState<Metric>('cpu');
  const m = METRICS.find((x) => x.value === metric)!;
  const pick = (p: AnalyticsPoint) => p[metric];
  const shown = hosts.filter((h) => h.points.some((p) => pick(p) !== null));
  // One scale for all, so the charts compare at a glance.
  const top = m.max ?? Math.max(1, ...shown.flatMap((h) => h.points.map((p) => (metric === 'temp' ? (p.temp_max ?? p.temp ?? 0) : (pick(p) ?? 0))))) * 1.1;
  const options = METRICS.filter((x) => hosts.some((h) => h.points.some((p) => p[x.value] !== null)));
  return (
    <Section label="Trends" aside={<Seg label="Metric" value={metric} onChange={setMetric} options={options.map((o) => ({ value: o.value, label: o.label }))} />}>
      {shown.length === 0 ? (
        <p className="note">No {m.label.toLowerCase()} readings in this range.</p>
      ) : (
        <div className="analytics-multiples">
          {shown.map((h) => (
            <div key={h.id} className="analytics-multiple">
              <div className="analytics-multiple-head">
                <Link to={link(h)}>{h.name}</Link>
                <span className="small ink-3 num">
                  {metric === 'cpu' && `avg ${n0(h.summary.cpu_avg, '%')} · peak ${n0(h.summary.cpu_peak, '%')}`}
                  {metric === 'temp' && `avg ${n0(h.summary.temp_avg, '°C')} · peak ${n0(h.summary.temp_peak, '°C')}`}
                  {metric === 'mem' && `avg ${n0(h.summary.mem_avg, '%')} · peak ${n0(h.summary.mem_peak, '%')}`}
                  {metric === 'disk' && `now ${n0(h.summary.disk_now, '%')}${h.summary.disk_full_days !== null ? ` · ${fullIn(h.summary.disk_full_days)}` : ''}`}
                </span>
              </div>
              <TrendChart
                series={[{ label: m.label, color: 'var(--series-1)', points: line(h.points, pick, step) }]}
                max={top}
                format={(v) => `${Math.round(v)}${m.unit === 'ms' ? '' : m.unit}`}
                tip={(v) => `${v.toFixed(v < 10 ? 1 : 0)} ${m.unit}`}
                height={120}
              />
            </div>
          ))}
        </div>
      )}
    </Section>
  );
}

function Outages({ hosts, label }: { hosts: HostAnalytics[]; label: string }) {
  const all = hosts.flatMap((h) => h.incidents.map((i) => ({ ...i, host: h }))).sort((a, b) => b.start - a.start);
  return (
    <Section label="Outages" aside={<span className="small ink-3">a server missing most of a minute's checks</span>}>
      {all.length === 0 ? (
        <p className="note">No outages in the last {label}.</p>
      ) : (
        <table className="table">
          <tbody>
            {all.slice(0, 50).map((i) => (
              <tr key={`${i.host.id}-${i.start}`}>
                <td>
                  <Link to={link(i.host)}>{i.host.name}</Link>
                </td>
                <td className="num small">{when(i.start)}</td>
                <td className="r num small">{i.ongoing ? <span className="state crit">down now</span> : `down ${formatDuration(i.end - i.start)}`}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Section>
  );
}

export function AnalyticsPage() {
  const [range, setRange] = useState<AnalyticsRangeName>('24h');
  const fetch = useCallback(() => api.analytics(range), [range]);
  const { data, error } = usePoll(fetch, 60000);
  const label = RANGES.find((r) => r.value === range)!.label;
  const hosts = useMemo(() => data?.hosts ?? [], [data]);
  return (
    <main>
      <div className="page-title">
        <div>
          <h1>Analytics</h1>
          <div className="page-sub">Every server, this one included, over the last {label}. Kept a minute at a time.</div>
          <ServersTabs current="/servers/analytics" />
        </div>
        <Seg label="Range" value={range} onChange={setRange} options={RANGES} />
      </div>
      {error && !data ? (
        <div className="alert">{error.message}</div>
      ) : !data || data.range.name !== range ? (
        <Skeleton kind="dashboard" />
      ) : (
        <>
          {!data.kept && <div className="alert">The database is unavailable, so nothing is being kept.</div>}
          <Tiles hosts={hosts} label={label} />
          <Section label="Servers" aside={<span className="small ink-3">means and peaks over the {label}</span>}>
            <HostsTable hosts={hosts} step={data.step} />
          </Section>
          <Trends hosts={hosts} step={data.step} />
          <Outages hosts={hosts} label={label} />
        </>
      )}
    </main>
  );
}
