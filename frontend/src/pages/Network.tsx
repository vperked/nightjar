import { useCallback, useMemo, useRef, useState } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import type { NetInterface, NetSample, NetworkResponse, NetworkView, Sample } from '../api/types';
import { Num } from '../components/Num';
import { Select } from '../components/Select';
import { Skeleton } from '../components/Skeleton';
import { Legend, TrendChart } from '../components/TrendChart';
import { Section, Seg } from '../components/ui';
import { formatBytes, formatRate } from '../format';
import { usePoll } from '../hooks/usePoll';

// The Network page is a live meter in the spirit of nload: every second, per interface, with how
// busy the link is, and which programs and remote hosts the traffic belongs to.

const KEEP = 300; // five minutes at one sample a second
type Range = 'live' | '30m';

const mbit = (bps: number) => {
  const v = (bps * 8) / 1e6;
  return v >= 100 ? `${Math.round(v)} Mbit/s` : v >= 10 ? `${v.toFixed(1)} Mbit/s` : `${v.toFixed(2)} Mbit/s`;
};
const axis = (v: number) => formatRate(v).replace('/s', '');

// useNetwork polls once a second, asking only for samples newer than those it has.
function useNetwork() {
  const samples = useRef<Record<string, NetSample[]>>({});
  const history = useRef<Sample[]>([]);
  const fetchNetwork = useCallback(async (): Promise<NetworkResponse> => {
    const lasts = Object.values(samples.current)
      .map((s) => s.at(-1)?.ts ?? 0)
      .filter(Boolean);
    const since = lasts.length ? Math.min(...lasts) : undefined;
    const r = await api.network(since);
    if (r.history) history.current = r.history;
    if (r.network) {
      const merged: Record<string, NetSample[]> = {};
      for (const [name, fresh] of Object.entries(r.network.samples)) {
        const have = samples.current[name] ?? [];
        const last = have.at(-1)?.ts ?? 0;
        merged[name] = [...have, ...fresh.filter((s) => s.ts > last)].slice(-KEEP);
      }
      samples.current = merged;
      return { ...r, network: { ...r.network, samples: merged }, history: history.current };
    }
    return r;
  }, []);
  return usePoll(fetchNetwork, 1000);
}

function Direction({ label, color, rate, avg, peak, total, speed }: { label: string; color: string; rate: number; avg: number; peak: number; total: number; speed?: number }) {
  const used = speed ? Math.min(100, ((rate * 8) / (speed * 1e6)) * 100) : null;
  return (
    <div className="net-dir">
      <div className="net-dir-head">
        <span className="swatch" style={{ background: color }} />
        <span className="label">{label}</span>
      </div>
      <div className="net-rate num">
        <Num value={rate} format={formatRate} duration={700} />
      </div>
      <div className="readout-sub num">{mbit(rate)}</div>
      {used !== null && (
        <div className="net-link" title={`${used.toFixed(1)}% of the ${speed} Mbit/s link`}>
          <span className="net-link-bar">
            <span style={{ width: `${Math.max(used, rate > 0 ? 0.6 : 0)}%`, background: color }} />
          </span>
          <span className="num">{used < 1 ? used.toFixed(2) : used.toFixed(0)}% of link</span>
        </div>
      )}
      <dl className="net-stats num">
        <dt>avg</dt>
        <dd>{formatRate(avg)}</dd>
        <dt>peak</dt>
        <dd>{formatRate(peak)}</dd>
        <dt>since boot</dt>
        <dd>{formatBytes(total)}</dd>
      </dl>
    </div>
  );
}

function Bar({ value, max, color }: { value: number; max: number; color: string }) {
  return (
    <span className="net-bar" aria-hidden="true">
      <span style={{ width: `${value >= 1 && max > 0 ? Math.max(2, (value / max) * 100) : 0}%`, background: color }} />
    </span>
  );
}

