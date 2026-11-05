import { Fragment, useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { ApiError, api, isDemo } from '../api/client';
import type { AgentBuild, AgentUpdate, AnalyticsPoint, AnalyticsRangeName, HostPoint, RemoteHost, RemoteReadings, Service, SSHAction, SSHKey } from '../api/types';
import { useServices } from '../api/ServicesContext';
import { State } from '../components/ui';
import { Select } from '../components/Select';
import { AttackWatch } from '../components/Attacks';
import { FirewallSummary } from '../components/FirewallSummary';
import { MuteButton } from '../components/MuteButton';
import { HOST_STATUS as STATUS, HostStatus as StatusTag } from '../components/RemoteSummary';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { Legend, TrendChart, withGaps, type Point } from '../components/TrendChart';
import { Bar, Section, Seg, ServersTabs } from '../components/ui';
import { formatAgo, formatBytes, formatRate, formatUptime } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';
import { Readout } from './Overview';
import { DriveRow } from './overview/Drives';
import { BulkBar, BulkResults, useBulk } from './servers/Bulk';

// Other servers, watched through the small agent installed on each, for its readings. Whether
// something merely answers (a router's page, a port, a ping) is an uptime check, under Services; a
// check can say which server it runs on, and that server's page lists it.
// The dashboard does the asking; the servers never need to reach it.

const series = (h: HostPoint[], pick: (p: HostPoint) => number | null): Point[] => h.map((p) => ({ t: p.t, v: pick(p) }));
const deg = (v: number) => `${Math.round(v)}°`;
const pct = (v: number) => `${Math.round(v)}%`;

function osLine(r: RemoteReadings): string {
  return `${r.system.platform_version || r.system.platform} · up ${formatUptime(r.system.uptime_seconds)}`;
}

// hottestDrive is the warmest drive that reported a temperature.
function hottestDrive(r: RemoteReadings): number | undefined {
  const temps = r.drives.map((d) => d.temperature).filter((t): t is number => t !== undefined);
  return temps.length ? Math.max(...temps) : undefined;
}

// Metric is one of a server card's three headline numbers, with its last ten minutes underneath.
function Metric({
  label,
  value,
  unit,
  points,
  warn,
  format,
  show = (v) => String(Math.round(v)),
}: {
  label: string;
  value: number | null;
  unit: string;
  points: Point[];
  warn: boolean;
  format: (v: number) => string;
  show?: (v: number) => string;
}) {
  return (
    <div className={`host-metric ${warn ? 'warn' : ''}`}>
      <span className="label">{label}</span>
      <span className="host-metric-value num">
        {value === null ? '—' : show(value)}
        {value !== null && <span className="unit">{unit}</span>}
      </span>
      <TrendChart series={[{ label, color: warn ? 'var(--warn)' : 'var(--series-1)', points }]} height={28} compact format={format} emptyText="" />
    </div>
  );
}

// ---------- history ranges ----------

const CHART_RANGES: { value: AnalyticsRangeName; label: string }[] = [
  { value: '1h', label: '1 hour' },
  { value: '24h', label: '24 hours' },
  { value: '7d', label: '7 days' },
  { value: '30d', label: '30 days' },
];

interface Kept {
  points: AnalyticsPoint[];
  step: number;
}

// useStored reads a server's stored minutes for ranges past the live hour; undefined while loading.
function useStored(id: string, range: AnalyticsRangeName): Kept | undefined {
  const fetchRange = useCallback(() => (range === '1h' ? Promise.resolve(null) : api.hostAnalytics(id, range)), [id, range]);
  const { data } = usePoll(fetchRange, 60000);
  return range === '1h' || !data ? undefined : { points: data.host.points ?? [], step: data.step };
}

// stored is one metric of the kept points, with a gap wherever buckets are missing.
const stored = (kept: Kept | undefined, pick: (p: AnalyticsPoint) => number | null): Point[] =>
  kept
    ? withGaps(
        kept.points.map((p) => ({ t: p.t, v: pick(p) })),
        kept.step,
      )
    : [];

function RangeBar({ range, setRange }: { range: AnalyticsRangeName; setRange: (r: AnalyticsRangeName) => void }) {
  return (
    <div className="range-bar">
      <span className="small ink-3">{range === '1h' ? 'Live, every 10 seconds' : 'From the minutes kept for analytics'}</span>
      <Seg label="Chart range" value={range} onChange={setRange} options={CHART_RANGES} />
    </div>
  );
}

// shownProblems leaves out "offline" when the page already says so with the error.
const shownProblems = (h: RemoteHost) => (h.status === 'offline' ? h.problems.filter((p) => !p.startsWith('Offline')) : h.problems);

// HostCard is one server in the list: a link to its page, or while choosing servers, a box to tick.
function HostCard({ h, now, select }: { h: RemoteHost; now: number; select?: { on: boolean; toggle: () => void } }) {
  const r = h.readings;
  const problems = shownProblems(h);
  const disks = r ? [...r.disks].sort((a, b) => b.percent - a.percent).slice(0, 3) : [];
  const drive = r ? hottestDrive(r) : undefined;
  const body = (
    <>
      <div className="host-head">
        {select && <span className={`select-box host-select ${select.on ? 'on' : ''}`} aria-hidden="true" />}
        <span className="min0">
          <strong className="truncate">{h.name}</strong>
          <span className="host-sub truncate">{h.kind === 'ssh' && h.ssh ? `SSH · ${h.ssh.user}@${h.address}${h.ssh.port !== 22 ? `:${h.ssh.port}` : ''}` : r ? osLine(r) : h.address}</span>
        </span>
        <StatusTag h={h} />
      </div>
      {h.status === 'offline' && (
        <p className="host-error">
          {h.error}
          {h.last_seen ? ` · last seen ${formatAgo(h.last_seen, now)}` : ''}
        </p>
      )}
      {problems.length > 0 && (
        <ul className="host-problems">
          {problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}
      {h.kind === 'ssh' ? (
        <div className="host-foot">
          <span>{h.status === 'online' ? 'SSH answering' : h.status === 'offline' ? 'SSH not answering' : 'Checking SSH…'}</span>
          <span>no agent</span>
        </div>
      ) : r ? (
        <>
          <div className="host-metrics">
            <Metric label="CPU" value={r.cpu.percent} unit="%" points={series(h.history, (p) => p.cpu)} warn={r.cpu.percent >= 90} format={pct} />
            <Metric label="Temp" value={r.temperature} unit="°C" points={series(h.history, (p) => p.temp)} warn={h.problems.some((p) => p.startsWith('CPU at'))} format={deg} />
            <Metric label="Memory" value={r.memory.percent} unit="%" points={series(h.history, (p) => p.mem)} warn={h.problems.some((p) => p.startsWith('Memory'))} format={pct} />
          </div>
          <div className="host-disks">
            {disks.map((d) => (
              <div key={d.mountpoint} className="host-disk">
                <span className="mono truncate">{d.mountpoint}</span>
                <Bar percent={d.percent} label={`${d.mountpoint} usage`} />
                <span className="num small">{Math.round(d.percent)}%</span>
              </div>
            ))}
          </div>
          <div className="host-foot num">
            <span title="Network in / out">
              ↓ {formatRate(r.network.bytes_recv_per_sec)} · ↑ {formatRate(r.network.bytes_sent_per_sec)}
            </span>
            <span>{[`${r.cpu.count} cores`, drive !== undefined ? `drives up to ${drive}°C` : '', r.gpu ? `GPU ${Math.round(r.gpu.temperature)}°C` : ''].filter(Boolean).join(' · ')}</span>
          </div>
        </>
      ) : (
        h.status === 'waiting' && <p className="host-sub">Waiting for the first answer…</p>
      )}
    </>
  );
  if (select)
    return (
      <div
        className={`host-card selectable ${h.status} ${select.on ? 'picked' : ''}`}
        role="checkbox"
        aria-checked={select.on}
        aria-label={`${h.name}: ${STATUS[h.status][1]}`}
        tabIndex={0}
        onClick={select.toggle}
        onKeyDown={(e) => {
          if (e.key === ' ' || e.key === 'Enter') {
            e.preventDefault();
            select.toggle();
          }
        }}
      >
        {body}
      </div>
    );
  return (
    <Link to={`/servers/${h.id}`} className={`host-card ${h.status}`} aria-label={`${h.name}: ${STATUS[h.status][1]}`}>
      {body}
    </Link>
  );
}

export function ServersPage() {
  const { data, error, refresh } = usePoll(api.hosts, 10000);
  const [adding, setAdding] = useState(false);
  // selected is the servers ticked while choosing; null when not choosing.
  const [selected, setSelected] = useState<Set<string> | null>(null);
  const bulk = useBulk(refresh);
  const now = useNow(10000);
  useEffect(() => {
    if (!selected) return;
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && !bulk.busy && setSelected(null);
    window.addEventListener('keydown', esc);
    return () => window.removeEventListener('keydown', esc);
  }, [selected, bulk.busy]);
  if (!data) return error ? <main className="alert">{error.message}</main> : <Skeleton kind="dashboard" />;
  const down = data.hosts.filter((h) => h.status === 'offline').length;
  // Selections only hold servers that are still watched.
  const picked = selected ? data.hosts.filter((h) => selected.has(h.id)) : [];
  const allPicked = picked.length === data.hosts.length;
  const toggle = (id: string) =>
    setSelected((s) => {
      const next = new Set(s);
      if (!next.delete(id)) next.add(id);
      return next;
    });
  return (
    <main className={selected ? 'choosing-servers' : undefined}>
      <div className="page-title">
        <div>
          <h1>Servers</h1>
          <div className="page-sub">
            {data.hosts.length === 0
              ? 'Watch other machines from here: their temperatures, CPU, memory, disks and drives, with alerts.'
              : `${data.hosts.length} watched${down ? ` · ${down} offline` : ''}. Checked every 10 seconds.`}
          </div>
          <ServersTabs current="/servers" />
        </div>
        {data.hosts.length > 0 && (
          <div className="page-actions">
            {selected ? (
              <>
                <button className="btn-quiet" onClick={() => setSelected(allPicked ? new Set() : new Set(data.hosts.map((h) => h.id)))}>
                  {allPicked ? 'Select none' : 'Select all'}
                </button>
                <button className="btn" onClick={() => setSelected(null)} disabled={bulk.busy}>
                  Done
                </button>
              </>
            ) : (
              <>
                {data.hosts.length > 1 && (
                  <button className="btn-quiet" onClick={() => setSelected(new Set())}>
                    Select
                  </button>
                )}
                <button className="btn" onClick={() => setAdding(true)}>
                  Add a server
                </button>
              </>
            )}
          </div>
        )}
      </div>
      {bulk.run && <BulkResults run={bulk.run} onDismiss={bulk.dismiss} />}
      {data.hosts.length === 0 ? (
        <section className="section servers-empty">
          <strong>No other servers yet</strong>
          <p>
            Install the small agent on a server for its temperatures, CPU, memory, disks and drives. This dashboard does the asking; the server never needs to reach it. Something you only want to know
            is answering, like a router or a website, is an <Link to="/services">uptime check</Link> instead.
          </p>
          <button className="btn" onClick={() => setAdding(true)}>
            Add a server
          </button>
        </section>
      ) : (
        <div className="host-grid">
          {data.hosts.map((h) => (
            <HostCard key={h.id} h={h} now={now} select={selected ? { on: selected.has(h.id), toggle: () => toggle(h.id) } : undefined} />
          ))}
        </div>
      )}
      {selected && picked.length === 0 && <p className="bulk-hint note">Tick the servers to act on, then choose what to do.</p>}
      {picked.length > 0 && (
        <BulkBar
          picked={picked}
          busy={bulk.busy}
          onRun={(action, minutes) =>
            bulk.start(action, picked, minutes).then(() => {
              if (action === 'remove') setSelected(new Set());
            })
          }
          onClear={() => setSelected(new Set())}
        />
      )}
      {adding && (
        <AddServerSheet
          port={data.port}
          builds={data.agents ?? []}
          onClose={() => setAdding(false)}
          onAdded={() => {
            setAdding(false);
            refresh();
          }}
        />
      )}
    </main>
  );
}

// ---------- adding ----------

// Command is a shell command with a copy button.
function Command({ text }: { text: string }) {
  const [copied, setCopied] = useState(false);
  const code = useRef<HTMLElement>(null);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      // Plain http has no clipboard access: select the text so Ctrl+C copies it.
      const range = document.createRange();
      if (code.current) range.selectNodeContents(code.current);
      window.getSelection()?.removeAllRanges();
      window.getSelection()?.addRange(range);
    }
  };
  return (
    <div className="cmd">
      <code ref={code}>{text}</code>
      <button type="button" className="btn-quiet" onClick={copy}>
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  );
}

// hostPart is the host name and port in what was typed as the address.
function hostPart(address: string): { host: string; port: string } {
  const bare = address
    .trim()
    .replace(/^https?:\/\//, '')
    .replace(/\/.*$/, '');
  const m = bare.match(/^(\[[^\]]+\]|[^:]+)(?::(\d+))?$/);
  return { host: m?.[1].replace(/^\[|\]$/g, '') ?? '', port: m?.[2] ?? '' };
}

function AddServerSheet({ port, builds, onClose, onAdded }: { port: number; builds: AgentBuild[]; onClose: () => void; onAdded: (h: RemoteHost) => void }) {
  const notify = useToast();
  const [how, setHow] = useState<'agent' | 'ssh'>('agent');
  const [login, setLogin] = useState<Login>(loginOf());
  const [keyReady, setKeyReady] = useState(false);
  const [arch, setArch] = useState<'amd64' | 'arm64'>('amd64');
  const [https, setHttps] = useState(true);
  const [name, setName] = useState('');
  const [address, setAddress] = useState('');
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  // hostKey is the server's host key, waiting to be checked before the password goes to it.
  const [hostKey, setHostKey] = useState('');
  // A key shown for one login is not the one to confirm for another.
  useEffect(() => setHostKey(''), [login, address]);

  useEffect(() => {
    api.hostToken().then(
      (r) => setToken(r.token),
      (e: Error) => setError(e.message),
    );
  }, []);
  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && !busy && onClose();
    window.addEventListener('keydown', esc);
    return () => window.removeEventListener('keydown', esc);
  }, [onClose, busy]);

  const build = builds.find((b) => b.arch === arch);
  const where = hostPart(address);
  const file = build ? build.file : `nightjar-agent-linux-${arch}`;
  const flags = `${where.port && Number(where.port) !== port ? ` -listen :${where.port}` : ''}${https ? '' : ' -tls=false'}`;
  // An address typed without a scheme gets the one the agent will serve.
  const agentAddress = /^https?:\/\//.test(address.trim()) ? address.trim() : `${https ? 'https' : 'http'}://${address.trim()}`;

  const connect = async (trusted = '') => {
    setBusy(true);
    setError('');
    try {
      if (how === 'ssh') await placeKey(login, { target: address }, trusted);
      setHostKey('');
      const { host, test } = await api.addHost(
        how === 'agent' ? { kind: 'agent', name, address: agentAddress, token } : { kind: 'ssh', name, ssh: { ...sshBody(login), host: login.host.trim() || address.trim() } },
      );
      if (how === 'agent') notify({ tone: 'ok', title: `${host.name} added`, body: 'Its readings refresh every 10 seconds.' });
      else if (test?.ok) notify({ tone: 'ok', title: `${host.name} added`, body: `Logged in: ${test.output?.split('\n')[0] ?? ''}` });
      else notify({ tone: 'error', title: `${host.name} added, but the login did not work`, body: test?.error });
      onAdded(host);
    } catch (err) {
      if (err instanceof ApiError && err.hostKey) setHostKey(err.hostKey);
      else setError(err instanceof ApiError || err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="sheet-backdrop" onMouseDown={() => !busy && onClose()}>
      <form
        className="sheet add-server"
        role="dialog"
        aria-modal="true"
        aria-label="Add a server"
        onMouseDown={(e) => e.stopPropagation()}
        onSubmit={(e) => {
          e.preventDefault();
          connect();
        }}
      >
        <div className="sheet-head">
          <div className="min0">
            <h2>Add a server</h2>
            <span className="page-sub">{how === 'agent' ? 'Download, drop it on the server, install, then connect.' : 'Nothing to install: Nightjar logs in over SSH when you ask it to.'}</span>
          </div>
          <button type="button" className="icon-btn" aria-label="Close" onClick={onClose} disabled={busy}>
            ×
          </button>
        </div>

        <div className="add-how" role="radiogroup" aria-label="How to watch it">
          {(
            [
              ['agent', 'With the agent', 'Temperatures, CPU, memory, disks, drives and network. A small program to install.'],
              ['ssh', 'SSH only', 'Test the login and reboot from here, and see its checks. Nothing to install.'],
            ] as const
          ).map(([value, title, sub]) => (
            <label key={value} className={`add-how-option ${how === value ? 'on' : ''}`}>
              <input type="radio" name="how" value={value} checked={how === value} onChange={() => setHow(value)} />
              <strong>{title}</strong>
              <span>{sub}</span>
            </label>
          ))}
        </div>

        <p className="add-note">
          Only want to know it answers, with nothing installed? That is an <Link to="/services">uptime check</Link>: HTTPS, a port or ping, with retries and alerts.
        </p>

        <div>
          <label className="install-field">
            <span>Address</span>
            <input className="field" value={address} onChange={(e) => setAddress(e.target.value)} placeholder="192.168.0.20" autoFocus required aria-label="Address" />
          </label>
        </div>

        {how === 'ssh' ? (
          <div className="add-ssh">
            <SSHLoginFields login={login} onChange={setLogin} onReady={setKeyReady} serverName="the server" showHost={false} />
            {hostKey && <HostKeyCheck hostKey={hostKey} serverName="the server" busy={busy} onConfirm={() => connect(hostKey)} onCancel={() => setHostKey('')} />}
          </div>
        ) : (
          <ol className="add-steps">
            <li>
              <div className="add-step-head">
                <strong>Get the agent</strong>
                <Seg
                  label="Server's processor"
                  value={arch}
                  onChange={setArch}
                  options={[
                    { value: 'amd64', label: 'x86-64' },
                    { value: 'arm64', label: 'ARM64' },
                  ]}
                />
              </div>
              {build ? (
                <>
                  <p>Built into this dashboard, so it is the same version. ARM64 is for a Raspberry Pi 4 or 5 and other ARM boards.</p>
                  <a
                    className="btn agent-download"
                    href={`/api/agent/download?arch=${arch}`}
                    download={build.file}
                    onClick={(e) => {
                      if (!isDemo()) return;
                      e.preventDefault();
                      notify({ tone: 'error', title: 'The demo has no agent to download', body: 'On a real dashboard this saves the agent built into it.' });
                    }}
                  >
                    Download {build.file} <span className="num">({formatBytes(build.size)})</span>
                  </a>
                </>
              ) : (
                <>
                  <p>This dashboard was built without the agent inside. Build it on this machine, in the Nightjar folder, then restart the monitor to download it from here next time.</p>
                  <Command text="make agent" />
                </>
              )}
            </li>
            <li>
              <strong>Put it on the server</strong>
              <p>
                Drag <code>{file}</code> {build ? 'from your downloads' : 'from bin/ in the Nightjar folder'} onto the server, into any folder: with a file app such as WinSCP, FileZilla or Cyberduck,
                into a folder the server shares, or on a USB stick.
              </p>
            </li>
            <li>
              <strong>Install it</strong>
              <p>
                In a terminal on the server, in the folder you dropped it in. It runs as a service that starts on boot and listens on port {where.port || port}, opens that port in ufw, firewalld or
                iptables if one is on, and adds smartmontools for drive readings. Only this machine needs to reach the port; on a cloud server, also allow it in the provider’s firewall. Over HTTPS it
                keeps itself on this dashboard’s version.
              </p>
              <label className="check">
                <input type="checkbox" checked={https} onChange={(e) => setHttps(e.target.checked)} /> Encrypt with HTTPS
              </label>
              <p>
                {https
                  ? 'Its key comes from the token, so this dashboard only talks to the real agent; there is no certificate to copy.'
                  : 'Plain HTTP: readings and the token cross the network unencrypted. Only on a network you trust.'}
              </p>
              <Command text={`chmod +x ${file} && sudo ./${file} install -token ${token || '…'}${flags}`} />
            </li>
          </ol>
        )}

        <div className="install-form">
          <label className="install-field">
            <span>Name</span>
            <input className="field" value={name} onChange={(e) => setName(e.target.value)} placeholder="its host name, if left empty" aria-label="Name" />
          </label>
          {how === 'agent' && (
            <details className="add-token">
              <summary>Token</summary>
              <input className="field mono" value={token} onChange={(e) => setToken(e.target.value)} aria-label="Token" spellCheck={false} autoComplete="off" />
              <p className="form-help">Made for this server and shown only here; the dashboard keeps it to itself. An agent you installed before keeps the token it was given.</p>
            </details>
          )}
          {error && <p className="form-error">{error}</p>}
          <button className="btn" type="submit" disabled={busy || !address.trim() || (how === 'agent' ? !token : !loginReady(login, keyReady))}>
            {busy
              ? how === 'agent'
                ? 'Connecting…'
                : login.place === 'password'
                  ? 'Adding the key…'
                  : 'Logging in…'
              : how === 'agent'
                ? 'Connect'
                : login.place === 'password'
                  ? 'Add key and server'
                  : 'Add server'}
          </button>
        </div>
      </form>
    </div>
  );
}

// ---------- one server ----------

function EditServer({ h, onSaved, onRemoved }: { h: RemoteHost; onSaved: () => void; onRemoved: () => void }) {
  const notify = useToast();
  const [name, setName] = useState(h.name);
  const [address, setAddress] = useState(h.address);
  const [token, setToken] = useState('');
  const [busy, setBusy] = useState(false);
  const [confirm, setConfirm] = useState(false);
  const [error, setError] = useState('');
  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.updateHost(h.id, h.kind === 'ssh' ? { kind: 'ssh', name } : { kind: 'agent', name, address, token });
      notify({ tone: 'ok', title: 'Saved' });
      setToken('');
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };
  const remove = async () => {
    setBusy(true);
    try {
      await api.deleteHost(h.id);
      notify({ tone: 'ok', title: `${h.name} removed`, body: `${h.kind === 'agent' ? 'Its agent keeps running until you uninstall it. ' : ''}Checks that belonged to it stay, belonging to none.` });
      onRemoved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
      setBusy(false);
    }
  };
  return (
    <Section label="Connection">
      <form className="host-edit" onSubmit={save}>
        <label className="install-field">
          <span>Name</span>
          <input className="field" value={name} onChange={(e) => setName(e.target.value)} aria-label="Name" required />
        </label>
        {h.kind === 'agent' && (
          <>
            <label className="install-field">
              <span>Address</span>
              <input className="field mono" value={address} onChange={(e) => setAddress(e.target.value)} aria-label="Address" required />
            </label>
            <label className="install-field">
              <span>New token</span>
              <input
                className="field mono"
                type="password"
                autoComplete="new-password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                placeholder="keep the saved one"
                aria-label="New token"
                spellCheck={false}
              />
            </label>
          </>
        )}
        {error && <p className="form-error">{error}</p>}
        <div className="host-edit-actions">
          <button className="btn" type="submit" disabled={busy}>
            Save
          </button>
          {confirm ? (
            <span className="confirm">
              <span className="small ink-3">Stop watching it?</span>
              <button type="button" className="btn-quiet danger" onClick={remove} disabled={busy}>
                Remove
              </button>
              <button type="button" className="btn-quiet" onClick={() => setConfirm(false)}>
                Keep
              </button>
            </span>
          ) : (
            <button type="button" className="btn-quiet danger" onClick={() => setConfirm(true)}>
              Remove…
            </button>
          )}
        </div>
        {h.kind === 'ssh' ? (
          <p className="form-help">Its address and login are under SSH. Install the agent and add it again for its readings.</p>
        ) : (
          <p className="form-help">
            {h.address.startsWith('http://') ? 'Plain HTTP: reinstall the agent without -tls=false and use https:// to encrypt it. ' : 'Encrypted with HTTPS, pinned to the key its token gives. '}
            To uninstall the agent itself, run <span className="mono">sudo nightjar-agent uninstall</span> on the server.
          </p>
        )}
      </form>
    </Section>
  );
}

