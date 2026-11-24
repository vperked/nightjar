import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import { ApiError, api, waitForRestart, errorText } from '../api/client';
import type { CheckSuggestion, SetupStatus, TestTarget } from '../api/types';
import { Working } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { plural } from '../format';

// The setup guide: one card per thing worth setting up, each prefilled with what the monitor found
// on this machine, testable before saving, and saved on its own.

type Tone = 'good' | 'warn' | 'idle';

function Step({ n, title, tone, status, children }: { n: number; title: string; tone: Tone; status: string; children: ReactNode }) {
  return (
    <section className={`panel setup-step ${tone === 'good' ? 'done' : ''}`}>
      <div className="setup-head">
        <span className="setup-n" aria-hidden="true">
          {tone === 'good' ? '✓' : n}
        </span>
        <h2>{title}</h2>
        <span className={`state ${tone}`}>{status}</span>
      </div>
      <div className="setup-body">{children}</div>
    </section>
  );
}

function Row({ label, hint, children }: { label: string; hint?: ReactNode; children: ReactNode }) {
  return (
    <label className="setup-row">
      <span className="setup-label">
        {label}
        {hint && <span className="form-help">{hint}</span>}
      </span>
      {children}
    </label>
  );
}

// useSaver saves one step's settings and tests connections; a save that needs a restart waits for it.
function useSaver(refresh: () => void) {
  const notify = useToast();
  const [busy, setBusy] = useState<string>();
  const [restarting, setRestarting] = useState(false);
  const save = async (what: string, values: Record<string, string>) => {
    setBusy('save:' + what);
    try {
      const r = await api.saveSettings(values);
      if (r.restarting) {
        setRestarting(true);
        await waitForRestart();
        return;
      }
      notify({ tone: 'ok', title: `${what} saved` });
      refresh();
    } catch (err) {
      notify({ tone: 'error', title: `Could not save ${what}`, body: err instanceof ApiError ? (Object.values(err.fields)[0] ?? err.message) : errorText(err) });
    } finally {
      setBusy(undefined);
    }
  };
  const test = async (target: TestTarget, values: Record<string, string>, name: string) => {
    setBusy('test:' + target);
    try {
      const r = await api.testSettings(target, values);
      if (target === 'alerts') {
        if (!r.results?.length) notify({ tone: 'warning', title: 'Nothing to test yet', body: r.detail });
        for (const c of r.results ?? [])
          notify(c.ok ? { tone: 'ok', title: `Test alert sent to ${c.channel}`, body: 'Check that it arrived.' } : { tone: 'error', title: `${c.channel} did not take it`, body: c.error });
      } else {
        notify(r.ok ? { tone: 'ok', title: `${name} connection works`, body: r.detail } : { tone: 'error', title: `Could not connect to ${name}`, body: r.detail });
      }
      return r.ok;
    } catch (err) {
      notify({ tone: 'error', title: 'Could not run the test', body: errorText(err) });
      return false;
    } finally {
      setBusy(undefined);
    }
  };
  return { save, test, busy, restarting };
}

type Saver = ReturnType<typeof useSaver>;

function Buttons({ s, testLabel, onTest, saveLabel, onSave, disabled }: { s: Saver; testLabel?: string; onTest?: () => void; saveLabel: string; onSave: () => void; disabled?: boolean }) {
  return (
    <div className="setup-actions">
      {onTest && (
        <button type="button" className="btn-quiet" disabled={!!s.busy || disabled} onClick={onTest}>
          {s.busy?.startsWith('test:') ? 'Testing…' : testLabel}
        </button>
      )}
      <button type="submit" className="btn" disabled={!!s.busy || disabled} onClick={onSave}>
        {s.busy?.startsWith('save:') ? 'saving…' : saveLabel}
      </button>
    </div>
  );
}

const stop = (e: FormEvent) => e.preventDefault();