// InterfaceCard is one interface at a glance: state, live rates with a sparkline, and totals.
function InterfaceCard({ i, samples, current, onPick }: { i: NetInterface; samples: NetSample[]; current: boolean; onPick: () => void }) {
  const series = useMemo(
    () => [
      { label: 'Down', color: 'var(--series-1)', points: samples.slice(-60).map((s) => ({ t: s.ts, v: s.rx })) },
      { label: 'Up', color: 'var(--series-2)', points: samples.slice(-60).map((s) => ({ t: s.ts, v: s.tx })) },
    ],
    [samples],
  );
  const meta = [
    i.default && 'default route',
    i.speed_mbps && (i.speed_mbps >= 1000 ? `${i.speed_mbps / 1000} Gbit/s` : `${i.speed_mbps} Mbit/s`),
    i.virtual && (i.name === 'lo' ? 'loopback' : 'virtual'),
  ].filter(Boolean);
  return (
    <button className={`iface ${current ? 'current' : ''} ${i.up ? '' : 'down'}`} onClick={onPick} aria-pressed={current}>
      <span className="iface-head">
        <span className="iface-name mono">{i.name}</span>
        <span className={`state ${i.up ? 'good' : 'idle'}`}>{i.up ? 'Up' : 'Down'}</span>
      </span>
      <span className="iface-meta">{meta.join(' · ') || '\u00a0'}</span>
      <span className="iface-rates num">
        <span>
          <span className="swatch" style={{ background: 'var(--series-1)' }} />↓ {formatRate(i.rx_bps)}
        </span>
        <span>
          <span className="swatch" style={{ background: 'var(--series-2)' }} />↑ {formatRate(i.tx_bps)}
        </span>
      </span>
      {i.up && samples.length > 1 ? <TrendChart series={series} height={34} compact format={formatRate} /> : <span className="iface-spark-empty" />}
      <span className="iface-totals num">
        since boot ↓ {formatBytes(i.rx_total)} · ↑ {formatBytes(i.tx_total)}
      </span>
      {(i.errors > 0 || i.drops > 0) && (
        <span className={`iface-issues num ${i.new_errors || i.new_drops ? 'fresh' : ''}`}>
          {[i.errors && `${i.errors.toLocaleString()} errors`, i.drops && `${i.drops.toLocaleString()} dropped packets`].filter(Boolean).join(' · ')}
          {i.new_errors || i.new_drops ? ' · new while watching' : ' since boot'}
        </span>
      )}
    </button>
  );
}

// active keeps the rows moving data (at least 1 B/s); with none, the five with most connections.
function active<T extends { rx_bps: number; tx_bps: number; connections: number }>(rows: T[]): { shown: T[]; idle: number } {
  const moving = rows.filter((r) => r.rx_bps + r.tx_bps >= 1);
  const shown = moving.length ? moving.slice(0, 12) : [...rows].sort((a, b) => b.connections - a.connections).slice(0, 5);
  return { shown, idle: rows.length - shown.length };
}