// ---------- SSH ----------

// sshTarget is where an SSH login connects when no host of its own is given: the server's address.
function sshTarget(h: RemoteHost): string {
  try {
    return new URL(h.address).hostname;
  } catch {
    return h.address;
  }
}

// ReinstallNow puts this dashboard's build on a server whose agent cannot update itself, over its
// SSH login; without one, it says where to set that up.
function ReinstallNow({ h, refresh }: { h: RemoteHost; refresh: () => void }) {
  const notify = useToast();
  const [busy, setBusy] = useState(false);
  if (!h.ssh) return <span className="agent-reinstall-hint">set up SSH below to reinstall it from here</span>;
  const reinstall = async () => {
    setBusy(true);
    try {
      await api.runHostSSH(h.id, 'reinstall-agent');
      notify({ tone: 'ok', title: `Agent reinstalled · ${h.name}`, body: 'It updates itself from now on.' });
      refresh();
    } catch (err) {
      notify({ tone: 'error', title: 'Could not reinstall the agent', body: err instanceof ApiError ? err.message : String(err) });
    } finally {
      setBusy(false);
    }
  };
  return (
    <button type="button" className="agent-reinstall" onClick={reinstall} disabled={busy}>
      {busy ? 'reinstalling…' : 'reinstall now'}
    </button>
  );
}

