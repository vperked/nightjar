import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { useSearchParams } from 'react-router';
import { api, errorText } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import type { RemoteHost, TailscaleNode, TailscaleSettings, TailscaleState } from '../api/types';
import { useCopy } from '../clipboard';
import { Select } from '../components/Select';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { NetworkTabs, Seg } from '../components/ui';
import { formatAgo, formatBytes } from '../format';
import { useNow } from '../hooks/useNow';

// The Tailscale page manages this machine's Tailscale, or a server's through its agent (?server=<id>):
// connecting and logging in, the settings (exit node, subnet routes, Tailscale SSH, MagicDNS, shields
// up, the machine's name), and the other machines on the tailnet. Using an exit node and shields up
// can cut the dashboard off, so they go back by themselves after two minutes unless kept.
//
// It is quick to use: each machine's last answer is kept for the visit, so coming back shows it at
// once while a fresh one is fetched; switches flip as soon as they are pressed and flip back only if
// the change is refused; and nothing is fetched while the tab is hidden.

type Action = 'up' | 'down' | 'logout';

// TailscaleTarget is a machine's Tailscale; each call answers with it after.
export interface TailscaleTarget {
  key: string;
  name: string;
  load: () => Promise<TailscaleState>;
  login: (authKey?: string) => Promise<TailscaleState>;
  action: (a: Action) => Promise<TailscaleState>;
  set: (s: Partial<TailscaleSettings>) => Promise<TailscaleState>;
  keep: (keep: boolean) => Promise<TailscaleState>;
}

const localTarget: TailscaleTarget = {
  key: 'local',
  name: 'this machine',
  load: api.tailscale,
  login: api.tailscaleLogin,
  action: api.tailscaleAction,
  set: api.setTailscale,
  keep: api.keepTailscale,
};

const hostTarget = (h: { id: string; name: string }): TailscaleTarget => ({
  key: h.id,
  name: h.name,
  load: () => api.hostTailscale(h.id),
  login: (k) => api.hostTailscaleLogin(h.id, k),
  action: (a) => api.hostTailscaleAction(h.id, a),
  set: (s) => api.setHostTailscale(h.id, s),
  keep: (k) => api.keepHostTailscale(h.id, k),
});

// tidy fills in lists an answer may leave out, and notes how far the machine's clock is from ours.
const tidy = (s: TailscaleState): TailscaleState => ({
  ...s,
  peers: (s.peers ?? []).map((p) => ({ ...p, ips: p.ips ?? [] })),
  health: s.health ?? [],
  settings: { ...s.settings, advertise_routes: s.settings?.advertise_routes ?? [] },
  skew: s.now ? s.now - Date.now() / 1000 : 0,
});

// seen is each machine's last answer this visit, shown at once when the page comes back to it.
const seen = new Map<string, TailscaleState>();

// ipv4 is a machine's IPv4 address, else its first.
const ipv4 = (n: TailscaleNode) => n.ips.find((ip) => !ip.includes(':')) ?? n.ips[0] ?? '';

export function TailscalePage() {
  const [params, setParams] = useSearchParams();
  const server = params.get('server') || 'local';
  const [hosts, setHosts] = useState<RemoteHost[]>();
  useEffect(() => {
    api.hosts().then(
      (r) => setHosts(r.hosts.filter((h) => h.kind === 'agent')),
      () => setHosts([]),
    );
  }, []);
  const host = hosts?.find((h) => h.id === server);
  const target = useMemo(() => (server === 'local' ? localTarget : host ? hostTarget(host) : undefined), [server, host]);
  const pick = (v: string) => setParams(v === 'local' ? {} : { server: v }, { replace: true });

  return (
    <main className="ts-page">
      <div className="page-title">
        <div className="min0">
          <h1>Tailscale</h1>
          <div className="page-sub">How {target ? target.name : 'the machine'} joins your tailnet, what it offers there, and the machines it can reach.</div>
          <NetworkTabs current="/network/tailscale" />
        </div>
        {!!hosts?.length && (
          <div className="toolbar">
            <Select label="Machine" value={server} onChange={pick} options={[{ value: 'local', label: 'This machine' }, ...hosts.map((h) => ({ value: h.id, label: h.name }))]} />
          </div>
        )}
      </div>
      {target ? <TailscaleManager key={target.key} target={target} /> : hosts ? <p className="note">No server with an agent by that name. Pick one above.</p> : <Skeleton kind="list" />}
    </main>
  );
}

