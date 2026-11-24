import { useCallback, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { api, errorText } from '../api/client';
import type { NetInterface, NetSample, NetworkResponse, NetworkView, Sample } from '../api/types';
import { Num } from '../components/Num';
import { Select } from '../components/Select';
import { Skeleton } from '../components/Skeleton';
import { AttackWatch } from '../components/Attacks';
import { useToast } from '../components/Toasts';
import { useNow } from '../hooks/useNow';
import { Legend, TrendChart } from '../components/TrendChart';
import { NetworkTabs, Section, Seg } from '../components/ui';
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

// failuresBeforeAlert is how many polls in a row must fail before the page says so: one lost poll in
// a second-by-second meter is a blip, and the last reading stays on screen through it.
const failuresBeforeAlert = 3;

// HISTORY_EVERY is how often the half-hour view is fetched again. The monitor sends it only to a poll
// without `since`, so asking for newer samples alone would leave it as it was when the page opened.
const HISTORY_EVERY = 15_000;

// useNetwork polls once a second, asking only for samples newer than those it has, and every
// HISTORY_EVERY for everything, which brings the half hour up to date. failures counts the polls that
// failed in a row.
function useNetwork() {
  const samples = useRef<Record<string, NetSample[]>>({});
  const history = useRef<Sample[]>([]);
  const historyAt = useRef(0);
  const [failures, setFailures] = useState(0);
  const fetchNetwork = useCallback(async (): Promise<NetworkResponse> => {
    const lasts = Object.values(samples.current)
      .map((s) => s.at(-1)?.ts ?? 0)
      .filter(Boolean);
    const stale = Date.now() - historyAt.current >= HISTORY_EVERY;
    const since = lasts.length && !stale ? Math.min(...lasts) : undefined;
    let r: NetworkResponse;
    try {
      r = await api.network(since);
    } catch (err) {
      setFailures((n) => n + 1);
      throw err;
    }
    setFailures(0);
    if (since === undefined) historyAt.current = Date.now();
    if (r.history) history.current = r.history;
    if (r.network) {
      // A monitor from before this sent null for an empty list (an interface with nothing new).
      const merged: Record<string, NetSample[]> = {};
      for (const [name, fresh] of Object.entries(r.network.samples ?? {})) {
        const have = samples.current[name] ?? [];
        const last = have.at(-1)?.ts ?? 0;
        merged[name] = [...have, ...(fresh ?? []).filter((s) => s.ts > last)].slice(-KEEP);
      }
      samples.current = merged;
      const n = r.network;
      return { ...r, network: { ...n, interfaces: n.interfaces ?? [], processes: n.processes ?? [], hosts: n.hosts ?? [], samples: merged }, history: history.current };
    }
    return r;
  }, []);
  return { ...usePoll(fetchNetwork, 1000), failures };
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
            <span className="net-fill" style={fill(Math.max(used, rate > 0 ? 0.6 : 0), color)} />
          </span>
          <span className="num">{used < 1 ? used.toFixed(2) : used.toFixed(0)}% of link</span>
        </div>
      )}
      <dl className="net-stats num">
        <div>
          <dt>avg</dt>
          <dd>{formatRate(avg)}</dd>
        </div>
        <div>
          <dt>peak</dt>
          <dd>{formatRate(peak)}</dd>
        </div>
        <div>
          <dt>since boot</dt>
          <dd>{formatBytes(total)}</dd>
        </div>
      </dl>
    </div>
  );
}

// fill is a bar's fill at a percent: the whole bar slid left, so the browser moves it without laying
// the page out again. These bars change every second, and growing their width reflowed the page on
// every frame of each change.
const fill = (percent: number, color: string) => ({ transform: `translateX(${Math.min(100, percent) - 100}%)`, background: color });