// AGENT_UPDATE says how keeping an agent on this dashboard's build is going.
const AGENT_UPDATE: Record<AgentUpdate['state'], string> = {
  current: 'up to date',
  updating: 'updating…',
  updated: 'updated',
  failed: 'update failed',
  manual: 'reinstall to auto-update',
};

const NEW_KEY = '\u0000new';
const OWN_KEY = '\u0000own';

const SSH_DONE: Record<SSHAction, string> = { test: 'Logged in', 'restart-agent': 'Agent restarted', 'reinstall-agent': 'Agent reinstalled', reboot: 'Rebooting' };

// KeyPlace is how a login's key gets onto the server: it is there already, Nightjar adds it by logging
// in once with the password, or you add its public line yourself.
type KeyPlace = 'there' | 'password' | 'self';

// Login is an SSH login as typed: which key (by path), user, port, and the host when it differs from
// the server's own address; and how its key gets onto the server.
interface Login {
  key: string;
  user: string;
  port: string;
  host: string;
  place: KeyPlace;
  password: string;
}

const loginOf = (h?: RemoteHost): Login => ({ key: h?.ssh?.key ?? '', user: h?.ssh?.user ?? 'root', port: String(h?.ssh?.port ?? 22), host: h?.ssh?.host ?? '', place: 'there', password: '' });