// ---------- state ----------

type Patch = Partial<TailscaleSettings>;

// useTailscale is a machine's Tailscale as last read, kept fresh, and the ways to change it.
// Settings changes go out one at a time, in order, and show at once: an answer that comes back while
// later ones are still on their way has those laid over it, so a switch never flickers back.
function useTailscale(target: TailscaleTarget) {
  const notify = useToast();
  const [st, setSt] = useState<TailscaleState | undefined>(() => seen.get(target.key));
  const [failed, setFailed] = useState('');
  const [busy, setBusy] = useState(false);
  const [saving, setSaving] = useState<Set<keyof TailscaleSettings>>(new Set());
  // seq moves on with each change, so a read that went out before it cannot bring back the old state.
  const seq = useRef(0);
  const queue = useRef<Promise<unknown>>(Promise.resolve());
  const ahead = useRef<Patch[]>([]);

  const show = useCallback(
    (s: TailscaleState) => {
      const next = tidy(s);
      const laid = ahead.current.reduce((acc, p) => ({ ...acc, settings: { ...acc.settings, ...p } }), next);
      seen.set(target.key, next);
      setSt(laid);
      setFailed('');
    },
    [target.key],
  );

  const reload = useCallback(() => {
    if (ahead.current.length) return; // the change on its way answers with the state after it
    const at = seq.current;
    target.load().then(
      (s) => seq.current === at && show(s),
      (e) => seq.current === at && setFailed(errorText(e)),
    );
  }, [target, show]);

  useEffect(reload, [reload]);
  // Read again now and then, but not while the tab is hidden: machines come and go, and a login
  // finishes in another tab. Often while a login link waits to be opened, so the page moves on as
  // soon as it has been.
  const waiting = !!st?.auth_url && !st.logged_in;
  useEffect(() => {
    const tick = () => document.visibilityState === 'visible' && reload();
    const t = window.setInterval(tick, waiting ? 3000 : 15000);
    const back = () => document.visibilityState === 'visible' && reload();
    document.addEventListener('visibilitychange', back);
    return () => {
      window.clearInterval(t);
      document.removeEventListener('visibilitychange', back);
    };
  }, [reload, waiting]);
  // A change not kept in time goes back on the machine by itself: read it again once it has.
  useEffect(() => {
    if (!st?.pending) return;
    const t = window.setTimeout(reload, Math.max(0, (st.pending.until - (st.skew ?? 0)) * 1000 - Date.now()) + 1500);
    return () => window.clearTimeout(t);
  }, [st?.pending, st?.skew, reload]);

  // act runs a change that is not a setting (logging in, connecting, keeping), shown once it is done.
  const act = async (change: (t: TailscaleTarget) => Promise<TailscaleState>, ok?: { title: string; body?: string }) => {
    setBusy(true);
    seq.current++;
    try {
      show(await change(target));
      if (ok) notify({ tone: 'ok', ...ok });
      return true;
    } catch (e) {
      notify({ tone: 'error', title: `Could not change Tailscale · ${target.name}`, body: errorText(e) });
      return false;
    } finally {
      setBusy(false);
    }
  };

  // set changes settings, shown at once. A risky one brings up the banner asking to keep it.
  const set = (patch: Patch) => {
    seq.current++;
    ahead.current.push(patch);
    const keys = Object.keys(patch) as (keyof TailscaleSettings)[];
    setSaving((s) => new Set([...s, ...keys]));
    setSt((s) => s && { ...s, settings: { ...s.settings, ...patch } });
    const run = async () => {
      try {
        const next = await target.set(patch);
        ahead.current = ahead.current.filter((p) => p !== patch);
        show(next);
      } catch (e) {
        ahead.current = ahead.current.filter((p) => p !== patch);
        const last = seen.get(target.key);
        if (last) show(last);
        notify({ tone: 'error', title: `Could not change Tailscale · ${target.name}`, body: errorText(e) });
      } finally {
        setSaving((s) => {
          const n = new Set(s);
          keys.forEach((k) => n.delete(k));
          return n;
        });
      }
    };
    queue.current = queue.current.then(run);
    return queue.current;
  };

  return { st, failed, busy, saving, act, set };
}