function AlertsStep({ st, s }: { st: SetupStatus; s: Saver }) {
  const [webhook, setWebhook] = useState('');
  const a = st.alerts;
  const channels = a.channels ?? [];
  return (
    <Step n={1} title="Alerts" tone={a.configured ? 'good' : 'warn'} status={a.configured ? `to ${channels.join(', ')}` : 'not set up'}>
      <p className="setup-why">Where the monitor tells you about outages, full disks, crashes and updates.</p>
      <form onSubmit={stop}>
        <Row label="Discord webhook" hint="In Discord: Server Settings → Integrations → Webhooks → New Webhook → Copy Webhook URL.">
          <input
            className="field"
            type="password"
            autoComplete="off"
            placeholder={a.webhook_set ? '•••••• (set — paste to replace)' : 'https://discord.com/api/webhooks/…'}
            value={webhook}
            onChange={(e) => setWebhook(e.target.value)}
          />
        </Row>
        <p className="form-help">
          Prefer ntfy, Telegram, Slack or email? They are under <Link to="/settings#alert-channels">Settings → Alert channels</Link>.
        </p>
        <Buttons
          s={s}
          testLabel="send a test alert"
          onTest={() => s.test('alerts', webhook ? { WEBHOOK: webhook } : {}, 'alerts')}
          saveLabel="Save webhook"
          onSave={() => s.save('Discord webhook', { WEBHOOK: webhook })}
          disabled={!webhook}
        />
      </form>
    </Step>
  );
}

function PlexStep({ st, s }: { st: SetupStatus; s: Saver }) {
  const p = st.plex;
  const d = p.detected;
  const [url, setUrl] = useState(p.enabled ? p.url : d.running ? d.url : p.url);
  const [useFound, setUseFound] = useState(d.token_found);
  const [token, setToken] = useState('');
  // An empty token is left out rather than sent: the server deletes a setting
  // saved as '', which would drop the token stored from the dashboard.
  const values = () => ({ PLEX_URL: url, ...(useFound ? { PLEX_TOKEN: '@detected' } : token ? { PLEX_TOKEN: token } : {}) });
  const tone: Tone = p.enabled ? 'good' : d.running ? 'warn' : 'idle';
  return (
    <Step n={2} title="Plex" tone={tone} status={p.enabled ? 'watching' : d.running ? 'found on this machine' : 'not found'}>
      <p className="setup-why">
        {d.running ? `Plex ${d.version ?? ''} is running here.` : 'No Plex server answers on this machine; point it at one elsewhere, or skip this step.'} Watching it adds the Plex page: who is
        streaming, transcodes, libraries, and an alert when it goes down.
      </p>
      <form onSubmit={stop}>
        <Row label="Server address">
          <input className="field" value={url} onChange={(e) => setUrl(e.target.value)} />
        </Row>
        {d.token_found && (
          <label className="check setup-check">
            <input type="checkbox" checked={useFound} onChange={(e) => setUseFound(e.target.checked)} /> Use the token this Plex server already has (read here, never shown)
          </label>
        )}
        {!useFound && (
          <Row label="Token" hint={p.token_set ? 'One is saved; paste a new one to replace it.' : 'Plex Web → any item → ⋯ → Get Info → View XML: the X-Plex-Token in the address.'}>
            <input className="field" type="password" autoComplete="off" value={token} onChange={(e) => setToken(e.target.value)} placeholder={p.token_set ? '•••••• (set)' : 'X-Plex-Token'} />
          </Row>
        )}
        <Buttons
          s={s}
          testLabel="test connection"
          onTest={() => s.test('plex', values(), 'Plex')}
          saveLabel={p.enabled ? 'Save' : 'Watch Plex'}
          onSave={async () => {
            if (await s.test('plex', values(), 'Plex')) s.save('Plex', { ...values(), PLEX_MONITOR: 'true' });
          }}
          disabled={!url || (!useFound && !token && !p.token_set)}
        />
      </form>
    </Step>
  );
}

