import { useEffect, useMemo, useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { ApiError, api, waitForRestart } from '../api/client';
import type { SettingField, SettingsResponse, TestTarget } from '../api/types';
import { AccountSection } from '../components/Account';
import { SectionsPanel } from '../components/Sections';
import { Select } from '../components/Select';
import { Skeleton, Working } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { noun } from '../format';

const SOURCE: Record<SettingField['source'], string> = { dashboard: 'set here', environment: 'from .env', default: '' };

const DRAFT_KEY = 'servermonitor.settings.draft';

// Groups with a connection test: it uses the values on screen, saved or not.
const TESTS: Record<string, { target: TestTarget; label: string; title: string }> = {
  Notifications: { target: 'alerts', label: 'Send test alert', title: 'Send a test alert to every channel, using the values on screen' },
  'Alert channels': { target: 'alerts', label: 'Send test alert', title: 'Send a test alert to every channel, using the values on screen' },
  Plex: { target: 'plex', label: 'Test connection', title: 'Connect to Plex with the values on screen' },
  qBittorrent: { target: 'qbittorrent', label: 'Test connection', title: 'Log in to qBittorrent with the values on screen' },
};

const slug = (group: string) => group.toLowerCase().replace(/\W+/g, '-');

function Control({ field, value, onChange }: { field: SettingField; value: string | undefined; onChange: (v: string) => void }) {
  const current = value ?? field.value;
  const label = field.label;
  if (field.startup) {
    return <span className="mono ink-2">{field.kind === 'secret' ? (field.set ? 'set' : 'not set') : current || '—'}</span>;
  }
  switch (field.kind) {
    case 'bool':
      return (
        <label className="check">
          <input type="checkbox" aria-label={label} checked={current === 'true'} onChange={(e) => onChange(String(e.target.checked))} /> {current === 'true' ? 'on' : 'off'}
        </label>
      );
    case 'secret':
      return (
        <span className="with-unit">
          <input
            className="field"
            type="password"
            aria-label={label}
            autoComplete="off"
            placeholder={field.set ? '•••••• (set — type to replace)' : 'not set'}
            value={value ?? ''}
            onChange={(e) => onChange(e.target.value)}
          />
        </span>
      );
    case 'int':
    case 'number':
      return (
        <span className="with-unit">
          <input
            className="field short"
            type="number"
            aria-label={label}
            step={field.kind === 'int' ? 1 : 'any'}
            min={field.min}
            max={field.max}
            value={current}
            onChange={(e) => onChange(e.target.value)}
          />
          {field.unit && <span className="unit">{field.unit}</span>}
        </span>
      );
    default:
      if (field.options) {
        return <Select className="short" label={label} value={current} onChange={onChange} options={field.options.map((o) => ({ value: o, label: o }))} />;
      }
      return <input className="field" aria-label={label} value={current} placeholder={field.default} onChange={(e) => onChange(e.target.value)} />;
  }
}

function Field({ field, value, error, onChange, onReset }: { field: SettingField; value: string | undefined; error?: string; onChange: (v: string) => void; onReset: () => void }) {
  const changed = value !== undefined;
  return (
    <div className={`form-row ${changed ? 'changed' : ''}`} id={field.key}>
      <div className="form-label">
        {field.label}
        <div className="form-help">
          {field.help && <span>{field.help} </span>}
          <code>{field.key}</code>
          {field.startup && <span> · set in .env</span>}
          {!field.startup && !field.live && <span title="Saving this restarts the monitor"> · restarts</span>}
          {!field.startup && SOURCE[field.source] && <span> · {SOURCE[field.source]}</span>}
        </div>
      </div>
      <div className="form-control">
        <Control field={field} value={value} onChange={onChange} />
        {!field.startup && field.source === 'dashboard' && !changed && (
          <button type="button" className="btn-quiet" onClick={onReset} title="Use the .env value or the default again">
            reset
          </button>
        )}
        {changed && value === '' && <div className="form-help">back to the .env value or default</div>}
        {error && <div className="form-error">{error}</div>}
      </div>
    </div>
  );
}

export function SettingsPage() {
  const [data, setData] = useState<SettingsResponse>();
  const [loadError, setLoadError] = useState('');
  // Unsaved edits outlive leaving the page (not the tab), except secrets, which never go to storage.
  const [draft, setDraft] = useState<Record<string, string>>(() => {
    try {
      return JSON.parse(sessionStorage.getItem(DRAFT_KEY) ?? '{}') as Record<string, string>;
    } catch {
      return {};
    }
  });
  const [errors, setErrors] = useState<Record<string, string>>({});
  // ?find= (from the finder) starts the page searched, which also reveals advanced settings.
  const [params] = useSearchParams();
  const [query, setQuery] = useState(() => params.get('find') ?? '');
  const [state, setState] = useState<'idle' | 'saving' | 'restarting'>('idle');
  const [saveError, setSaveError] = useState('');
  const [testing, setTesting] = useState<TestTarget>();
  const [showAdvanced, setShowAdvanced] = useState(() => {
    try {
      return localStorage.getItem('servermonitor.settings.advanced') === 'true';
    } catch {
      return false;
    }
  });
  const notify = useToast();
  const toggleAdvanced = (on: boolean) => {
    setShowAdvanced(on);
    try {
      localStorage.setItem('servermonitor.settings.advanced', String(on));
    } catch {
      /* storage unavailable */
    }
  };

  const load = () =>
    api.settings().then(setData, (e: Error) =>
      // A backend from before dashboard settings has no such route.
      setLoadError(e.message === 'Not Found' ? 'The running backend is older than this dashboard and has no settings yet. Restart it to load the new version.' : e.message),
    );
  useEffect(() => {
    load();
  }, []);

  // Advanced settings stay folded away unless asked for, searched for, or already changed from the default.
  // Sections have their own switches above, so their raw setting is not listed.
  const visible = (f: SettingField) => f.key !== 'DASHBOARD_HIDDEN' && (showAdvanced || !f.advanced || f.source !== 'default' || f.key in draft);
  const groups = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (data?.groups ?? [])
      .map((g) => ({ ...g, fields: g.fields.filter((f) => f.key !== 'DASHBOARD_HIDDEN' && (q ? `${f.label} ${f.key} ${f.help ?? ''} ${g.name}`.toLowerCase().includes(q) : visible(f))) }))
      .filter((g) => g.fields.length > 0);
  }, [data, query, showAdvanced, draft]);
  const fieldsByKey = useMemo(() => new Map((data?.groups ?? []).flatMap((g) => g.fields.map((f) => [f.key, f] as const))), [data]);
  const hidden = (data?.groups ?? []).reduce((n, g) => n + g.fields.filter((f) => !visible(f)).length, 0);
  // Saving restarts the monitor only for settings it builds connections or timers from at startup.
  const needsRestart = Object.keys(draft).filter((k) => !fieldsByKey.get(k)?.live);

  const changes = Object.keys(draft).length;
  useEffect(() => {
    try {
      const kept = Object.fromEntries(Object.entries(draft).filter(([k]) => fieldsByKey.get(k)?.kind !== 'secret'));
      if (Object.keys(kept).length) sessionStorage.setItem(DRAFT_KEY, JSON.stringify(kept));
      else sessionStorage.removeItem(DRAFT_KEY);
    } catch {
      /* storage unavailable */
    }
  }, [draft, fieldsByKey]);
  // Closing or reloading the tab with unsaved changes asks first.
  useEffect(() => {
    if (!changes) return;
    const warn = (e: BeforeUnloadEvent) => e.preventDefault();
    window.addEventListener('beforeunload', warn);
    return () => window.removeEventListener('beforeunload', warn);
  }, [changes]);
  const edit = (key: string, value: string) => setDraft((d) => ({ ...d, [key]: value }));
  // An emptied secret box means "no change"; "reset" is how a saved secret is removed.
  const editField = (f: SettingField, value: string) => (f.kind === 'secret' && value === '' ? setDraft(({ [f.key]: _, ...rest }) => rest) : edit(f.key, value));
  const discard = () => {
    setDraft({});
    setErrors({});
    setSaveError('');
  };
  // runTest tries a connection with the values on screen, saved or not; alerts reach every channel they set up.
  const runTest = async (target: TestTarget) => {
    setTesting(target);
    try {
      const r = await api.testSettings(target, draft);
      if (target === 'alerts') {
        if (!r.results?.length) notify({ tone: 'warning', title: 'No alert channels are set up', body: 'Add a Discord webhook or another channel first.' });
        for (const c of r.results ?? []) {
          notify(
            c.ok
              ? { tone: 'ok', title: `Test alert sent to ${c.channel}` }
              : { tone: 'error', title: c.channel === 'settings' ? 'A channel is set up wrong' : `${c.channel} did not take the test alert`, body: c.error },
          );
        }
      } else {
        const name = target === 'plex' ? 'Plex' : 'qBittorrent';
        notify(r.ok ? { tone: 'ok', title: `${name} connection works`, body: r.detail } : { tone: 'error', title: `Could not connect to ${name}`, body: r.detail });
      }
    } catch (err) {
      if (err instanceof ApiError && Object.keys(err.fields).length) setErrors(err.fields);
      notify({ tone: 'error', title: 'Could not run the test', body: (err as Error).message });
    } finally {
      setTesting(undefined);
    }
  };
  const save = async () => {
    setState('saving');
    setSaveError('');
    try {
      const r = await api.saveSettings(draft);
      // Saved: nothing to restore after the restart's reload.
      try {
        sessionStorage.removeItem(DRAFT_KEY);
      } catch {
        /* storage unavailable */
      }
      if (r.restarting) {
        setState('restarting');
        await waitForRestart();
        return;
      }
      notify({ tone: 'ok', title: 'Settings saved', body: 'They apply from the next monitoring cycle.' });
      setDraft({});
      setErrors({});
      setState('idle');
      load();
    } catch (err) {
      setState('idle');
      if (err instanceof ApiError) {
        setErrors(err.fields);
        setSaveError(err.message);
      } else {
        setSaveError('Could not save the settings.');
      }
      notify({ tone: 'error', title: 'Settings not saved', body: err instanceof ApiError && Object.keys(err.fields).length ? 'Fix the highlighted fields.' : (err as Error).message });
    }
  };

  if (state === 'restarting') {
    return (
      <main>
        <Working text="Saved. Restarting the monitor to apply the changes…" />
      </main>
    );
  }

  return (
    <main className={changes ? 'has-savebar' : ''}>
      <div className="page-title">
        <div>
          <h1>Settings</h1>
          <div className="page-sub">
            New here? The <Link to="/setup">setup guide</Link> finds Plex, qBittorrent and your apps for you.
          </div>
        </div>
        <div className="toolbar">
          <label className="check" title="Tuning knobs most setups never change">
            <input type="checkbox" checked={showAdvanced} onChange={(e) => toggleAdvanced(e.target.checked)} /> advanced{!showAdvanced && hidden > 0 ? ` (${hidden} more)` : ''}
          </label>
          <input className="search" placeholder="find a setting…" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Find a setting" />
        </div>
      </div>
      {loadError && <p className="alert">{loadError}</p>}
      {!data && !loadError && <Skeleton kind="form" />}
      {data && (
        <div className="settings-layout">
          <nav className="settings-nav" aria-label="Setting groups">
            <a href="#account">Account</a>
            <a href="#sections">Sections</a>
            {data.groups.map((g) => (
              <a key={g.name} href={`#${slug(g.name)}`}>
                {g.name}
              </a>
            ))}
          </nav>
          <div className="settings-groups">
            {!query && <AccountSection />}
            {!query && <SectionsPanel />}
            {groups.map((g) => (
              <section className="section" key={g.name} id={slug(g.name)}>
                <div className="section-head">
                  <span className="label">{g.name}</span>
                  {g.name === 'Storage and logging' && (
                    <Link className="btn-quiet" to="/logs">
                      Open the monitor log →
                    </Link>
                  )}
                  {g.name === 'Dashboard' && (
                    <Link className="btn-quiet" to="/status-page">
                      Design the status page →
                    </Link>
                  )}
                  {TESTS[g.name] && (
                    <button className="btn-quiet" onClick={() => runTest(TESTS[g.name].target)} disabled={!!testing} title={TESTS[g.name].title}>
                      {testing === TESTS[g.name].target ? 'testing…' : TESTS[g.name].label}
                    </button>
                  )}
                </div>
                {g.fields.map((f) => (
                  <Field key={f.key} field={f} value={draft[f.key]} error={errors[f.key]} onChange={(v) => editField(f, v)} onReset={() => edit(f.key, '')} />
                ))}
              </section>
            ))}
            {groups.length === 0 && <p className="note">No setting matches.</p>}
          </div>
        </div>
      )}
      {changes > 0 && (
        <div className="savebar" role="region" aria-label="Unsaved changes">
          <span>
            <span className="num">{changes}</span> unsaved {noun(changes, 'change')} ·{' '}
            {needsRestart.length ? `saving restarts the monitor (${needsRestart.map((k) => fieldsByKey.get(k)?.label ?? k).join(', ')})` : 'applies at once'}
            {saveError && <span className="form-error"> · {saveError}</span>}
          </span>
          <span className="savebar-actions">
            <button className="btn-quiet" onClick={discard}>
              discard
            </button>
            <button className="btn" onClick={save} disabled={state === 'saving'}>
              {state === 'saving' ? 'saving…' : needsRestart.length ? 'Save and restart' : 'Save'}
            </button>
          </span>
        </div>
      )}
    </main>
  );
}