type TS = ReturnType<typeof useTailscale>;

// ---------- the page ----------

export function TailscaleManager({ target }: { target: TailscaleTarget }) {
  const ts = useTailscale(target);
  const canEdit = useCanEdit();
  const { st, failed } = ts;

  if (!st) {
    return failed ? <p className="note">{failed}</p> : <TailscaleSkeleton />;
  }
  if (!st.installed) return <NotInstalled name={target.name} />;
  if (!st.running) return <p className="note ts-problem">{st.problem || 'Tailscale is not running.'}</p>;
  return (
    <div className="ts-body">
      {st.logged_in ? <Overview st={st} name={target.name} ts={ts} canEdit={canEdit} /> : st.problem && <p className="note">{st.problem}</p>}
      {st.pending && <PendingChange st={st} name={target.name} ts={ts} canEdit={canEdit} />}
      {st.health.length > 0 && (
        <ul className="ts-health" aria-label="Tailscale warnings">
          {st.health.map((h) => (
            <li key={h}>{h}</li>
          ))}
        </ul>
      )}
      {!st.logged_in && st.backend !== 'NeedsMachineAuth' && canEdit && <Login st={st} ts={ts} name={target.name} />}
      {st.backend === 'NeedsMachineAuth' && <Approval name={st.self?.name ?? target.name} />}
      {st.logged_in && (
        <div className="ts-grid">
          <Machines st={st} />
          <div className="ts-side">
            <ExitNodes st={st} ts={ts} canEdit={canEdit} />
            <Settings st={st} ts={ts} canEdit={canEdit} />
          </div>
        </div>
      )}
    </div>
  );
}

function TailscaleSkeleton() {
  return (
    <div className="ts-body" aria-busy="true" aria-label="Loading Tailscale">
      <div className="readouts ts-readouts">
        {[0, 1, 2, 3].map((i) => (
          <div key={i} className="readout">
            <span className="ts-shimmer" style={{ width: 70, height: 11 }} />
            <span className="ts-shimmer" style={{ width: '60%', height: 34, marginTop: 12 }} />
            <span className="ts-shimmer" style={{ width: '80%', height: 11, marginTop: 8 }} />
          </div>
        ))}
      </div>
      <Skeleton kind="list" rows={4} />
    </div>
  );
}

const INSTALL = 'curl -fsSL https://tailscale.com/install.sh | sh';

function NotInstalled({ name }: { name: string }) {
  const copy = useCopy(INSTALL);
  return (
    <div className="ts-card ts-hero-card">
      <TailnetMark />
      <h2>Tailscale isn’t installed on {name}</h2>
      <p>Install it with Tailscale’s script, then come back here to log in.</p>
      <div className="ts-code">
        <code className="mono">{INSTALL}</code>
        <button type="button" className="btn-quiet" onClick={copy.copy}>
          {copy.label()}
        </button>
      </div>
    </div>
  );
}

// TailnetMark is a small mesh of machines, for the empty and logged-out states.
function TailnetMark() {
  const dots = [
    [12, 12],
    [36, 6],
    [58, 18],
    [24, 34],
    [50, 40],
  ];
  const links = [
    [0, 1],
    [1, 2],
    [0, 3],
    [3, 4],
    [2, 4],
    [1, 3],
    [1, 4],
  ];
  return (
    <svg className="ts-mark" viewBox="0 0 70 48" aria-hidden="true">
      {links.map(([a, b]) => (
        <line key={`${a}${b}`} x1={dots[a][0]} y1={dots[a][1]} x2={dots[b][0]} y2={dots[b][1]} />
      ))}
      {dots.map(([x, y], i) => (
        <circle key={i} cx={x} cy={y} r={i === 0 ? 4.5 : 3.5} className={i === 0 ? 'self' : ''} />
      ))}
    </svg>
  );
}

const STATE_WORD: Record<string, string> = { Running: 'Connected', Starting: 'Connecting', Stopped: 'Disconnected', NeedsLogin: 'Logged out', NeedsMachineAuth: 'Awaiting approval' };

