import { useState } from 'react';
import type { Attack, AttackDefense, AttackFinding, AttackMethod, AttackSource, AttackView } from '../api/types';
import { formatAgo, formatRate } from '../format';
import { Section } from './ui';

// The methods, for agents' views (which leave the list out) and before the first answer.
export const ATTACK_METHODS: AttackMethod[] = [
  {
    id: 'syn_flood',
    name: 'SYN flood',
    how: 'Opens TCP connections and never finishes them, to fill the queue of connections waiting to be accepted so real visitors are turned away.',
    unit: 'per second',
  },
  { id: 'udp_flood', name: 'UDP flood', how: 'Sprays UDP packets, often at ports nothing listens on, to use up bandwidth and the CPU spent answering them.', unit: 'packets/s' },
  { id: 'icmp_flood', name: 'Ping flood', how: 'Sends ping requests faster than they can be answered, to use up bandwidth and CPU.', unit: 'pings/s' },
  { id: 'connection_flood', name: 'Connection flood', how: 'Opens and holds a great many connections, often from few addresses, until the server runs out of room for more.', unit: 'connections' },
  { id: 'http_flood', name: 'HTTP flood', how: 'Sends web requests that look ordinary, many times a second, to keep the web server and the apps behind it busy.', unit: 'requests/s' },
  { id: 'volumetric', name: 'Volumetric', how: 'Sends more traffic than the line can carry, so everything on it slows or stops.', unit: 'of the link' },
];

const methodOf = (id: string, methods: AttackMethod[]) => methods.find((m) => m.id === id) ?? { id, name: id, how: '', unit: '' };

const big = (v: number) => (v > 0 && v < 1 ? 'under 1' : v >= 1e6 ? `${(v / 1e6).toFixed(1)}M` : v >= 1e4 ? `${Math.round(v / 1e3)}k` : v >= 1e3 ? `${(v / 1e3).toFixed(1)}k` : `${Math.round(v)}`);

// strength says a finding in its method's unit: "9.0k per second", "92% of the link".
export function strength(f: AttackFinding, methods = ATTACK_METHODS): string {
  const m = methodOf(f.method, methods);
  return f.method === 'volumetric' ? `${Math.round(f.value * 100)}% ${m.unit}` : `${big(f.value)} ${m.unit}`;
}

// describe names an attack: its exact kinds when known ("DNS amplification and SYN flood"), else its
// methods.
export function describe(a: Attack, methods = ATTACK_METHODS): string {
  const names = a.kinds?.length ? a.kinds.map((k) => k.name) : a.methods.map((f) => methodOf(f.method, methods).name);
  return names.length <= 1 ? (names[0] ?? 'an attack') : `${names.slice(0, -1).join(', ')} and ${names.at(-1)}`;
}

const lasted = (a: Attack, now: number) => {
  const s = Math.max(0, (a.end || now) - a.start);
  return s >= 3600 ? `${Math.floor(s / 3600)}h ${Math.round((s % 3600) / 60)}m` : s >= 60 ? `${Math.floor(s / 60)}m ${Math.round(s % 60)}s` : `${Math.round(s)}s`;
};

function Sources({ sources, blocked }: { sources: AttackSource[]; blocked: Set<string> }) {
  if (!sources.length) return null;
  return (
    <div className="attack-sources">
      <div className="attack-list-head">Most involved</div>
      <ul className="attack-list">
        {sources.slice(0, 8).map((s) => (
          <li key={s.address} className="attack-row">
            <span className="attack-row-who min0">
              <span className="mono">{s.address}</span>
              {s.network && <span className="attack-network"> {s.network}: many visitors, never blocked</span>}
              {s.ssh && <span className="attack-network"> logged in over SSH, never blocked</span>}
              {blocked.has(s.address) && <span className="attack-blocked-tag"> blocked</span>}
            </span>
            <span className="attack-row-nums">{counts(s)}</span>
          </li>
        ))}
      </ul>
    </div>
  );
}