// loginReady says whether a login can be saved: a key chosen, and the password when Nightjar is to add it.
const loginReady = (l: Login, keyReady: boolean) => keyReady && (l.place !== 'password' || !!l.password);

// placeKey adds the login's key on the server first, when the password was given for that; the
// password is used for that one login and kept nowhere. It throws when the server turns it down, and
// throws the ApiError itself, with its hostKey, while nobody has confirmed the server's host key.
// trusted is the host key someone just checked on the server: it is kept before the password goes.
async function placeKey(l: Login, where: { id?: string; target?: string }, trusted = '') {
  if (l.place !== 'password') return;
  const login = { ...sshBody(l), host: l.host.trim() || where.target?.trim() || undefined };
  try {
    if (trusted) await api.trustSSHHostKey({ id: where.id, login, host_key: trusted });
    await api.authorizeSSHKey({ id: where.id, login, password: l.password });
  } catch (err) {
    if (err instanceof ApiError && err.hostKey) throw err;
    throw new Error(`Could not add the key: ${err instanceof ApiError ? err.message : String(err)}`);
  }
}

// HostKeyCheck shows the host key a server presents before the password goes to it: only once it
// matches the key on the server itself is it kept, and the key added.
function HostKeyCheck({ hostKey, serverName, busy, onConfirm, onCancel }: { hostKey: string; serverName: string; busy: boolean; onConfirm: () => void; onCancel: () => void }) {
  const kind = hostKey.split(' ')[0].toLowerCase();
  return (
    <div className="ssh-place ssh-host-key" role="alert">
      <span className="ssh-place-head">Is this {serverName}’s host key?</span>
      <p className="mono">{hostKey}</p>
      <p className="form-help">Nightjar has not seen this server before. Before the password goes, compare the key with what this shows on the server:</p>
      <Command text={`ssh-keygen -lf /etc/ssh/ssh_host_${kind}_key.pub`} />
      <span className="confirm">
        <button type="button" className="btn" onClick={onConfirm} disabled={busy}>
          {busy ? 'Adding the key…' : 'It matches, add the key'}
        </button>
        <button type="button" className="btn-quiet" onClick={onCancel} disabled={busy}>
          Cancel
        </button>
      </span>
    </div>
  );
}