// Overview is the strip across the top: the connection, the tailnet's machines, this machine's
// address, and the traffic to the others.
function Overview({ st, name, ts, canEdit }: { st: TailscaleState; name: string; ts: TS; canEdit: boolean }) {
  const [confirm, setConfirm] = useState<'' | 'down' | 'logout'>('');
  const tone = st.backend === 'Running' ? 'up' : st.backend === 'Starting' || st.backend === 'NeedsMachineAuth' ? 'wait' : 'down';
  const self = st.self;
  const online = st.peers.filter((p) => p.online).length;
  const rx = st.peers.reduce((n, p) => n + p.rx, 0);
  const tx = st.peers.reduce((n, p) => n + p.tx, 0);
  const direct = st.peers.filter((p) => p.online && p.direct).length;
  const relayed = st.peers.filter((p) => p.online && !p.direct && p.relay).length;
  const run = (a: Action, title: string) => ts.act((t) => t.action(a), { title: `${title} · ${name}` }).then(() => setConfirm(''));

  return (
    <>
      <div className="readouts ts-readouts">
        <div className={`readout ts-conn ${tone}`}>
          <div className="readout-head">
            <span className="label">Connection</span>
            {st.version && <span className="readout-sub">v{st.version}</span>}
          </div>
          <div className="readout-value ts-word">
            <span className="ts-pulse" aria-hidden="true" />
            {STATE_WORD[st.backend] ?? st.backend}
          </div>
          <div className="readout-sub ts-ellipsis" title={st.tailnet}>
            {st.logged_in ? (st.tailnet ?? 'tailnet') : 'not on a tailnet'}
          </div>
          {canEdit && st.logged_in && (
            <div className="ts-conn-acts">
              {st.backend === 'Stopped' ? (
                <button type="button" className="btn small-btn" disabled={ts.busy} onClick={() => run('up', 'Connected')}>
                  Connect
                </button>
              ) : (
                <button type="button" className="btn-quiet" disabled={ts.busy} onClick={() => setConfirm('down')}>
                  Disconnect
                </button>
              )}
              <button type="button" className="btn-quiet danger" disabled={ts.busy} onClick={() => setConfirm('logout')}>
                Log out
              </button>
            </div>
          )}
        </div>
        <div className="readout">
          <span className="label">Machines</span>
          <div className="readout-value">
            {online}
            <span className="unit">/ {st.peers.length} online</span>
          </div>
          <div className="readout-sub">{st.peers.length ? `${direct} direct · ${relayed} relayed` : 'none yet'}</div>
          <div className="ts-dots" aria-hidden="true">
            {st.peers.slice(0, 48).map((p) => (
              <span key={p.id} className={p.online ? (p.active ? 'on active' : 'on') : ''} title={p.name} />
            ))}
          </div>
        </div>
        <div className="readout">
          <span className="label">{self?.name ?? 'This machine'}</span>
          {self && ipv4(self) ? (
            <>
              <CopyValue text={ipv4(self)} />
              <div className="readout-sub ts-ellipsis" title={self.dns_name}>
                {self.dns_name || self.ips.filter((ip) => ip !== ipv4(self)).join(' ')}
              </div>
              {self.key_expiry ? <div className="readout-sub">key expires {new Date(self.key_expiry * 1000).toLocaleDateString()}</div> : null}
            </>
          ) : (
            <>
              <div className="readout-value ts-muted">—</div>
              <div className="readout-sub">no address yet</div>
            </>
          )}
        </div>
        <div className="readout">
          <span className="label">Traffic</span>
          <div className="net-pair">
            <div className="readout-value">
              <span className="unit">in</span>
              {formatBytes(rx)}
            </div>
            <div className="readout-value">
              <span className="unit">out</span>
              {formatBytes(tx)}
            </div>
          </div>
          <div className="readout-sub">with the tailnet, since it connected</div>
        </div>
      </div>
      {confirm && (
        <div className="fw-ask ts-confirm" role="group" aria-label={confirm === 'down' ? 'Disconnect' : 'Log out'}>
          <p>
            {confirm === 'down'
              ? `${name} leaves the tailnet until you connect it again; it stays logged in. Anyone reaching it over Tailscale is cut off.`
              : `${name} leaves the tailnet and its key is expired: logging in again needs a login link or a new auth key. Anyone reaching it over Tailscale is cut off.`}
          </p>
          <div className="fw-ask-acts">
            <button type="button" className="btn-quiet" disabled={ts.busy} onClick={() => setConfirm('')}>
              Cancel
            </button>
            <button type="button" className="btn" disabled={ts.busy} onClick={() => (confirm === 'down' ? run('down', 'Disconnected') : run('logout', 'Logged out'))}>
              {confirm === 'down' ? 'Disconnect' : 'Log out'}
            </button>
          </div>
        </div>
      )}
      {st.problem && <p className="note">{st.problem}</p>}
    </>
  );
}

