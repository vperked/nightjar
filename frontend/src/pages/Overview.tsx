import { useCallback, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { api } from '../api/client';
import { useCharts } from '../appearance';
import { useServices } from '../api/ServicesContext';
import type { Process, Sample, Stats } from '../api/types';
import { LatestIncident } from '../components/LatestIncident';
import { Num } from '../components/Num';
import { PlexSummary } from '../components/PlexNow';
import { PluginCards } from '../components/PluginCards';
import { RemoteSummary } from '../components/RemoteSummary';
import { useShown } from '../components/Sections';
import { Skeleton } from '../components/Skeleton';
import { Legend, TrendChart, type Point, type Scrub } from '../components/TrendChart';
import { Bar, Pct, Section, State, Ticks } from '../components/ui';
import { formatBytes, formatRate, formatResponse, formatUptime } from '../format';
import { usePoll } from '../hooks/usePoll';
import { DrivesSection } from './overview/Drives';
import { HealthSummary, ServiceProblems } from './overview/Health';

const pct = (v: number) => `${Math.round(v)}%`;

// Gauge is a tile's fill as a block of dots (or a row of segments, drawn as lines), for a reading
// that changes too slowly to chart.
function Gauge({ percent }: { percent: number }) {
  const v = Math.min(100, Math.max(0, percent));
  return (
    <div className={`readout-gauge ${percent >= 90 ? 'crit' : percent >= 75 ? 'warn' : ''}`} role="meter" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(percent)} aria-label="Used">
      {/* The stylesheet rounds the fill to whole columns of dots, or segments. */}
      <span style={{ ['--v' as string]: v }} />
    </div>
  );
}

// Hovering a card's sparkline scrubs through it: the headline shows the value at that moment and
// the line below says when, instead of a tooltip covering the card.
export function Readout({
  label,
  value,
  unit,
  sub,
  points,
  color = 'var(--series-1)',
  gauge = false,
}: {
  label: string;
  value: number;
  unit?: string;
  sub: string;
  points: Point[];
  color?: string;
  gauge?: boolean;
}) {
  const [scrub, setScrub] = useState<Scrub | null>(null);
  const v = scrub?.values[0];
  return (
    <div className={`readout ${scrub ? 'scrubbing' : ''}`} style={{ ['--metric' as string]: color }}>
      <span className="label">{label}</span>
      <div className="readout-value">
        {scrub ? v === null || v === undefined ? '--' : v.toFixed(1) : <Num value={value} format={(n) => n.toFixed(1)} />}
        {unit && <span className="unit">{unit}</span>}
      </div>
      <div className="readout-sub">{scrub ? `at ${scrub.time}` : sub}</div>
      {gauge ? <Gauge percent={value} /> : <TrendChart series={[{ label, color, points }]} height={60} compact format={pct} onScrub={setScrub} />}
    </div>
  );
}

function NetworkReadout({ stats, rx, tx }: { stats: Stats; rx: Point[]; tx: Point[] }) {
  const [scrub, setScrub] = useState<Scrub | null>(null);
  const charts = useCharts();
  const bw = stats.network.bandwidth;
  const series = [
    { label: 'In', color: 'var(--series-1)', points: rx },
    { label: 'Out', color: 'var(--series-2)', points: tx },
  ];
  const rate = (i: number, now: number | undefined) => {
    if (scrub) {
      const v = scrub.values[i];
      return v === null || v === undefined ? '--' : formatRate(v);
    }
    return now === undefined ? '--' : <Num value={now} format={formatRate} />;
  };
  return (
    <div className={`readout ${scrub ? 'scrubbing' : ''}`}>
      <div className="readout-head">
        <Link to="/network" className="label readout-link" title="Live network meter">
          Network →
        </Link>
        {scrub ? (
          <span className="readout-sub">at {scrub.time}</span>
        ) : charts === 'dots' ? (
          // In grows up from the middle of the chart and out grows down.
          <span className="legend">
            <span>↑ in</span>
            <span>↓ out</span>
          </span>
        ) : (
          <Legend series={series} />
        )}
      </div>
      <div className="net-pair">
        <div className="readout-value">
          <span className="unit">in </span>
          {rate(0, bw?.bytes_recv_per_sec)}
        </div>
        <div className="readout-value">
          <span className="unit">out </span>
          {rate(1, bw?.bytes_sent_per_sec)}
        </div>
      </div>
      <TrendChart series={series} height={60} compact format={formatRate} onScrub={setScrub} />
    </div>
  );
}