// saveLabel names what saving a login does.
const saveLabel = (l: Login, busy: boolean) => (busy ? (l.place === 'password' ? 'Adding the key…' : 'Logging in…') : l.place === 'password' ? 'Add key, save and test' : 'Save and test');

// SSHLoginFields picks a key (one on this machine, your own pasted in, or a new one), the user, port
// and host to log in with, and how the key gets onto the server.
function SSHLoginFields({
  login,
  onChange,
  serverName,
  hostLabel = 'Host',
  hostPlaceholder,
  showHost = true,
  onReady,
}: {
  login: Login;
  // onChange takes an update to the latest login, as a state setter does, so one made after a
  // request returns keeps what was typed meanwhile.
  onChange: (update: (l: Login) => Login) => void;
  serverName: string;
  hostLabel?: string;
  hostPlaceholder?: string;
  // showHost is false where the host is asked for elsewhere.
  showHost?: boolean;
  // onReady says whether a usable key is chosen.
  onReady: (ready: boolean) => void;
}) {
  const notify = useToast();
  const [keys, setKeys] = useState<SSHKey[] | null>(null);
  const [newName, setNewName] = useState('nightjar');
  const [ownName, setOwnName] = useState('my-key');
  const [ownKey, setOwnKey] = useState('');
  const [busy, setBusy] = useState('');
  const [error, setError] = useState('');
  const set = (patch: Partial<Login>) => onChange((l) => ({ ...l, ...patch }));

  useEffect(() => {
    api.sshKeys().then(
      ({ keys }) => {
        setKeys(keys);
        onChange((l) => (l.key ? l : { ...l, key: keys[0]?.path || OWN_KEY }));
      },
      (err) => setError(err instanceof ApiError ? err.message : String(err)),
    );
    // Only once: the keys on this machine do not change while the form is open.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);
  const chosen = keys?.find((k) => k.path === login.key);
  useEffect(() => onReady(!!chosen), [chosen, onReady]);

  // A key just made or added is on no server yet, so it is put there with the password.
  const added = (k: SSHKey, title: string, body: string) => {
    setKeys((ks) => [k, ...(ks ?? [])]);
    set({ key: k.path, place: 'password' });
    notify({ tone: 'ok', title, body });
  };
  const makeKey = async () => {
    setBusy('key');
    setError('');
    try {
      const { key: k } = await api.newSSHKey(newName.trim());
      added(k, `Key ${k.name} made`, `Now put it on ${serverName}.`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy('');
    }
  };
  const addOwn = async () => {
    setBusy('own');
    setError('');
    try {
      const { key: k } = await api.importSSHKey(ownName.trim(), ownKey);
      setOwnKey('');
      added(k, `Key ${k.name} added`, 'Kept on this machine, readable only by Nightjar.');
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy('');
    }
  };

  const keyOptions = [
    ...(keys ?? []).map((k) => ({ value: k.path, label: k.name, text: k.name, hint: `${k.type} · ${k.own ? 'kept by Nightjar' : k.path.replace(/\/[^/]+$/, '')} · ${k.fingerprint}` })),
    { value: OWN_KEY, label: 'Add a key you have…', text: 'add', hint: 'Paste a private key the server already accepts' },
    { value: NEW_KEY, label: 'Make a new key…', text: 'new', hint: 'An ed25519 key with no passphrase, kept in Nightjar’s SSH folder' },
  ];
  const user = login.user.trim() || 'root';

  return (
    <>
      {keys === null ? (
        <p className="note">Looking for keys…</p>
      ) : (
        <div className="install-field">
          <span>Key</span>
          <Select value={login.key} options={keyOptions} onChange={(key) => set({ key })} label="Key" />
        </div>
      )}
      {login.key === NEW_KEY && keys !== null && (
        <div className="ssh-new-key">
          <label className="install-field">
            <span>Name for the new key</span>
            <input className="field mono" value={newName} onChange={(e) => setNewName(e.target.value)} aria-label="Name for the new key" spellCheck={false} />
          </label>
          <button type="button" className="btn" onClick={makeKey} disabled={!!busy || !newName.trim()}>
            {busy === 'key' ? 'Making…' : 'Make key'}
          </button>
        </div>
      )}
      {login.key === OWN_KEY && keys !== null && (
        <div className="ssh-own-key">
          <label className="install-field">
            <span>Name for it here</span>
            <input className="field mono" value={ownName} onChange={(e) => setOwnName(e.target.value)} aria-label="Name for the key" spellCheck={false} />
          </label>
          <label className="install-field">
            <span>Private key</span>
            <textarea
              className="field mono ssh-private"
              value={ownKey}
              onChange={(e) => setOwnKey(e.target.value)}
              placeholder={'-----BEGIN OPENSSH PRIVATE KEY-----\n…\n-----END OPENSSH PRIVATE KEY-----'}
              aria-label="Private key"
              spellCheck={false}
              autoComplete="off"
              rows={6}
            />
          </label>
          <p className="form-help">
            The file without <span className="mono">.pub</span>, such as <span className="mono">~/.ssh/id_ed25519</span>, with no passphrase. It is kept on this machine, readable only by Nightjar, and
            never shown again.
          </p>
          <div>
            <button type="button" className="btn" onClick={addOwn} disabled={!!busy || !ownName.trim() || !ownKey.trim()}>
              {busy === 'own' ? 'Adding…' : 'Add key'}
            </button>
          </div>
        </div>
      )}
      {error && <p className="form-error">{error}</p>}
      <div className="ssh-login-fields">
        <label className="install-field">
          <span>User</span>
          <input className="field mono" value={login.user} onChange={(e) => set({ user: e.target.value })} aria-label="User" spellCheck={false} autoComplete="off" />
        </label>
        <label className="install-field">
          <span>Port</span>
          <input className="field mono" value={login.port} onChange={(e) => set({ port: e.target.value.replace(/\D/g, '') })} aria-label="Port" inputMode="numeric" />
        </label>
      </div>
      {showHost && (
        <label className="install-field">
          <span>{hostLabel}</span>
          <input className="field mono" value={login.host} onChange={(e) => set({ host: e.target.value })} placeholder={hostPlaceholder} aria-label={hostLabel} spellCheck={false} />
        </label>
      )}
      {chosen && (
        <div className="ssh-place">
          <span className="ssh-place-head">
            Is {chosen.name} on {serverName} already?
          </span>
          <Seg
            label="How the key gets onto the server"
            value={login.place}
            onChange={(place) => set({ place })}
            options={[
              { value: 'there', label: 'Yes' },
              { value: 'password', label: 'Add it with a password' },
              { value: 'self', label: 'I’ll add it' },
            ]}
          />
          {login.place === 'there' && <p className="form-help">Nightjar logs in with it as {user} when you save, to check it works.</p>}
          {login.place === 'password' && (
            <>
              <input
                type="password"
                className="field"
                value={login.password}
                onChange={(e) => set({ password: e.target.value })}
                placeholder={`password for ${user}`}
                aria-label="Password on the server"
                autoComplete="new-password"
              />
              <p className="form-help">Nightjar logs in once with the password to add the key, then uses the key. The password is not kept.</p>
            </>
          )}
          {login.place === 'self' && (
            <>
              <p className="form-help">
                Add this line to <span className="mono">~/.ssh/authorized_keys</span> for {user} on {serverName}, then save.
              </p>
              <Command text={chosen.public} />
            </>
          )}
        </div>
      )}
    </>
  );
}

const sshBody = (l: Login) => ({ key: l.key, user: l.user.trim(), port: Number(l.port) || 22, host: l.host.trim() || undefined });

// LoginStatus says whether a server's saved login works, from the last time Nightjar used it, and
// which host key it trusts.
function LoginStatus({ h, busy }: { h: RemoteHost; busy: boolean }) {
  const now = useNow(30000);
  const c = h.ssh_check;
  const tone = busy ? 'busy' : !c ? 'none' : c.ok ? 'ok' : 'bad';
  return (
    <div className={`ssh-status ${tone}`} role="status">
      <div className="ssh-status-line">
        <span className="ssh-status-mark" aria-hidden="true" />
        <span className="ssh-status-word">
          {busy ? (
            'Logging in…'
          ) : !c ? (
            'Not tried yet'
          ) : c.ok ? (
            c.as ? (
              <>
                Logs in as <span className="mono">{c.as}</span>
              </>
            ) : (
              'Logs in'
            )
          ) : (
            'Can’t log in'
          )}
        </span>
        {c && !busy && (
          <span className="ssh-status-when">
            {c.ok ? 'worked' : 'tried'} {formatAgo(c.at, now)}
          </span>
        )}
      </div>
      {c && !c.ok && !busy && c.error && <p className="ssh-status-why">{c.error}</p>}
      {c?.host_key && (
        <p className="ssh-status-key" title="The server’s host key Nightjar trusts. If it changes, Nightjar refuses to log in.">
          Host key <span className="mono">{c.host_key}</span>
        </p>
      )}
    </div>
  );
}

// SSHAccess is the way in when the agent cannot help: a login Nightjar keeps for the server (a key on
// this machine, a user and a port), with which it can test the login, restart the agent or reboot.
// A server watched through its SSH login alone always has one.
function SSHAccess({ h, refresh }: { h: RemoteHost; refresh: () => void }) {
  const notify = useToast();
  const sshOnly = h.kind === 'ssh';
  const [editing, setEditing] = useState(false);
  const [login, setLogin] = useState<Login>(loginOf(h));
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState<string>('');
  const [error, setError] = useState('');
  const [result, setResult] = useState<{ action: SSHAction; ok: boolean; text: string } | null>(null);
  const [confirmReboot, setConfirmReboot] = useState(false);
  const [confirmForget, setConfirmForget] = useState(false);
  // hostKey is the server's host key, waiting to be checked before the password goes to it.
  const [hostKey, setHostKey] = useState('');
  // A key shown for one login is not the one to confirm for another.
  useEffect(() => setHostKey(''), [login]);

  const open = () => {
    setLogin(loginOf(h));
    setError('');
    setEditing(true);
  };

  const save = async (trusted = '') => {
    setBusy('save');
    setError('');
    try {
      await placeKey(login, { id: h.id }, trusted);
      setHostKey('');
      const { test } = await api.setHostSSH(h.id, sshBody(login));
      if (test?.ok) notify({ tone: 'ok', title: 'SSH login saved', body: `Logged in as ${test.output?.split('\n')[0] ?? login.user}.` });
      else notify({ tone: 'error', title: 'Saved, but the login did not work', body: test?.error });
      setEditing(false);
      setResult(null);
      refresh();
    } catch (err) {
      if (err instanceof ApiError && err.hostKey) setHostKey(err.hostKey);
      else setError(err instanceof ApiError || err instanceof Error ? err.message : String(err));
    } finally {
      setBusy('');
    }
  };

  const forget = async () => {
    setBusy('forget');
    try {
      await api.setHostSSH(h.id, null);
      notify({ tone: 'ok', title: 'SSH login removed', body: 'The key stays on this machine.' });
      setConfirmForget(false);
      setEditing(false);
      setResult(null);
      refresh();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy('');
    }
  };

  const run = async (action: SSHAction) => {
    setBusy(action);
    setConfirmReboot(false);
    setResult(null);
    try {
      const { output } = await api.runHostSSH(h.id, action);
      // A test speaks through the status above; the others show what the server said.
      setResult(action === 'test' ? null : { action, ok: true, text: output });
      if (action !== 'test') notify({ tone: 'ok', title: `${SSH_DONE[action]} · ${h.name}` });
    } catch (err) {
      if (action !== 'test') setResult({ action, ok: false, text: err instanceof ApiError ? err.message : String(err) });
    } finally {
      setBusy('');
      refresh();
    }
  };

  const aside = sshOnly ? 'how Nightjar reaches it' : 'for when the agent can’t help';
  if (editing)
    return (
      <Section label="SSH" aside={<span className="small ink-3">{aside}</span>}>
        <form
          className="host-edit"
          onSubmit={(e) => {
            e.preventDefault();
            save();
          }}
        >
          <SSHLoginFields
            login={login}
            onChange={setLogin}
            onReady={setReady}
            serverName={h.name}
            hostLabel={sshOnly ? 'Address' : 'Host'}
            hostPlaceholder={sshOnly ? 'vps.example.net' : sshTarget(h)}
          />
          {hostKey && <HostKeyCheck hostKey={hostKey} serverName={h.name} busy={busy === 'save'} onConfirm={() => save(hostKey)} onCancel={() => setHostKey('')} />}
          {error && <p className="form-error">{error}</p>}
          <div className="host-edit-actions">
            <span className="confirm">
              <button className="btn" type="submit" disabled={!!busy || !loginReady(login, ready) || (sshOnly && !login.host.trim())}>
                {saveLabel(login, busy === 'save')}
              </button>
              <button type="button" className="btn-quiet" onClick={() => setEditing(false)} disabled={!!busy}>
                Cancel
              </button>
            </span>
          </div>
          <p className="form-help">
            {sshOnly ? 'Rebooting' : 'Restarting the agent or rebooting'} needs root, or a user that can run <span className="mono">systemctl</span> with sudo and no password. The first login trusts
            the server’s host key and remembers it.
          </p>
        </form>
      </Section>
    );

  if (!h.ssh)
    return (
      <Section label="SSH" aside={<span className="small ink-3">not set up</span>}>
        <p className="note">Give Nightjar an SSH login for {h.name} to restart its agent or reboot it from here when the agent stops answering.</p>
        <div className="host-edit-actions ssh-setup">
          <button type="button" className="btn" onClick={open}>
            Set up SSH…
          </button>
        </div>
      </Section>
    );

  const keyName = h.ssh.key.replace(/^.*\//, '');
  return (
    <Section label="SSH" aside={<span className="small ink-3 mono">{keyName}</span>}>
      <div className="host-edit">
        <LoginStatus h={h} busy={busy === 'test'} />
        <div className="ssh-actions">
          <button type="button" className={h.ssh_check?.ok ? 'btn-quiet' : 'btn'} onClick={() => run('test')} disabled={!!busy}>
            {busy === 'test' ? 'Logging in…' : h.ssh_check ? 'Test again' : 'Test login'}
          </button>
          {h.kind === 'agent' && (
            <>
              <button type="button" className="btn-quiet" onClick={() => run('restart-agent')} disabled={!!busy}>
                {busy === 'restart-agent' ? 'Restarting…' : 'Restart agent'}
              </button>
              <button
                type="button"
                className="btn-quiet"
                onClick={() => run('reinstall-agent')}
                disabled={!!busy}
                title="Install this dashboard's build of the agent, with the token and settings already on the server"
              >
                {busy === 'reinstall-agent' ? 'Reinstalling…' : 'Reinstall agent'}
              </button>
            </>
          )}
          {confirmReboot ? (
            <span className="confirm">
              <span className="small ink-3">Reboot {h.name}?</span>
              <button type="button" className="btn-quiet danger" onClick={() => run('reboot')} disabled={!!busy}>
                Reboot
              </button>
              <button type="button" className="btn-quiet" onClick={() => setConfirmReboot(false)}>
                Keep running
              </button>
            </span>
          ) : (
            <button type="button" className="btn-quiet danger" onClick={() => setConfirmReboot(true)} disabled={!!busy}>
              {busy === 'reboot' ? 'Rebooting…' : 'Reboot…'}
            </button>
          )}
        </div>
        {result && (
          <div className={`ssh-result ${result.ok ? 'ok' : 'bad'}`} role="status">
            <span className="ssh-result-head">{result.ok ? SSH_DONE[result.action] : 'Did not work'}</span>
            {result.text && <pre>{result.text}</pre>}
          </div>
        )}
        {h.ssh_command && (
          <details className="ssh-by-hand">
            <summary>Log in yourself</summary>
            <Command text={h.ssh_command} />
          </details>
        )}
        <div className="host-edit-actions">
          <button type="button" className="btn-quiet" onClick={open} disabled={!!busy}>
            Change login…
          </button>
          {!sshOnly &&
            (confirmForget ? (
              <span className="confirm">
                <span className="small ink-3">Forget this login?</span>
                <button type="button" className="btn-quiet danger" onClick={forget} disabled={!!busy}>
                  Forget
                </button>
                <button type="button" className="btn-quiet" onClick={() => setConfirmForget(false)}>
                  Keep
                </button>
              </span>
            ) : (
              <button type="button" className="btn-quiet danger" onClick={() => setConfirmForget(true)} disabled={!!busy}>
                Forget…
              </button>
            ))}
        </div>
      </div>
    </Section>
  );
}

// ServicesOn are the uptime checks of things that run on this server.
function ServicesOn({ h }: { h: RemoteHost }) {
  const { data } = useServices();
  const now = useNow(10000);
  const mine = (data?.services ?? []).filter((s: Service) => s.server === h.id);
  return (
    <Section label="Services on it" aside={<Link to={`/services?server=${encodeURIComponent(h.id)}#add`}>add a check →</Link>}>
      {mine.length === 0 ? (
        <p className="note">
          No checks belong to {h.name} yet. Add an uptime check for something it runs, like Plex, a share or SSH, and pick {h.name} as its server.
        </p>
      ) : (
        <table className="table">
          <tbody>
            {mine.map((s) => (
              <tr key={s.name}>
                <td className="shrink">
                  <State status={s.status} />
                </td>
                <td className="min0">
                  <Link to={`/services/${encodeURIComponent(s.name)}`} className="truncate">
                    {s.name}
                  </Link>
                  <div className="sub truncate mono">{s.target}</div>
                </td>
                <td className="r num small shrink">{s.status === 'up' && s.latency_ms != null ? `${Math.round(s.latency_ms)} ms` : s.last_change ? formatAgo(s.last_change, now) : '—'}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </Section>
  );
}

// SSHServerDetail is the page of a server watched through its SSH login alone: whether SSH answers,
// the checks that belong to it, and the login itself.
function SSHServerDetail({ h, refresh }: { h: RemoteHost; refresh: () => void }) {
  const navigate = useNavigate();
  const now = useNow(10000);
  return (
    <main>
      <div className="page-title">
        <div className="min0">
          <div className="crumb">
            <Link to="/servers">Servers</Link> /
          </div>
          <h1 className="truncate">{h.name}</h1>
          <div className="page-sub">
            <StatusTag h={h} /> SSH only · <span className="mono">{h.address}</span>
          </div>
        </div>
      </div>
      {h.status === 'offline' && (
        <div className="alert host-alert">
          {h.error}
          {h.last_seen ? ` Last answered ${formatAgo(h.last_seen, now)}.` : ''}
        </div>
      )}
      <div className="columns">
        <div className="col">
          <ServicesOn h={h} />
          <p className="note ssh-only-note">
            Nightjar shows whether its SSH port answers and raises no alerts of its own; its checks do. Install the <Link to="/docs/servers">agent</Link> for temperatures, disks and drives.
          </p>
        </div>
        <div className="col">
          <SSHAccess h={h} refresh={refresh} />
          <EditServer h={h} onSaved={refresh} onRemoved={() => navigate('/servers')} />
        </div>
      </div>
    </main>
  );
}

function Detail({ h, refresh }: { h: RemoteHost; refresh: () => void }) {
  const navigate = useNavigate();
  const notify = useToast();
  const now = useNow(10000);
  // Its defense is managed through its agent; its firewall on the Firewall page.
  const toggleDefense = async (on: boolean) => {
    try {
      await api.setHostDefense(h.id, on);
      notify({
        tone: 'ok',
        title: on ? `Defending ${h.name}` : `Defense off · ${h.name}`,
        body: on ? 'Its agent answers the next attack in the firewall.' : 'Attacks are still watched and announced.',
      });
      refresh();
    } catch (e) {
      notify({ tone: 'error', title: 'Could not change the defense', body: e instanceof ApiError ? e.message : String(e) });
    }
  };
  const unblock = async (address: string) => {
    try {
      await api.unblockHost(h.id, address);
      notify({ tone: 'ok', title: `${address} unblocked · ${h.name}`, body: 'Left alone for an hour.' });
      refresh();
    } catch (e) {
      notify({ tone: 'error', title: `Could not unblock ${address}`, body: e instanceof ApiError ? e.message : String(e) });
    }
  };
  const r = h.readings;
  const cores = r?.cpu.cores ?? [];
  const [range, setRange] = useState<AnalyticsRangeName>('1h');
  const kept = useStored(h.id, range);
  const live = useMemo(() => ({ cpu: series(h.history, (p) => p.cpu), mem: series(h.history, (p) => p.mem), temp: series(h.history, (p) => p.temp) }), [h.history]);
  const usage = useMemo(
    () => [
      { label: 'CPU', color: 'var(--series-1)', points: range === '1h' ? live.cpu : stored(kept, (p) => p.cpu) },
      { label: 'Memory', color: 'var(--series-2)', points: range === '1h' ? live.mem : stored(kept, (p) => p.mem) },
    ],
    [range, live, kept],
  );
  const temps = useMemo(() => [{ label: 'CPU temperature', color: 'var(--series-2)', points: range === '1h' ? live.temp : stored(kept, (p) => p.temp) }], [range, live, kept]);
  const rangeLabel = CHART_RANGES.find((x) => x.value === range)!.label;
  return (
    <main>
      <div className="page-title">
        <div className="min0">
          <div className="crumb">
            <Link to="/servers">Servers</Link> /
          </div>
          <h1 className="truncate">{h.name}</h1>
          <div className="page-sub">
            <StatusTag h={h} /> <span className="mono">{h.address}</span>
            {r ? ` · ${osLine(r)}` : ''}
          </div>
        </div>
        <MuteButton subject={`host:${h.id}`} label={h.name} />
      </div>

      {h.status === 'offline' && (
        <div className="alert host-alert">
          {h.error}
          {h.last_seen ? ` Last seen ${formatAgo(h.last_seen, now)}; the readings below are from then.` : ''}
        </div>
      )}
      {shownProblems(h).length > 0 && (
        <ul className="host-problems wide">
          {shownProblems(h).map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}

      {r && (
        <div className={`readouts ${h.status === 'offline' ? 'stale' : ''}`}>
          <Readout label="CPU" value={r.cpu.percent} unit="%" sub={`${r.cpu.count} cores${r.cpu.load ? ` · load ${r.cpu.load[0].toFixed(2)}` : ''}`} points={live.cpu} />
          {r.temperature !== null ? (
            <Readout
              label="CPU temperature"
              value={r.temperature}
              unit="°C"
              sub={r.fans[0] ? `${r.fans[0].label} ${r.fans[0].speed_rpm.toLocaleString()} rpm` : 'from the CPU sensor'}
              points={live.temp}
            />
          ) : (
            <div className="readout">
              <span className="label">CPU temperature</span>
              <div className="readout-value">—</div>
              <div className="readout-sub">no sensor on this machine</div>
            </div>
          )}
          <Readout label="Memory" value={r.memory.percent} unit="%" sub={`${formatBytes(r.memory.used)} / ${formatBytes(r.memory.total)}`} points={live.mem} />
          <div className="readout">
            <span className="label">Network</span>
            <div className="readout-value host-net num">
              <span>↓ {formatRate(r.network.bytes_recv_per_sec)}</span>
              <span>↑ {formatRate(r.network.bytes_sent_per_sec)}</span>
            </div>
            <div className="readout-sub">
              disks read {formatRate(r.disk_io.read_per_sec)} · write {formatRate(r.disk_io.write_per_sec)}
            </div>
          </div>
        </div>
      )}

      <div className="columns">
        <div className="col">
          <RangeBar range={range} setRange={setRange} />
          <Section label="CPU and memory" aside={<Legend series={usage} />}>
            <TrendChart series={usage} max={100} format={pct} height={190} emptyText={range === '1h' ? 'Collecting readings…' : kept ? 'Nothing kept for this range yet.' : 'Loading…'} />
          </Section>
          <Section label="Temperature" aside={<span className="small ink-3">CPU, the last {rangeLabel}</span>}>
            <TrendChart
              series={temps}
              format={deg}
              tip={(v) => `${v.toFixed(1)} °C`}
              height={160}
              emptyText={r && r.temperature === null ? 'This machine has no CPU temperature sensor.' : 'Collecting readings…'}
            />
          </Section>
          {r && (
            <Section label="Drives" aside={r.drives_error ? undefined : <span className="small ink-3">SMART, every 5 minutes</span>}>
              {r.drives.length > 0 ? (
                <div className="drives">
                  {r.drives.map((d) => (
                    <DriveRow key={d.device} d={d} now={now} mutable={false} />
                  ))}
                </div>
              ) : (
                <p className="note">{r.drives_error || 'No drives reported yet.'}</p>
              )}
            </Section>
          )}
        </div>
        <div className="col">
          {r && (
            <Section label="Host">
              <dl className="kv">
                <dt>Hostname</dt>
                <dd>{r.system.hostname}</dd>
                <dt>OS</dt>
                <dd title={r.system.platform_version}>{r.system.platform_version || r.system.platform}</dd>
                <dt>Kernel</dt>
                <dd>{r.system.platform_release}</dd>
                <dt>Uptime</dt>
                <dd>{formatUptime(r.system.uptime_seconds)}</dd>
                <dt>Load 1/5/15</dt>
                <dd>{r.cpu.load ? r.cpu.load.map((l) => l.toFixed(2)).join(' ') : '—'}</dd>
                {r.swap.total > 0 && (
                  <>
                    <dt>Swap</dt>
                    <dd>
                      {Math.round(r.swap.percent)}% of {formatBytes(r.swap.total)}
                    </dd>
                  </>
                )}
                {r.fans.map((f) => (
                  <Fragment key={f.name}>
                    <dt>{f.label}</dt>
                    <dd>{f.speed_rpm.toLocaleString()} rpm</dd>
                  </Fragment>
                ))}
                {r.gpu && (
                  <>
                    <dt>GPU</dt>
                    <dd title={r.gpu.name}>
                      {r.gpu.utilization.toFixed(0)}% · {r.gpu.temperature.toFixed(0)} °C
                    </dd>
                  </>
                )}
                <dt>Agent</dt>
                <dd>
                  {r.agent}
                  {h.update && h.update.state !== 'current' && (
                    <span className={`agent-update ${h.update.state}`} title={h.update.detail}>
                      {AGENT_UPDATE[h.update.state]}
                    </span>
                  )}
                  {(h.update?.state === 'manual' || h.update?.state === 'failed') && <ReinstallNow h={h} refresh={refresh} />}
                </dd>
              </dl>
            </Section>
          )}
          {cores.length > 1 && (
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
          {r && (
            <Section label="Storage">
              <div className="storage">
                {r.disks.map((p) => (
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
          <ServicesOn h={h} />
          {r?.attacks && <AttackWatch view={r.attacks} now={now} onToggle={toggleDefense} onUnblock={unblock} />}
          {h.kind === 'agent' && <FirewallSummary h={h} />}
          <EditServer h={h} onSaved={refresh} onRemoved={() => navigate('/servers')} />
          <SSHAccess h={h} refresh={refresh} />
        </div>
      </div>
    </main>
  );
}

export function ServerDetail() {
  const { id = '' } = useParams();
  const fetchHost = useCallback(() => api.host(id), [id]);
  const { data, error, refresh } = usePoll(fetchHost, 10000);
  if (!data) {
    if (error)
      return (
        <main className="alert">
          {error.message} <Link to="/servers">Back to servers</Link>
        </main>
      );
    return <Skeleton kind="dashboard" />;
  }
  return data.host.kind === 'ssh' ? <SSHServerDetail h={data.host} refresh={refresh} /> : <Detail h={data.host} refresh={refresh} />;
}