// CopyValue is a large address that copies itself when clicked.
function CopyValue({ text }: { text: string }) {
  const copy = useCopy(text);
  return (
    <button type="button" className="readout-value ts-copy-value" title="Copy" onClick={copy.copy}>
      {text}
      <span className="ts-copy-hint">{copy.state === 'copied' ? 'copied' : 'copy'}</span>
    </button>
  );
}

function CopyChip({ text }: { text: string }) {
  const copy = useCopy(text);
  return (
    <button type="button" className="ts-chip mono" title="Copy" onClick={copy.copy}>
      {copy.state === 'copied' ? 'Copied' : text}
    </button>
  );
}

function PendingChange({ st, name, ts, canEdit }: { st: TailscaleState; name: string; ts: TS; canEdit: boolean }) {
  const now = useNow(1000);
  const until = (st.pending?.until ?? 0) - (st.skew ?? 0);
  const left = Math.max(0, Math.round(until - now));
  return (
    <div className="fw-pending ts-pending" role="alert">
      <div className="ts-pending-clock num" aria-hidden="true" style={{ ['--left' as string]: `${(left / 120) * 100}%` }}>
        {left}s
      </div>
      <p>
        <strong>Check that you can still reach {name}.</strong> If you can, keep the change. Otherwise {st.pending?.what} goes back by itself in <span className="num">{left}s</span>.
      </p>
      {canEdit && (
        <div className="fw-ask-acts">
          <button type="button" className="btn-quiet" disabled={ts.busy} onClick={() => ts.act((t) => t.keep(false), { title: `Put back · ${name}` })}>
            Put it back
          </button>
          <button type="button" className="btn" disabled={ts.busy} onClick={() => ts.act((t) => t.keep(true), { title: `Kept · ${name}` })}>
            Keep
          </button>
        </div>
      )}
    </div>
  );
}

function Approval({ name }: { name: string }) {
  return (
    <div className="ts-card ts-hero-card">
      <TailnetMark />
      <h2>Waiting for approval</h2>
      <p>
        {name} is logged in, but the tailnet asks an admin to approve each new machine. Approve it in{' '}
        <a href="https://login.tailscale.com/admin/machines" target="_blank" rel="noreferrer">
          Tailscale’s admin console
        </a>
        ; this page moves on by itself.
      </p>
    </div>
  );
}