// counts says what one address is doing, leaving out what it is not: "720 half-open · 3 connections".
const counts = (s: AttackSource) =>
  [
    s.syn_recv && `${s.syn_recv.toLocaleString()} half-open`,
    s.established && `${s.established.toLocaleString()} connection${s.established === 1 ? '' : 's'}`,
    s.requests && `${s.requests.toLocaleString()} request${s.requests === 1 ? '' : 's'}`,
  ]
    .filter(Boolean)
    .join(' · ') || '—';

// left says how long a block has to run: "12m more".
const left = (until: number, now: number) => {
  const m = Math.max(1, Math.ceil((until - now) / 60));
  return m >= 60 ? `${Math.round(m / 60)}h more` : `${m}m more`;
};

// Defense shows what the firewall is doing about attacks: the switch, the measures in force, the
// addresses blocked (each can be let back in), and what it cannot do. Without onToggle and onUnblock it
// is read-only, as for an agent, whose switch is AGENT_DEFEND in its settings file.
function Defense({
  d,
  attacking,
  now,
  onToggle,
  onUnblock,
}: {
  d: AttackDefense;
  attacking: boolean;
  now: number;
  onToggle?: (on: boolean) => Promise<void>;
  onUnblock?: (address: string) => Promise<void>;
}) {
  const [busy, setBusy] = useState('');
  const act = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    try {
      await fn();
    } finally {
      setBusy('');
    }
  };
  const toggle = onToggle && (
    <button type="button" className="btn-quiet" disabled={!!busy} onClick={() => act('toggle', () => onToggle(!d.on))}>
      {busy === 'toggle' ? 'Saving…' : d.on ? 'Turn off' : 'Turn on'}
    </button>
  );
  const state = d.problem ? 'Can’t defend here' : !d.on ? 'Off: attacks are only watched' : d.active ? 'Defending' : 'On, ready';
  return (
    <div className={`attack-defense ${d.active ? 'active' : ''} ${d.problem || !d.on ? 'idle' : ''}`}>
      <div className="attack-defense-head">
        <span className="label">Defense</span>
        <span className="attack-defense-state">{state}</span>
        {toggle}
      </div>
      {d.problem ? (
        <p className="small">{d.problem}</p>
      ) : !d.on ? (
        <p className="small ink-3">{onToggle ? 'Turned on, Nightjar answers an attack in the firewall while it lasts.' : 'Switched off on this server (AGENT_DEFEND=off).'}</p>
      ) : !d.active && !attacking ? (
        <p className="small ink-3">
          When an attack starts, Nightjar adds firewall rules suited to it and blocks the addresses doing the most, then takes them away when it is over. Your own networks, Cloudflare, your servers
          and whoever is logged in over SSH are never blocked.
        </p>
      ) : null}
      {d.measures.length > 0 && (
        <ul className="attack-defense-measures">
          {d.measures.map((m) => (
            <li key={m.id}>
              <span className="attack-method">{m.name}</span>
              <span className="attack-how">{m.how}</span>
            </li>
          ))}
        </ul>
      )}
      {d.blocked.length > 0 && (
        <div className="attack-blocks">
          <div className="attack-list-head">Blocked</div>
          <ul className="attack-list">
            {d.blocked.slice(0, 50).map((b) => (
              <li key={b.address} className="attack-row">
                <span className="attack-row-who min0">
                  <span className="mono">{b.address}</span>
                  {b.where === 'web' && <span className="attack-network"> in nginx</span>}
                  <span className="attack-row-why">{b.why}</span>
                </span>
                <span className="attack-row-nums">{left(b.until, now)}</span>
                {onUnblock && (
                  <button type="button" className="btn-quiet" disabled={!!busy} onClick={() => act(b.address, () => onUnblock(b.address))}>
                    {busy === b.address ? 'Unblocking…' : 'Unblock'}
                  </button>
                )}
              </li>
            ))}
          </ul>
          {d.blocked.length > 50 && <p className="small ink-3">and {d.blocked.length - 50} more</p>}
        </div>
      )}
      {!!d.cannot?.length && (
        <ul className="attack-cannot">
          {d.cannot.map((c) => (
            <li key={c}>{c}</li>
          ))}
        </ul>
      )}
      {d.error && <p className="form-error">{d.error}</p>}
    </div>
  );
}

