import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ApiError, api, waitForRestart } from '../api/client';
import type { AppJob, AppsResponse, StoreApp } from '../api/types';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { AppsTabs } from '../components/ui';

// The app store: torrent, media and home-server apps installed from their official releases, each
// as its own systemd service. Apps swizzin or a package already installed show as installed and are left alone.

const CATEGORIES = ['All', 'Download clients', 'Indexers', 'Automation', 'Media', 'Files and sync', 'Network', 'Self-hosting', 'Notifications'];

// The usual torrent pipeline, left to right, shown as a strip with each app's state.
const STACK: [string, string][] = [
  ['prowlarr', 'finds releases'],
  ['sonarr', 'shows'],
  ['radarr', 'films'],
  ['qbittorrent', 'downloads'],
  ['unpackerr', 'unpacks'],
];

// address is where an app's web interface answers, from this browser.
function address(a: StoreApp): string | undefined {
  if (!a.installed || !a.port) return undefined;
  const host = window.location.hostname;
  // swizzin puts its apps behind nginx at /<name>.
  if (a.by === 'swizzin') return `${window.location.protocol}//${host}/${a.id}/`;
  return `http://${host}:${a.port}`;
}

// Each app's logo, bundled so the store needs nothing from the internet to look right.
const LOGOS = import.meta.glob<string>('../assets/apps/*.{svg,png}', { eager: true, import: 'default' });
const logoOf = (id: string) => LOGOS[`../assets/apps/${id}.svg`] ?? LOGOS[`../assets/apps/${id}.png`];

// Tile is an app's icon: its logo on a light tile tinted with its colour, or its initial without one.
function Tile({ a, size = 44 }: { a: Pick<StoreApp, 'id' | 'name' | 'color'>; size?: number }) {
  const logo = logoOf(a.id);
  return (
    <span className={`app-tile ${logo ? 'has-logo' : ''}`} style={{ ['--app' as string]: a.color, width: size, height: size, fontSize: size * 0.42 }} aria-hidden="true">
      {logo ? <img src={logo} alt="" draggable={false} /> : a.name.replace(/^qB/, 'q').slice(0, 1).toUpperCase()}
    </span>
  );
}

function StateLine({ a }: { a: StoreApp }) {
  if (!a.available) return <span className="app-state">no build for this machine</span>;
  if (!a.installed) return <span className="app-state">not installed{a.port ? ` · port ${a.port}` : ''}</span>;
  const who = a.by === 'store' ? (a.version ? `v${a.version}` : 'from the store') : a.by === 'swizzin' ? 'installed by swizzin' : 'installed on this machine';
  return (
    <span className={`app-state installed ${a.running ? 'running' : 'stopped'}`}>
      <span className="app-dot" />
      {a.running ? 'running' : 'stopped'} · {who}
    </span>
  );
}

function AppCard({ a, busy, onOpen }: { a: StoreApp; busy: boolean; onOpen: () => void }) {
  const url = address(a);
  return (
    <article className={`app-card ${a.installed ? 'is-installed' : ''}`}>
      <div className="app-card-head">
        <Tile a={a} />
        <div className="min0">
          <h3>{a.name}</h3>
          <span className="app-category">{a.category}</span>
        </div>
      </div>
      <p className="app-summary">{a.summary}</p>
      <div className="app-card-foot">
        <StateLine a={a} />
        <span className="app-actions">
          {url && (
            <a className="btn-quiet" href={url} target="_blank" rel="noreferrer">
              Open ↗
            </a>
          )}
          {!a.installed ? (
            <button className="btn small-btn" onClick={onOpen} disabled={busy || !a.available}>
              Install
            </button>
          ) : (
            a.by === 'store' && (
              <button className="btn-quiet" onClick={onOpen} disabled={busy}>
                Manage
              </button>
            )
          )}
        </span>
      </div>
    </article>
  );
}