function Body({ net, history }: { net: NetworkView; history: Sample[] }) {
  const choices = net.interfaces.filter((i) => !i.virtual || i.up);
  const [picked, setPicked] = useState<string>();
  const [range, setRange] = useState<Range>('live');
  const iface: NetInterface | undefined = net.interfaces.find((i) => i.name === picked) ?? net.interfaces[0];
  const samples = iface ? (net.samples[iface.name] ?? []) : [];
  const series = useMemo(() => {
    const pts = range === 'live' ? samples.map((s) => ({ t: s.ts, rx: s.rx, tx: s.tx })) : history.map((s) => ({ t: s.ts, rx: s.rx_bps, tx: s.tx_bps }));
    return [
      { label: 'Download', color: 'var(--series-1)', points: pts.map((p) => ({ t: p.t, v: p.rx })) },
      { label: 'Upload', color: 'var(--series-2)', points: pts.map((p) => ({ t: p.t, v: p.tx })) },
    ];
  }, [samples, history, range]);
  const maxProc = Math.max(1, ...net.processes.map((p) => Math.max(p.rx_bps, p.tx_bps)));
  const maxHost = Math.max(1, ...net.hosts.map((h) => Math.max(h.rx_bps, h.tx_bps)));
  // Rows moving data come first; idle ones fold into a count unless nothing is moving at all.
  const procs = active(net.processes);
  const hosts = active(net.hosts);

  if (!iface) return <p className="note">No network interfaces found.</p>;
  return (
    <>
      <div className="page-title">
        <div className="min0">
          <h1 className="title-with-badge">
            Network
            <span className="live-badge" title="Updates every second while this page is open">
              <span className="live-dot" aria-hidden="true" />
              Live
            </span>
          </h1>
          <div className="page-sub">
            <span className="mono">{iface.name}</span>
            {iface.speed_mbps ? ` · ${iface.speed_mbps >= 1000 ? `${iface.speed_mbps / 1000} Gbit/s` : `${iface.speed_mbps} Mbit/s`} link` : ''}
            {iface.default ? ' · default route' : ''}
            {!iface.up ? ' · down' : ''} · live, every second
          </div>
        </div>
        {choices.length > 1 && (
          <div className="toolbar">
            <Select
              label="Interface"
              value={iface.name}
              onChange={setPicked}
              options={choices.map((i) => ({ value: i.name, text: i.name, label: `${i.name}${i.default ? ' (default)' : ''}${!i.up ? ' · down' : ''}` }))}
            />
          </div>
        )}
      </div>

      <section className="panel net-live">
        <div className="net-dirs">
          <Direction label="Download" color="var(--series-1)" rate={iface.rx_bps} avg={iface.rx_avg} peak={iface.rx_peak} total={iface.rx_total} speed={iface.speed_mbps} />
          <Direction label="Upload" color="var(--series-2)" rate={iface.tx_bps} avg={iface.tx_avg} peak={iface.tx_peak} total={iface.tx_total} speed={iface.speed_mbps} />
        </div>
        <div className="net-chart">
          <div className="section-head">
            <Legend series={series} />
            <Seg<Range>
              label="Range"
              value={range}
              onChange={setRange}
              options={[
                { value: 'live', label: '5 min live' },
                { value: '30m', label: '30 min' },
              ]}
            />
          </div>
          <TrendChart series={series} height={210} format={axis} tip={formatRate} emptyText="measuring…" />
          <div className="chart-note small ink-3 num">
            {range === 'live' ? `${iface.rx_pps.toFixed(0)} packets/s in · ${iface.tx_pps.toFixed(0)} out` : 'all interfaces, every monitoring cycle'}
            {iface.new_errors || iface.new_drops ? (
              <span className="net-warn">
                {' '}
                · {iface.new_errors ?? 0} errors and {iface.new_drops ?? 0} drops since you opened this page
              </span>
            ) : null}
          </div>
        </div>
      </section>

      <div className="columns net-columns">
        <Section label="Programs" aside={<span className="small ink-3 num">{net.connections} connections</span>}>
          {net.processes.length === 0 ? (
            <p className="note" style={{ padding: 0 }}>
              No connections to other machines right now.
            </p>
          ) : (
            <table className="table net-table">
              <thead>
                <tr>
                  <th>Program</th>
                  <th className="r">Down</th>
                  <th className="r">Up</th>
                </tr>
              </thead>
              <tbody>
                {procs.shown.map((p) => (
                  <tr key={p.name}>
                    <td className="fill">
                      <div className="truncate" title={p.pids?.length ? `pid ${p.pids.join(', ')}` : undefined}>
                        {p.name}
                      </div>
                      <div className="sub truncate">
                        {[p.unit && `${p.unit}.service`, `${p.connections} conn`, p.source === 'qbittorrent' ? 'speeds from qBittorrent (includes uTP)' : ''].filter(Boolean).join(' · ')}
                      </div>
                    </td>
                    <td className="r num shrink">
                      <div>{formatRate(p.rx_bps)}</div>
                      <Bar value={p.rx_bps} max={maxProc} color="var(--series-1)" />
                    </td>
                    <td className="r num shrink">
                      <div>{formatRate(p.tx_bps)}</div>
                      <Bar value={p.tx_bps} max={maxProc} color="var(--series-2)" />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          <p className="chart-note small ink-3">{procs.idle ? `${procs.idle} more with open connections but no traffic · ` : ''}TCP traffic, by program; traffic within this machine is left out.</p>
        </Section>
        <Section label="Remote hosts" aside={hosts.idle ? <span className="small ink-3">{hosts.idle} idle not shown</span> : undefined}>
          {net.hosts.length === 0 ? (
            <p className="note" style={{ padding: 0 }}>
              Nothing connected.
            </p>
          ) : (
            <table className="table net-table">
              <thead>
                <tr>
                  <th>Host</th>
                  <th className="r">Down</th>
                  <th className="r">Up</th>
                </tr>
              </thead>
              <tbody>
                {hosts.shown.map((h) => (
                  <tr key={h.peer + h.process}>
                    <td className="fill">
                      <div className="mono truncate">{h.peer}</div>
                      <div className="sub truncate">
                        {h.process} · {h.connections} conn
                      </div>
                    </td>
                    <td className="r num shrink">
                      <div>{formatRate(h.rx_bps)}</div>
                      <Bar value={h.rx_bps} max={maxHost} color="var(--series-1)" />
                    </td>
                    <td className="r num shrink">
                      <div>{formatRate(h.tx_bps)}</div>
                      <Bar value={h.tx_bps} max={maxHost} color="var(--series-2)" />
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </Section>
      </div>

      <Section label="All interfaces" aside={<span className="small ink-3">click one to watch it</span>}>
        <div className="ifaces">
          {net.interfaces.map((i) => (
            <InterfaceCard key={i.name} i={i} samples={net.samples[i.name] ?? []} current={i.name === iface.name} onPick={() => setPicked(i.name)} />
          ))}
        </div>
      </Section>
    </>
  );
}

export function NetworkPage() {
  const poll = useNetwork();
  return (
    <main>
      {poll.error && !poll.unauthorized && <div className="alert">Can't reach the monitor ({poll.error.message}). Retrying.</div>}
      {!poll.data ? (
        <Skeleton kind="dashboard" />
      ) : poll.data.enabled && poll.data.network ? (
        <Body net={poll.data.network} history={poll.data.history ?? []} />
      ) : (
        <p className="note">
          The live meter is not available. <Link to="/">Back to overview</Link>
        </p>
      )}
    </main>
  );
}