// AttackWatch shows the watch for denial-of-service attacks: calm or under attack, which methods and
// how strong, what is running that explains heavy traffic, who is most involved, and past attacks.
export function AttackWatch({
  view,
  now,
  label = 'Attacks',
  onToggle,
  onUnblock,
}: {
  view: AttackView;
  now: number;
  label?: string;
  onToggle?: (on: boolean) => Promise<void>;
  onUnblock?: (address: string) => Promise<void>;
}) {
  const methods = view.methods?.length ? view.methods : ATTACK_METHODS;
  const blocked = new Set(view.defense?.blocked.map((b) => b.address));
  const explained = view.findings.filter((f) => f.expected);
  const status = view.attacking ? 'Under attack' : view.learning ? 'Calm · still learning what is normal here' : 'Calm';
  return (
    <Section label={label} aside={<span className={`attack-status ${view.attacking ? 'on' : ''}`}>{status}</span>}>
      {view.attacking && view.current ? (
        <div className="attack-now" role="alert">
          <strong>{describe(view.current, methods)}</strong>
          <span className="small">
            for {lasted(view.current, now)} · peak {formatRate(view.current.peak_rx_bits / 8)} in
          </span>
          {!!view.current.kinds?.length && (
            <ul className="attack-kinds">
              {view.current.kinds.map((k) => (
                <li key={k.id}>
                  <span className="attack-method">{k.name}</span>
                  {k.detail && <span className="small">{k.detail}</span>}
                  <span className="small ink-3 num">{Math.round(k.share * 100)}% of what was sampled</span>
                  <span className="attack-how">{k.how}</span>
                </li>
              ))}
            </ul>
          )}
          <span className="label attack-measures">{view.current.kinds?.length ? 'As the counters measure it' : 'Methods'}</span>
          <ul className="attack-methods">
            {view.current.methods.map((f) => (
              <li key={f.method}>
                <span className="attack-method">{methodOf(f.method, methods).name}</span>
                <span className="num">{strength(f, methods)}</span>
                <span className="small ink-3">{f.normal > 0 ? `normal here: ${big(f.normal)}` : `counts from ${big(f.threshold)}`}</span>
                {!view.current?.kinds?.length && <span className="attack-how">{methodOf(f.method, methods).how}</span>}
              </li>
            ))}
          </ul>
          <Sources sources={view.current.sources} blocked={blocked} />
        </div>
      ) : (
        <p className="note">Nothing flooding this machine. Watching for SYN, UDP, ping, connection and HTTP floods and traffic enough to fill the line, every 5 seconds.</p>
      )}
      {view.defense && <Defense d={view.defense} attacking={view.attacking} now={now} onToggle={onToggle} onUnblock={onUnblock} />}
      {explained.length > 0 && (
        <ul className="attack-explained">
          {explained.map((f) => (
            <li key={f.method}>
              {methodOf(f.method, methods).name}-sized traffic ({strength(f, methods)}), but {f.expected}: not an attack.
            </li>
          ))}
        </ul>
      )}
      {view.history.length > 0 && (
        <div className="attack-history">
          <span className="label">Past attacks</span>
          <ul>
            {view.history.slice(0, 10).map((a) => (
              <li key={a.id}>
                <span>
                  {describe(a, methods)}
                  {a.kinds?.[0]?.detail && <span className="small ink-3"> · {a.kinds[0].detail}</span>}
                </span>
                <span className="small ink-3 num">
                  {formatAgo(a.start, now)} · {lasted(a, now)} · peak {formatRate(a.peak_rx_bits / 8)}
                  {a.sources[0] && ` · most from ${a.sources[0].address}${a.sources[0].network ? ` (${a.sources[0].network})` : ''}`}
                </span>
              </li>
            ))}
          </ul>
        </div>
      )}
    </Section>
  );
}