function Login({ st, ts, name }: { st: TailscaleState; ts: TS; name: string }) {
  const [key, setKey] = useState('');
  return (
    <div className="ts-card ts-login">
      <div className="ts-login-intro">
        <TailnetMark />
        <h2>Put {name} on your tailnet</h2>
        <p>Log in once and it joins your other machines, reachable from anywhere by name.</p>
      </div>
      <div className="ts-login-ways">
        <div className="ts-login-way">
          <h3>With your account</h3>
          {st.auth_url ? (
            <>
              <p>Open the link and log in. This page moves on by itself once you have.</p>
              <a className="btn ts-link" href={st.auth_url} target="_blank" rel="noreferrer">
                Open login link
              </a>
              <span className="small ink-3 mono ts-url">
                <span className="ts-waiting" aria-hidden="true" />
                waiting for you to log in
              </span>
            </>
          ) : (
            <>
              <p>Get a link to log in with your Tailscale account in this browser.</p>
              <button type="button" className="btn" disabled={ts.busy} onClick={() => ts.act((t) => t.login())}>
                {ts.busy ? 'Getting a link…' : 'Get login link'}
              </button>
            </>
          )}
        </div>
        <div className="ts-login-or" aria-hidden="true">
          or
        </div>
        <form
          className="ts-login-way"
          onSubmit={(e) => {
            e.preventDefault();
            ts.act((t) => t.login(key.trim()), { title: 'Logged in to Tailscale' }).then((ok) => ok && setKey(''));
          }}
        >
          <h3>With an auth key</h3>
          <p>
            For a server nobody logs in at. Make a key in the{' '}
            <a href="https://login.tailscale.com/admin/settings/keys" target="_blank" rel="noreferrer">
              admin console
            </a>
            .
          </p>
          <div className="ts-row">
            <input className="field mono" type="password" autoComplete="off" spellCheck={false} placeholder="tskey-auth-…" aria-label="Auth key" value={key} onChange={(e) => setKey(e.target.value)} />
            <button type="submit" className="btn" disabled={ts.busy || !key.trim().startsWith('tskey-')}>
              Log in
            </button>
          </div>
        </form>
      </div>
    </div>
  );
}

// ---------- machines ----------

type Filter = 'all' | 'online' | 'exit';

function connection(p: TailscaleNode): { kind: 'direct' | 'relay' | ''; text: string } {
  if (!p.online) return { kind: '', text: '' };
  if (p.direct) return { kind: 'direct', text: 'direct' };
  if (p.relay) return { kind: 'relay', text: `relay · ${p.relay}` };
  return { kind: '', text: '' };
}

function Machines({ st }: { st: TailscaleState }) {
  const now = useNow(30000);
  const [filter, setFilter] = useState<Filter>('all');
  const [query, setQuery] = useState('');
  const most = Math.max(1, ...st.peers.map((p) => p.rx + p.tx));
  const q = query.trim().toLowerCase();
  const shown = st.peers.filter(
    (p) =>
      (filter === 'all' || (filter === 'online' ? p.online : p.exit_node_option)) && (!q || [p.name, p.dns_name, p.os, p.user, ...(p.tags ?? []), ...p.ips].some((v) => v?.toLowerCase().includes(q))),
  );
  const exits = st.peers.some((p) => p.exit_node_option);
  return (
    <section className="ts-card ts-machines" aria-label="Machines">
      <div className="ts-card-head">
        <span className="label">Machines</span>
        <div className="ts-filters">
          {st.peers.length > 6 && <input className="field ts-search" type="search" placeholder="Find a machine" aria-label="Find a machine" value={query} onChange={(e) => setQuery(e.target.value)} />}
          <Seg
            label="Show"
            value={filter}
            onChange={setFilter}
            options={[{ value: 'all', label: `All ${st.peers.length}` }, { value: 'online', label: 'Online' }, ...(exits ? [{ value: 'exit' as const, label: 'Exit nodes' }] : [])]}
          />
        </div>
      </div>
      {!st.peers.length ? (
        <p className="note">No other machines on the tailnet yet. Install Tailscale on your phone or laptop and log in with the same account.</p>
      ) : !shown.length ? (
        <p className="note">No machine matches.</p>
      ) : (
        <ul className="ts-list">
          {shown.map((p) => {
            const c = connection(p);
            return (
              <li key={p.id} className={`ts-peer ${p.online ? 'online' : 'offline'}`}>
                <span className={`ts-dot ${p.online ? (p.active ? 'active' : 'on') : ''}`} aria-hidden="true" />
                <div className="ts-peer-main">
                  <div className="ts-peer-name">
                    <strong>{p.name}</strong>
                    {p.exit_node && <span className="ts-tag on">exit node in use</span>}
                    {!p.exit_node && p.exit_node_option && <span className="ts-tag">exit node</span>}
                    {p.routes?.length ? <span className="ts-tag">routes {p.routes.join(', ')}</span> : null}
                    {p.expired && <span className="ts-tag warn">key expired</span>}
                  </div>
                  <div className="ts-peer-meta">
                    <span>{p.online ? 'online' : p.last_seen ? `seen ${formatAgo(p.last_seen, now)}` : 'offline'}</span>
                    {p.os && <span>{p.os}</span>}
                    {(p.tags?.length || p.user) && <span>{p.tags?.length ? p.tags.join(' ') : p.user}</span>}
                  </div>
                </div>
                <div className="ts-peer-addr">{ipv4(p) && <CopyChip text={ipv4(p)} />}</div>
                <div className="ts-peer-link">{c.kind && <span className={`ts-link-badge ${c.kind}`}>{c.text}</span>}</div>
                <div className="ts-peer-traffic" title={`${formatBytes(p.rx)} in · ${formatBytes(p.tx)} out`}>
                  <span className="ts-bar" aria-hidden="true">
                    <span style={{ width: `${((p.rx + p.tx) / most) * 100}%` }} />
                  </span>
                  <span className="num">{formatBytes(p.rx + p.tx)}</span>
                </div>
              </li>
            );
          })}
        </ul>
      )}
    </section>
  );
}