// Console shows a job's log, following new lines.
function Console({ job }: { job: AppJob }) {
  const box = useRef<HTMLDivElement>(null);
  useEffect(() => {
    if (box.current) box.current.scrollTop = box.current.scrollHeight;
  }, [job.lines.length]);
  return (
    <div className="install-console" ref={box} role="log" aria-label="Install log">
      {job.lines.map((l, i) =>
        l.startsWith('::step:: ') ? (
          <div key={i} className="install-step">
            {l.slice(9)}
          </div>
        ) : (
          <div key={i}>{l || ' '}</div>
        ),
      )}
    </div>
  );
}

// NtfyHookup points the monitor's alerts at a freshly installed ntfy server.
function NtfyHookup({ a }: { a: StoreApp }) {
  const notify = useToast();
  const [busy, setBusy] = useState(false);
  const topic = useMemo(() => `nightjar-${Math.random().toString(36).slice(2, 10)}`, []);
  // From the box itself (an SSH tunnel), the browser's host is no use to a phone.
  const host = ['localhost', '127.0.0.1', '::1'].includes(window.location.hostname) ? '<box-ip>' : window.location.hostname;
  const use = async () => {
    setBusy(true);
    try {
      const r = await api.saveSettings({ NTFY_URL: `http://127.0.0.1:${a.port}/${topic}` });
      notify({ tone: 'ok', title: 'Alerts now go to your ntfy server', body: r.restarting ? 'The monitor is restarting to connect it.' : undefined });
      if (r.restarting) await waitForRestart();
    } catch (e) {
      notify({ tone: 'error', title: 'Could not set ntfy up', body: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="ntfy-hookup">
      <strong>Send the monitor's alerts here</strong>
      <p>
        In the ntfy app on your phone, add the server{' '}
        <span className="mono">
          http://{host}:{a.port}
        </span>{' '}
        and subscribe to <span className="mono">{topic}</span>. Then:
      </p>
      <button className="btn" onClick={use} disabled={busy}>
        {busy ? 'Connecting…' : 'Use it for alerts'}
      </button>
    </div>
  );
}

function InstallSheet({ a, job, user, onClose, onStarted }: { a: StoreApp; job: AppJob | null; user: string; onClose: () => void; onStarted: (j: AppJob) => void }) {
  const [port, setPort] = useState(String(a.port || ''));
  const [check, setCheck] = useState(true);
  const [purge, setPurge] = useState(false);
  const [confirmRemove, setConfirmRemove] = useState(false);
  const [error, setError] = useState('');
  // The sheet follows a job that is running, or one started from it; an old finished job would
  // otherwise hide the reinstall and remove buttons.
  const [followed, setFollowed] = useState(job && job.app === a.id && !job.finished ? job.id : 0);
  const mine = job && job.app === a.id && (!job.finished || job.id === followed) ? job : null;
  const running = !!mine && !mine.finished;

  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && !running && onClose();
    window.addEventListener('keydown', esc);
    return () => window.removeEventListener('keydown', esc);
  }, [onClose, running]);

  const install = async () => {
    setError('');
    try {
      const { job } = await api.installApp(a.id, Number(port) || 0, check);
      setFollowed(job.id);
      onStarted(job);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  };
  const remove = async () => {
    setError('');
    try {
      const { job } = await api.removeApp(a.id, purge);
      setFollowed(job.id);
      onStarted(job);
    } catch (e) {
      setError(e instanceof ApiError ? e.message : String(e));
    }
  };

  const done = mine?.finished;
  return (
    <div className="sheet-backdrop" onMouseDown={() => !running && onClose()}>
      <div className="sheet" role="dialog" aria-modal="true" aria-label={a.name} onMouseDown={(e) => e.stopPropagation()}>
        <div className="sheet-head">
          <Tile a={a} size={52} />
          <div className="min0">
            <h2>{a.name}</h2>
            <span className="app-category">
              {a.category} ·{' '}
              <a href={a.homepage} target="_blank" rel="noreferrer">
                website ↗
              </a>
            </span>
          </div>
          <button className="icon-btn" aria-label="Close" onClick={onClose} disabled={running}>
            ×
          </button>
        </div>
        <p className="app-summary">{a.summary}</p>

        {mine ? (
          <>
            <div className={`install-status ${running ? 'running' : mine.ok ? 'ok' : 'failed'}`}>
              {running && <span className="spinner" aria-hidden="true" />}
              <strong>
                {running ? mine.step || 'Starting' : mine.ok ? (mine.action === 'install' ? `${a.name} is installed and running` : `${a.name} was removed`) : `The ${mine.action} failed`}
              </strong>
              {!running && !mine.ok && <span>{mine.error}</span>}
            </div>
            <Console job={mine} />
            {done && mine.ok && mine.action === 'install' && (
              <div className="install-next">
                {a.sign_in && <p>{a.sign_in}</p>}
                {a.port > 0 && (
                  <a className="btn" href={`http://${window.location.hostname}:${port}`} target="_blank" rel="noreferrer">
                    Open {a.name} ↗
                  </a>
                )}
                {a.id === 'ntfy' && <NtfyHookup a={{ ...a, port: Number(port) || a.port }} />}
              </div>
            )}
          </>
        ) : !a.installed ? (
          <div className="install-form">
            {a.port > 0 && (
              <label className="install-field">
                <span>Web port</span>
                <input className="field num" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value.replace(/\D/g, ''))} aria-label="Web port" />
              </label>
            )}
            {a.port > 0 && (
              <label className="check">
                <input type="checkbox" checked={check} onChange={(e) => setCheck(e.target.checked)} /> Add an uptime check for it
              </label>
            )}
            <p className="form-help">
              Downloads the latest official release to <span className="mono">/opt/{a.id}</span>, keeps its settings in <span className="mono">/var/lib/{a.id}</span>, and runs it as the{' '}
              <span className="mono">{a.id}.service</span> systemd service under the <span className="mono">{user}</span> account.
            </p>
            {error && <p className="form-error">{error}</p>}
            <button className="btn" onClick={install}>
              Install {a.name}
            </button>
          </div>
        ) : (
          <div className="install-form">
            <p className="form-help">Installed from the store{a.version ? `, version ${a.version}` : ''}. Reinstalling fetches the latest release and keeps its settings.</p>
            {a.sign_in && <p className="form-help">{a.sign_in}</p>}
            {a.id === 'ntfy' && <NtfyHookup a={a} />}
            {error && <p className="form-error">{error}</p>}
            <div className="install-manage">
              <button className="btn-quiet" onClick={install}>
                Reinstall latest
              </button>
              {confirmRemove ? (
                <span className="confirm">
                  <label className="check">
                    <input type="checkbox" checked={purge} onChange={(e) => setPurge(e.target.checked)} /> Also delete its settings and data
                  </label>
                  <button className="btn-quiet danger" onClick={remove}>
                    Remove
                  </button>
                  <button className="btn-quiet" onClick={() => setConfirmRemove(false)}>
                    Keep
                  </button>
                </span>
              ) : (
                <button className="btn-quiet danger" onClick={() => setConfirmRemove(true)}>
                  Remove…
                </button>
              )}
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

export function AppsPage() {
  const [data, setData] = useState<AppsResponse>();
  const [error, setError] = useState('');
  const [category, setCategory] = useState('All');
  const [query, setQuery] = useState('');
  const [open, setOpen] = useState<string>();
  const [job, setJob] = useState<AppJob | null>(null);
  const notify = useToast();

  const load = useCallback(
    () =>
      api.apps().then(
        (r) => {
          setData(r);
          setJob((j) => j ?? r.job);
        },
        (e: Error) => setError(e.message),
      ),
    [],
  );
  useEffect(() => {
    load();
  }, [load]);

  // While a job runs, follow its log every second, one request at a time; when it ends, reload the apps.
  const running = !!job && !job.finished;
  const runningId = running ? job.id : 0;
  const latest = useRef({ job, data, load, notify });
  latest.current = { job, data, load, notify };
  useEffect(() => {
    if (!runningId) return;
    let stopped = false;
    let timer = 0;
    const poll = async () => {
      const have = latest.current.job?.lines.length ?? 0;
      const r = await api.appJob(have).catch(() => undefined);
      if (stopped) return;
      if (!r) {
        timer = window.setTimeout(poll, 1000);
        return;
      }
      const next = r.job;
      const { data, load, notify } = latest.current;
      if (!next || next.id !== runningId) {
        // The monitor restarted mid-job, which ends it.
        setJob(next);
        load();
        return;
      }
      setJob((j) => (j && j.id === next.id ? { ...next, lines: [...j.lines, ...(next.lines ?? []).slice(Math.max(0, j.lines.length - have))] } : next));
      if (!next.finished) {
        timer = window.setTimeout(poll, 1000);
        return;
      }
      load();
      const name = data?.apps.find((a) => a.id === next.app)?.name ?? next.app;
      notify(next.ok ? { tone: 'ok', title: `${name} ${next.action === 'install' ? 'installed' : 'removed'}` } : { tone: 'error', title: `${name}: ${next.action} failed`, body: next.error });
    };
    timer = window.setTimeout(poll, 1000);
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [runningId]);

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (data?.apps ?? []).filter((a) => (category === 'All' || a.category === category) && (!q || `${a.name} ${a.summary} ${a.category}`.toLowerCase().includes(q)));
  }, [data, category, query]);

  if (error) return <main className="alert">{error}</main>;
  if (!data) return <Skeleton kind="dashboard" />;
  const byId = new Map(data.apps.map((a) => [a.id, a]));
  const installed = data.apps.filter((a) => a.installed).length;
  const current = open ? byId.get(open) : undefined;
  const runningApp = job && running ? byId.get(job.app) : undefined;

  return (
    <main>
      <div className="page-title">
        <div>
          <h1>App store</h1>
          <div className="page-sub">
            Torrent, media and home-server apps from their official releases, each running as its own service. {installed} of {data.apps.length} on this machine.
          </div>
          <AppsTabs current="/apps" />
        </div>
      </div>

      {runningApp && job && (
        <button className="install-banner" onClick={() => setOpen(runningApp.id)}>
          <span className="spinner" aria-hidden="true" />
          <span>
            <strong>
              {job.action === 'install' ? 'Installing' : 'Removing'} {runningApp.name}
            </strong>{' '}
            · {job.step || 'starting'}
          </span>
          <span className="install-banner-link">show log</span>
        </button>
      )}

      <section className="stack" aria-label="The torrent stack">
        <div className="stack-head">
          <span className="label">The torrent stack</span>
          <span className="small ink-3">what each piece does, and whether you have it</span>
        </div>
        <ol className="stack-flow">
          {STACK.map(([id, role]) => {
            const a = byId.get(id);
            if (!a) return null;
            return (
              <li key={id} className={a.installed ? (a.running ? 'on' : 'stopped') : 'off'}>
                <button onClick={() => !a.installed && setOpen(id)} disabled={a.installed} title={a.installed ? `${a.name}: ${a.running ? 'running' : 'stopped'}` : `Install ${a.name}`}>
                  <Tile a={a} size={34} />
                  <span className="min0">
                    <strong>{a.name}</strong>
                    <span>{a.installed ? (a.running ? 'running' : 'stopped') : `+ install · ${role}`}</span>
                  </span>
                </button>
              </li>
            );
          })}
        </ol>
      </section>

      <div className="store-filters">
        <div className="chips" role="group" aria-label="Category">
          {CATEGORIES.map((c) => (
            <button key={c} className={`chip ${category === c ? 'active' : ''}`} aria-pressed={category === c} onClick={() => setCategory(c)}>
              {c}
            </button>
          ))}
        </div>
        <input className="search" type="search" placeholder="Search apps…" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Search apps" />
      </div>

      <div className="app-grid">
        {shown.map((a) => (
          <AppCard key={a.id} a={a} busy={running} onOpen={() => setOpen(a.id)} />
        ))}
        {shown.length === 0 && <p className="note">No app matches.</p>}
      </div>

      {current && <InstallSheet a={current} job={job} user={data.user} onClose={() => setOpen(undefined)} onStarted={setJob} />}
    </main>
  );
}