function ServicesSection() {
  const { data } = useServices();
  const navigate = useNavigate();
  if (!data) return null;
  if (data.services.length === 0) {
    return (
      <Section label="Services">
        <p className="note" style={{ padding: 0 }}>
          No uptime checks yet. <Link to="/services#add">Add one →</Link>
        </p>
      </Section>
    );
  }
  const list = data.services;
  // Counted as the health badge counts: paused checks are not watched, and only down is not up.
  const watched = list.filter((s) => s.status !== 'paused');
  const up = watched.filter((s) => s.status !== 'down').length;
  return (
    <Section
      label="Services"
      aside={
        <span className="small ink-3">
          <span className="num">
            {up}/{watched.length}
          </span>{' '}
          up · <Link to="/services">all services →</Link>
        </span>
      }
    >
      <table className="table">
        <thead>
          <tr>
            <th className="shrink hide-sm">State</th>
            <th>Service</th>
            <th className="hide-sm">Last 30 checks</th>
            <th className="r hide-sm">Resp.</th>
            <th className="r">24h</th>
          </tr>
        </thead>
        <tbody>
          {list.slice(0, 8).map((s) => (
            <tr key={s.name} className="link" onClick={(e) => !(e.target as Element).closest('a') && navigate(`/services/${encodeURIComponent(s.name)}`)}>
              <td className="shrink hide-sm">
                <State status={s.status} />
              </td>
              <td className="name-cell">
                <Link to={`/services/${encodeURIComponent(s.name)}`} className="svc-name truncate" style={{ display: 'block' }}>
                  {s.name}
                </Link>
                <div className="show-sm svc-sm">
                  <State status={s.status} />
                  <Ticks beats={s.recent} slots={30} />
                </div>
              </td>
              <td className="ticks-cell hide-sm">
                <Ticks beats={s.recent} slots={30} />
              </td>
              <td className="r num hide-sm">{formatResponse(s)}</td>
              <td className="r">
                <Pct value={s.uptime_24h} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </Section>
  );
}

function Processes({ stats }: { stats: Stats }) {
  const rows = (procs: Process[] | null, field: 'cpu_percent' | 'memory_percent') =>
    (procs ?? []).map((p) => (
      <tr key={`${field}${p.pid}`}>
        <td className="fill">
          <span className="truncate" style={{ display: 'block' }} title={p.name}>
            {p.name}
          </span>
        </td>
        <td className="r num ink-3 shrink">{p.pid}</td>
        <td className="r num shrink">{p[field].toFixed(1)}%</td>
      </tr>
    ));
  return (
    <Section label="Processes" aside={<span className="small ink-3 num">{stats.processes.total} running</span>}>
      <div className="proc-grid">
        <div className="min0">
          <table className="table">
            <thead>
              <tr>
                <th>By CPU</th>
                <th className="r">PID</th>
                <th className="r">CPU</th>
              </tr>
            </thead>
            <tbody>{rows(stats.processes.top_cpu, 'cpu_percent')}</tbody>
          </table>
        </div>
        <div className="min0">
          <table className="table">
            <thead>
              <tr>
                <th>By memory</th>
                <th className="r">PID</th>
                <th className="r">Mem</th>
              </tr>
            </thead>
            <tbody>{rows(stats.processes.top_memory, 'memory_percent')}</tbody>
          </table>
        </div>
      </div>
    </Section>
  );
}

function Body({ stats }: { stats: Stats }) {
  const h = stats.history ?? [];
  const s = stats.system;
  const cores = stats.cpu.per_core ?? [];
  // Shared by the summary, the attention card and the drives card.
  const { data: units } = usePoll(api.units, 30000);
  const { data: drives } = usePoll(api.drives, 60000);
  const shown = useShown();
  return (
    <>
      <div className="page-title">
        <div className="min0">
          <h1 className="truncate">{s.hostname}</h1>
          <div className="page-sub">
            {s.platform_version || s.platform} · up {formatUptime(s.uptime_seconds)}
          </div>
          <HealthSummary units={units} drives={drives} />
        </div>
      </div>
      <div className="readouts">
        <Readout
          label="CPU"
          value={stats.cpu.percent}
          unit="%"
          sub={`${stats.cpu.cores} cores${stats.cpu.load ? ` · load ${stats.cpu.load[0].toFixed(2)}` : ''}`}
          points={h.map((p) => ({ t: p.ts, v: p.cpu }))}
        />
        <Readout
          label="Memory"
          value={stats.memory.percent}
          unit="%"
          sub={`${formatBytes(stats.memory.used)} / ${formatBytes(stats.memory.total)}`}
          points={h.map((p) => ({ t: p.ts, v: p.memory }))}
        />
        <Readout label="Disk" value={stats.disk.total.percent} unit="%" sub={`${formatBytes(stats.disk.total.free)} free`} points={h.map((p) => ({ t: p.ts, v: p.disk }))} gauge />
        <NetworkReadout stats={stats} rx={h.map((p) => ({ t: p.ts, v: p.rx_bps }))} tx={h.map((p) => ({ t: p.ts, v: p.tx_bps }))} />
      </div>

      <LatestIncident />
      {shown('attention') && <ServiceProblems data={units} />}
      <div className="columns">
        <div className="col">
          {shown('services') && <ServicesSection />}
          {shown('remote') && shown('servers') && <RemoteSummary />}
          {shown('plex') && <PlexSummary />}
          <PluginCards />
          {shown('processes') && <Processes stats={stats} />}
        </div>
        <div className="col">
          {shown('host') && (
            <Section label="Host">
              <dl className="kv">
                <dt>Hostname</dt>
                <dd>{s.hostname}</dd>
                <dt>OS</dt>
                <dd title={s.platform_version}>{s.platform_version || s.platform}</dd>
                <dt>Kernel</dt>
                <dd>{s.platform_release}</dd>
                <dt>Uptime</dt>
                <dd>{formatUptime(s.uptime_seconds)}</dd>
                <dt>Load 1/5/15</dt>
                <dd>{stats.cpu.load ? stats.cpu.load.map((l) => l.toFixed(2)).join(' ') : '—'}</dd>
                {stats.temperature !== null && (
                  <>
                    <dt>CPU temp</dt>
                    <dd>{stats.temperature.toFixed(1)} °C</dd>
                  </>
                )}
                {stats.fans.slice(0, 2).map((f) => (
                  <Fan key={f.label} label={f.label} rpm={f.speed_rpm} />
                ))}
                {stats.gpu && (
                  <>
                    <dt>GPU</dt>
                    <dd title={stats.gpu.name}>
                      {stats.gpu.utilization.toFixed(0)}% · {stats.gpu.temperature.toFixed(0)} °C
                    </dd>
                  </>
                )}
              </dl>
            </Section>
          )}
          {shown('cores') && cores.length > 1 && (
            <Section label="Cores" aside={<span className="small ink-3">% busy, now</span>}>
              <div className="cores">
                {cores.map((v, i) => (
                  <div key={i} className="core" title={`core ${i}: ${v.toFixed(1)}%`}>
                    <span style={{ height: `${Math.max(1, Math.min(100, v))}%` }} />
                  </div>
                ))}
              </div>
              <div className="core-axis">
                <span>0</span>
                <span>{cores.length - 1}</span>
              </div>
            </Section>
          )}
          {shown('storage') && (
            <Section label="Storage">
              <div className="storage">
                {stats.disk.partitions.map((p) => (
                  <div key={p.mountpoint} className="storage-row">
                    <div className="storage-line">
                      <span className="mono truncate" title={`${p.device} · ${p.fstype}`}>
                        {p.mountpoint}
                      </span>
                      <span className="num ink-3 small" style={{ whiteSpace: 'nowrap' }}>
                        <span style={{ color: 'var(--ink)' }}>{p.percent.toFixed(0)}%</span> of {formatBytes(p.total)}
                      </span>
                    </div>
                    <Bar percent={p.percent} label={`${p.mountpoint} usage`} />
                  </div>
                ))}
              </div>
            </Section>
          )}
          {shown('drives') && <DrivesSection data={drives} />}
          {shown('interfaces') && (
            <Section label="Interfaces">
              <table className="table">
                <tbody>
                  {(stats.network.interfaces ?? []).map((i) => (
                    <tr key={i.name}>
                      <td className="mono">{i.name}</td>
                      <td className="r num ink-3 small">{(i.addresses ?? []).filter((a) => !a.includes(':')).join(', ') || '—'}</td>
                      <td className={`r mono small ${i.is_up ? 'pct-good' : 'ink-3'}`}>{i.is_up ? 'up' : 'down'}</td>
                    </tr>
                  ))}
                </tbody>
              </table>
            </Section>
          )}
        </div>
      </div>
    </>
  );
}

function Fan({ label, rpm }: { label: string; rpm: number }) {
  return (
    <>
      <dt>{label}</dt>
      <dd>{rpm.toLocaleString()} rpm</dd>
    </>
  );
}

// maxSamples matches the backend's half hour of samples.
const maxSamples = 360;

// useStats polls /api/stats, asking only for history newer than what it already has.
function useStats() {
  const history = useRef<Sample[]>([]);
  const fetchStats = useCallback(async () => {
    const last = history.current.at(-1)?.ts;
    const s = await api.stats(last);
    const fresh = last === undefined ? s.history : s.history.filter((x) => x.ts > last);
    history.current = [...history.current, ...fresh].slice(-maxSamples);
    return { ...s, history: history.current };
  }, []);
  return usePoll(fetchStats, 5000);
}

export function Overview() {
  const stats = useStats();
  return (
    <main>
      {stats.error && !stats.unauthorized && (
        <div className="alert" role="alert">
          Can't reach the monitor ({stats.error.message}). Retrying.
        </div>
      )}
      {stats.data ? <Body stats={stats.data} /> : <Skeleton kind="dashboard" />}
    </main>
  );
}