// ---------- exit node and settings ----------

function ExitNodes({ st, ts, canEdit }: { st: TailscaleState; ts: TS; canEdit: boolean }) {
  const s = st.settings;
  const exits = st.peers.filter((p) => p.exit_node_option);
  const busy = ts.saving.has('exit_node');
  // A new exit node keeps the local network direct, so the machine stays reachable from it.
  const choose = (id: string) => {
    if (!canEdit || id === s.exit_node) return;
    ts.set({ exit_node: id, ...(id && !s.exit_node ? { exit_node_allow_lan: true } : {}) });
  };
  const options = [
    { id: '', name: 'None', sub: 'straight to the internet', online: true },
    ...exits.map((p) => ({ id: p.id, name: p.name, sub: p.online ? (p.direct ? 'direct' : p.relay ? `relay · ${p.relay}` : 'online') : 'offline', online: p.online })),
  ];
  return (
    <section className="ts-card" aria-label="Exit node">
      <div className="ts-card-head">
        <span className="label">Exit node</span>
        <span className="readout-sub">goes back in 2 min unless kept</span>
      </div>
      {!exits.length && !s.exit_node ? (
        <p className="note">No machine on the tailnet offers to be an exit node. Turn on “Offer to be an exit node” on one, and approve it in the admin console.</p>
      ) : (
        <div className={`ts-exits ${busy ? 'saving' : ''}`} role="radiogroup" aria-label="Exit node">
          {options.map((o) => (
            <button
              key={o.id || 'none'}
              type="button"
              role="radio"
              aria-checked={s.exit_node === o.id}
              disabled={!canEdit}
              className={`ts-exit ${s.exit_node === o.id ? 'on' : ''} ${o.online ? '' : 'offline'}`}
              onClick={() => choose(o.id)}
            >
              <span className="ts-radio" aria-hidden="true" />
              <span className="ts-exit-name">{o.name}</span>
              <span className="ts-exit-sub">{o.sub}</span>
            </button>
          ))}
        </div>
      )}
      {s.exit_node && (
        <Toggle
          label="Reach the local network directly"
          help="Keeps machines on this network able to reach this one."
          on={s.exit_node_allow_lan}
          saving={ts.saving.has('exit_node_allow_lan')}
          disabled={!canEdit}
          onChange={(on) => ts.set({ exit_node_allow_lan: on })}
        />
      )}
    </section>
  );
}

function Toggle({ label, help, on, saving, disabled, onChange }: { label: string; help: ReactNode; on: boolean; saving?: boolean; disabled?: boolean; onChange: (on: boolean) => void }) {
  return (
    <label className={`ts-toggle ${saving ? 'saving' : ''} ${disabled ? 'disabled' : ''}`}>
      <span className="ts-toggle-text">
        <span className="ts-setting-name">{label}</span>
        <span className="ts-setting-help">{help}</span>
      </span>
      <span className="switch">
        <input type="checkbox" role="switch" aria-label={label} checked={on} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
        <span className="switch-track" aria-hidden="true" />
      </span>
    </label>
  );
}

type ToggleKey = 'accept_routes' | 'accept_dns' | 'ssh' | 'advertise_exit_node' | 'shields_up';