function QBittorrentStep({ st, s }: { st: SetupStatus; s: Saver }) {
  const q = st.qbittorrent;
  const d = q.detected;
  const found = d.found && !q.enabled;
  const [host, setHost] = useState(found ? d.host : q.host);
  const [port, setPort] = useState(String(found ? d.port : q.port));
  const [username, setUsername] = useState(found && d.username ? d.username : q.username);
  const [password, setPassword] = useState('');
  const values = () => ({ QBITTORRENT_HOST: host, QBITTORRENT_PORT: port, QBITTORRENT_USERNAME: username, ...(password ? { QBITTORRENT_PASSWORD: password } : {}) });
  const tone: Tone = q.enabled ? 'good' : d.found ? 'warn' : 'idle';
  return (
    <Step n={3} title="qBittorrent" tone={tone} status={q.enabled ? 'watching' : d.found ? 'found on this machine' : 'not found'}>
      <p className="setup-why">
        {d.found ? `Found its Web UI settings${d.answering ? `, answering on port ${d.port}` : ''}.` : 'No qBittorrent settings found on this machine.'} Watching it adds the qBittorrent page, restarts
        stalled downloads and can clean up by seeding ratio.
      </p>
      <form onSubmit={stop}>
        <div className="setup-grid">
          <Row label="Host">
            <input className="field" value={host} onChange={(e) => setHost(e.target.value)} />
          </Row>
          <Row label="Port">
            <input className="field" inputMode="numeric" value={port} onChange={(e) => setPort(e.target.value)} />
          </Row>
          <Row label="Username">
            <input className="field" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" />
          </Row>
          <Row label="Password" hint={q.password_set ? 'One is saved; type to replace it.' : 'The Web UI password.'}>
            <input className="field" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="off" placeholder={q.password_set ? '•••••• (set)' : ''} />
          </Row>
        </div>
        <Buttons
          s={s}
          testLabel="test connection"
          onTest={() => s.test('qbittorrent', values(), 'qBittorrent')}
          saveLabel={q.enabled ? 'Save' : 'Watch qBittorrent'}
          onSave={async () => {
            if (await s.test('qbittorrent', values(), 'qBittorrent')) s.save('qBittorrent', { ...values(), QBITTORRENT_MONITOR: 'true' });
          }}
          disabled={!host || !port || !username || (!password && !q.password_set)}
        />
      </form>
    </Step>
  );
}

function ChecksStep({ st, refresh }: { st: SetupStatus; refresh: () => void }) {
  const notify = useToast();
  const suggestions = st.checks.suggestions;
  const [picked, setPicked] = useState<Set<string>>(() => new Set(suggestions.map((x) => x.target)));
  const [adding, setAdding] = useState(false);
  // picked can still hold targets added earlier (they leave the suggestions on
  // refresh), so count only what is listed and ticked now.
  const chosen = suggestions.filter((x) => picked.has(x.target));
  const add = async () => {
    setAdding(true);
    const failed: string[] = [];
    for (const c of chosen) {
      try {
        await api.saveCheck(check(c));
      } catch (err) {
        failed.push(`${c.name}: ${errorText(err)}`);
      }
    }
    setAdding(false);
    if (failed.length) notify({ tone: 'error', title: 'Some checks were not added', body: failed.join('; ') });
    else notify({ tone: 'ok', title: `Added ${plural(chosen.length, 'check')}`, body: 'They run on the next cycle.' });
    refresh();
  };
  const done = st.checks.count > 0;
  return (
    <Step n={4} title="Uptime checks" tone={done ? 'good' : 'warn'} status={done ? `${st.checks.count} watched` : 'none yet'}>
      <p className="setup-why">Checks ping your services and alert when one stops answering. These apps answer on this machine:</p>
      {suggestions.length ? (
        <>
          <ul className="setup-suggestions">
            {suggestions.map((c) => (
              <li key={c.target}>
                <label className="setup-pick">
                  <input
                    type="checkbox"
                    className="select-box"
                    checked={picked.has(c.target)}
                    onChange={(e) =>
                      setPicked((p) => {
                        const n = new Set(p);
                        if (e.target.checked) n.add(c.target);
                        else n.delete(c.target);
                        return n;
                      })
                    }
                  />
                  <span>
                    <strong>{c.name}</strong> <span className="mono ink-3">{c.target}</span>
                  </span>
                </label>
              </li>
            ))}
          </ul>
          <div className="setup-actions">
            <Link className="btn-quiet" to="/services#add">
              Add another
            </Link>
            <button className="btn" disabled={adding || chosen.length === 0} onClick={add}>
              {adding ? 'Adding…' : `Add ${plural(chosen.length, 'check')}`}
            </button>
          </div>
        </>
      ) : (
        <p className="note" style={{ padding: 0 }}>
          {done ? 'Everything found here is already watched.' : 'Nothing found to suggest.'} <Link to="/services#add">Add a check by hand →</Link>
        </p>
      )}
    </Step>
  );
}