function Bar({ value, max, color }: { value: number; max: number; color: string }) {
  return (
    <span className="net-bar" aria-hidden="true">
      <span className="net-fill" style={fill(value >= 1 && max > 0 ? Math.max(2, (value / max) * 100) : 0, color)} />
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

// ifaceRank orders the cards: real links first, then virtual ones, loopback, and what is down last.
function ifaceRank(i: NetInterface): number {
  if (!i.up) return 3;
  if (i.name === 'lo') return 2;
  return i.virtual ? 1 : 0;
}

// HOLD is how long the program and host lists keep their order, and keep a row that went quiet.
// The monitor re-sorts them every second, which would shuffle the rows under the reader's eye.
const HOLD = 10_000;

type Rate = { rx_bps: number; tx_bps: number; connections: number };

// useSteady keeps rows moving data (at least 1 B/s), or with none, the five with most connections.
// Rows keep their places for HOLD, new ones join at the bottom, and a row that stops stays that
// long too; then the list is put in order again, busiest first.
function useSteady<T extends Rate>(rows: T[], key: (r: T) => string): { shown: T[]; idle: number } {
  const memo = useRef({ order: [] as string[], at: 0, seen: new Map<string, number>() });
  return useMemo(() => {
    const m = memo.current;
    const now = Date.now();
    for (const r of rows) if (r.rx_bps + r.tx_bps >= 1) m.seen.set(key(r), now);
    for (const [k, t] of m.seen) if (now - t > HOLD) m.seen.delete(k);
    let live = rows.filter((r) => m.seen.has(key(r)));
    if (!live.length) live = [...rows].sort((a, b) => b.connections - a.connections).slice(0, 5);
    const byKey = new Map(live.map((r) => [key(r), r]));
    if (now - m.at > HOLD) {
      m.order = live.map(key); // the monitor's order: busiest first
      m.at = now;
    } else {
      m.order = [...m.order.filter((k) => byKey.has(k)), ...live.map(key).filter((k) => !m.order.includes(k))];
    }
    const shown = m.order.slice(0, 12).map((k) => byKey.get(k)!);
    return { shown, idle: rows.length - shown.length };
  }, [rows, key]);
}

const procKey = (p: { name: string }) => p.name;
const hostKey = (h: { peer: string; process?: string }) => `${h.peer} ${h.process ?? ''}`;

function Body({ net, history }: { net: NetworkView; history: Sample[] }) {
  const [picked, setPicked] = useState<string>();
  // The attack watch leads the page during an attack; while calm it waits at the foot.
  const attacks = useLocalAttacks();
  const cards = useMemo(() => [...net.interfaces].sort((a, b) => ifaceRank(a) - ifaceRank(b)), [net.interfaces]);
  const [range, setRange] = useState<Range>('live');
  const iface: NetInterface | undefined = net.interfaces.find((i) => i.name === picked) ?? net.interfaces[0];
  // Down virtual interfaces stay out of the menu, unless one is being watched (picked from its card).
  const choices = net.interfaces.filter((i) => !i.virtual || i.up || i === iface);
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
  // Rows moving data are shown, held in place a while; idle ones fold into a count.
  const procs = useSteady(net.processes, procKey);
  const hosts = useSteady(net.hosts, hostKey);

  // The meter has just woken and not read the interfaces yet: a moment, not "none".
  if (!iface) return Date.now() / 1000 - net.since < 10 ? <Skeleton kind="dashboard" /> : <p className="note">No network interfaces found.</p>;
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
            {!iface.up ? ' · down' : ''}
          </div>
          <NetworkTabs current="/network" />
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

      {attacks.attacking && attacks.node}

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
          <TrendChart series={series} height={220} format={formatRate} emptyText="measuring…" />
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
                      <div className="sub truncate" title={p.source === 'qbittorrent' ? 'Speeds come from qBittorrent itself, so they include its uTP (UDP) traffic.' : undefined}>
                        {[p.unit && `${p.unit}.service`, `${p.connections} conn`, p.source === 'qbittorrent' ? 'via qBittorrent' : ''].filter(Boolean).join(' · ')}
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
          {cards.map((i) => (
            <InterfaceCard key={i.name} i={i} samples={net.samples[i.name] ?? []} current={i.name === iface.name} onPick={() => setPicked(i.name)} />
          ))}
        </div>
      </Section>

      {!attacks.attacking && attacks.node}
    </>
  );
}

// useLocalAttacks is the watch for attacks on this machine, every 5 seconds, and whether one is on.
function useLocalAttacks(): { node: ReactNode; attacking: boolean } {
  const { data, refresh } = usePoll(api.attacks, 5000);
  const now = useNow(5000);
  const notify = useToast();
  if (!data?.enabled || !data.attacks) return { node: null, attacking: false };
  const toggle = async (on: boolean) => {
    try {
      await api.setDefense(on);
      notify(
        on
          ? { tone: 'ok', title: 'Defending against attacks', body: 'Nightjar answers the next one in the firewall.' }
          : { tone: 'ok', title: 'Defense off', body: 'Attacks are still watched and announced.' },
      );
      refresh();
    } catch (e) {
      notify({ tone: 'error', title: 'Could not change the defense', body: errorText(e) });
    }
  };
  const unblock = async (address: string) => {
    try {
      await api.unblock(address);
      notify({ tone: 'ok', title: `${address} unblocked`, body: 'Left alone for an hour.' });
      refresh();
    } catch (e) {
      notify({ tone: 'error', title: `Could not unblock ${address}`, body: errorText(e) });
    }
  };
  return { node: <AttackWatch view={data.attacks} now={now} onToggle={toggle} onUnblock={unblock} />, attacking: data.attacks.attacking };
}

export function NetworkPage() {
  const poll = useNetwork();
  return (
    <main>
      {poll.error && !poll.unauthorized && (poll.failures >= failuresBeforeAlert || !poll.data) && <div className="alert">Can't reach the monitor ({poll.error.message}). Retrying.</div>}
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