// GROUPS are the settings that are on or off, by what they are for.
const GROUPS: { title: string; items: { key: ToggleKey; label: string; help: string }[] }[] = [
  {
    title: 'Use the tailnet',
    items: [
      { key: 'accept_dns', label: 'Use tailnet DNS', help: 'MagicDNS names for the other machines.' },
      { key: 'accept_routes', label: 'Use subnet routes', help: 'Reach networks other machines route.' },
    ],
  },
  { title: 'Share with it', items: [{ key: 'advertise_exit_node', label: 'Offer to be an exit node', help: 'Others may send internet traffic through here, once approved.' }] },
  {
    title: 'Protect',
    items: [
      { key: 'ssh', label: 'Tailscale SSH', help: 'The tailnet’s policy decides who may log in.' },
      { key: 'shields_up', label: 'Shields up', help: 'Turn away every connection from the tailnet.' },
    ],
  },
];

function Settings({ st, ts, canEdit }: { st: TailscaleState; ts: TS; canEdit: boolean }) {
  const s = st.settings;
  return (
    <section className="ts-card" aria-label="Settings">
      <div className="ts-card-head">
        <span className="label">Settings</span>
      </div>
      {GROUPS.map((g) => (
        <div key={g.title} className="ts-group">
          <div className="ts-group-title">{g.title}</div>
          {g.items.map((t) => (
            <Toggle key={t.key} label={t.label} help={t.help} on={s[t.key]} saving={ts.saving.has(t.key)} disabled={!canEdit} onChange={(on) => ts.set({ [t.key]: on })} />
          ))}
          {g.title === 'Share with it' && <RoutesField st={st} ts={ts} canEdit={canEdit} />}
        </div>
      ))}
      <div className="ts-group">
        <div className="ts-group-title">Name</div>
        <NameField st={st} ts={ts} canEdit={canEdit} />
      </div>
    </section>
  );
}

// TextSetting is a text setting saved with Enter or its button, kept in step with the machine.
function TextSetting({
  label,
  help,
  value,
  placeholder,
  canEdit,
  saving,
  onSave,
  clean = (v) => v.trim(),
}: {
  label: string;
  help: string;
  value: string;
  placeholder: string;
  canEdit: boolean;
  saving: boolean;
  onSave: (v: string) => void;
  clean?: (v: string) => string;
}) {
  const [draft, setDraft] = useState(value);
  useEffect(() => setDraft(value), [value]);
  const changed = clean(draft) !== clean(value);
  return (
    <form
      className={`ts-text ${saving ? 'saving' : ''}`}
      onSubmit={(e) => {
        e.preventDefault();
        if (changed) onSave(draft);
      }}
    >
      <span className="ts-setting-name">{label}</span>
      <span className="ts-setting-help">{help}</span>
      <div className="ts-row">
        <input className="field mono" placeholder={placeholder} aria-label={label} disabled={!canEdit} value={draft} onChange={(e) => setDraft(e.target.value)} />
        {canEdit && changed && (
          <button type="submit" className="btn small-btn">
            Save
          </button>
        )}
      </div>
    </form>
  );
}

const splitRoutes = (v: string) =>
  v
    .split(/[\s,]+/)
    .map((r) => r.trim())
    .filter(Boolean);

function RoutesField({ st, ts, canEdit }: { st: TailscaleState; ts: TS; canEdit: boolean }) {
  return (
    <TextSetting
      label="Subnets to route"
      help="Networks the tailnet may reach through here, such as 192.168.1.0/24. Each needs approving."
      value={st.settings.advertise_routes.join(', ')}
      placeholder="none"
      canEdit={canEdit}
      saving={ts.saving.has('advertise_routes')}
      clean={(v) => splitRoutes(v).join(',')}
      onSave={(v) => void ts.set({ advertise_routes: splitRoutes(v) })}
    />
  );
}

function NameField({ st, ts, canEdit }: { st: TailscaleState; ts: TS; canEdit: boolean }) {
  return (
    <TextSetting
      label="Name on the tailnet"
      help="Leave empty to use the machine’s own hostname."
      value={st.settings.hostname}
      placeholder={st.self?.name ?? ''}
      canEdit={canEdit}
      saving={ts.saving.has('hostname')}
      onSave={(v) => void ts.set({ hostname: v.trim() })}
    />
  );
}