const check = (c: CheckSuggestion) => ({
  name: c.name,
  type: c.type,
  target: c.target,
  interval: 60,
  timeout: 10,
  retries: 2,
  expected_status: null,
  keyword: '',
  verify_tls: true,
  tls_warn_days: 14,
  paused: false,
  retry_interval: 20,
  ping_count: 3,
  ping_interval: 0.2,
});

function UpdatesStep({ st, s }: { st: SetupStatus; s: Saver }) {
  const u = st.updates;
  return (
    <Step n={5} title="Updates" tone={u.enabled ? 'good' : 'warn'} status={u.enabled ? 'checking' : 'off'}>
      <p className="setup-why">
        {u.targets
          ? `${plural(u.targets, 'thing')} to keep updated ${u.targets === 1 ? 'was' : 'were'} found among OS packages, snaps, this monitor and apps like Sonarr and Plex.`
          : 'Nightjar can keep OS packages, snaps, this monitor and apps like Sonarr and Plex up to date, though none were found here yet.'}{' '}
        Turn on checks to see what's new; choose per item on the <Link to="/updates">Updates page</Link> which install by themselves ({u.window}).
      </p>
      {u.enabled ? (
        <div className="setup-actions">
          <Link className="btn-quiet" to="/updates">
            Open the Updates page →
          </Link>
        </div>
      ) : (
        <form onSubmit={stop}>
          <Buttons s={s} saveLabel="Turn on update checks" onSave={() => s.save('Update checks', { SYSTEM_UPDATE_ENABLED: 'true' })} />
        </form>
      )}
    </Step>
  );
}

export function SetupPage() {
  const [st, setSt] = useState<SetupStatus>();
  const [error, setError] = useState('');
  const refresh = () =>
    api.setup().then(
      (v) => {
        setSt(v);
        setError('');
      },
      (e: Error) => setError(e.message),
    );
  useEffect(() => {
    refresh();
  }, []);
  const s = useSaver(refresh);

  if (s.restarting) {
    return (
      <main>
        <Working text="Saved. Restarting the monitor to connect…" />
      </main>
    );
  }
  const done = st ? [st.alerts.configured, st.plex.enabled, st.qbittorrent.enabled, st.checks.count > 0, st.updates.enabled].filter(Boolean).length : 0;
  return (
    <main className="setup">
      <div className="page-title">
        <div>
          <h1>Set up</h1>
          <div className="page-sub">
            Any order, each step saves on its own. Everything here is also under Settings. Not sure about a step? <Link to="/docs/getting-started#run-the-setup-guide">The docs</Link> explain each one.
          </div>
        </div>
        {st && (
          <div className="setup-progress" aria-label={`${done} of 5 set up`}>
            <span className="num">{done}/5</span>
            <span className="setup-bar">
              <span style={{ width: `${(done / 5) * 100}%` }} />
            </span>
          </div>
        )}
      </div>
      {error && <p className="alert">{error}</p>}
      {!st && !error && <Working text="Looking around this machine…" />}
      {st && (
        <div className="setup-steps">
          <AlertsStep st={st} s={s} />
          <PlexStep st={st} s={s} />
          <QBittorrentStep st={st} s={s} />
          <ChecksStep st={st} refresh={refresh} />
          <UpdatesStep st={st} s={s} />
        </div>
      )}
    </main>
  );
}
