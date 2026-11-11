import { type ReactNode, useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { ApiError, api, waitForRestart } from '../api/client';
import type { SettingField, SettingsResponse, TestTarget } from '../api/types';
import { AccountSection, ViewersSection } from '../components/Account';
import { SectionsPanel } from '../components/Sections';
import { ActivityPanel } from '../components/ActivityPanel';
import { AppearancePanel } from '../components/AppearancePanel';
import { BackupPanel } from '../components/BackupPanel';
import { GroupIcon, SettingsGroup } from '../components/SettingsGroup';
import { Select } from '../components/Select';
import { Skeleton, Working } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { noun } from '../format';
import { useUnsaved } from '../freshness';

const SOURCE: Record<SettingField['source'], string> = { dashboard: 'set here', environment: 'from .env', default: '' };

const DRAFT_KEY = 'nightjar.settings.draft';

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
  // A reset (an empty draft) shows the default it goes back to, not a switch that reads Off
  // whatever the default is; typed fields show it as their placeholder instead, so they can
  // still be emptied to type afresh. (A value from .env, if set, wins over the default.)
  const picked = value === '' ? field.default : current;
  const label = field.label;
  if (field.startup) {
    return <span className="set-fixed mono">{field.kind === 'secret' ? (field.set ? 'set' : 'not set') : current || '—'}</span>;
  }
  switch (field.kind) {
    case 'bool':
      return (
        <label className="check set-switch">
          <input type="checkbox" aria-label={label} checked={picked === 'true'} onChange={(e) => onChange(String(e.target.checked))} /> {picked === 'true' ? 'On' : 'Off'}
        </label>
      );
    case 'secret':
      return (
        <input
          className="field set-input"
          type="password"
          aria-label={label}
          autoComplete="off"
          placeholder={field.set ? '•••••• (set — type to replace)' : 'not set'}
          value={value ?? ''}
          onChange={(e) => onChange(e.target.value)}
        />
      );
    case 'int':
    case 'number':
      // The unit sits inside the field, so every number lines up the same way.
      return (
        <span className={`set-number ${field.unit ? 'has-unit' : ''}`}>
          <input
            className="field"
            type="number"
            aria-label={label}
            step={field.kind === 'int' ? 1 : 'any'}
            min={field.min}
            max={field.max}
            placeholder={field.default}
            value={current}
            onChange={(e) => onChange(e.target.value)}
          />
          {field.unit && <span className="set-unit">{field.unit}</span>}
        </span>
      );
    default:
      if (field.options) {
        return <Select className="set-select" label={label} value={picked} onChange={onChange} options={field.options.map((o) => ({ value: o, label: o }))} />;
      }
      return <input className="field set-input" aria-label={label} value={current} placeholder={field.default} onChange={(e) => onChange(e.target.value)} />;
  }
}

const RESTART_ICON = 'M20 11a8 8 0 0 0-14.6-4.5M4 4v4h4M4 13a8 8 0 0 0 14.6 4.5M20 20v-4h-4';
const LOCK_ICON = 'M6 11h12v9H6zM8 11V8a4 4 0 0 1 8 0v3';

function Badge({ icon, children, title }: { icon?: string; children: ReactNode; title?: string }) {
  return (
    <span className="set-badge" title={title}>
      {icon && (
        <svg viewBox="0 0 24 24" aria-hidden="true">
          <path d={icon} />
        </svg>
      )}
      {children}
    </span>
  );
}

function Field({
  field,
  value,
  error,
  onChange,
  onReset,
  onUndo,
}: {
  field: SettingField;
  value: string | undefined;
  error?: string;
  onChange: (v: string) => void;
  onReset: () => void;
  onUndo: () => void;
}) {
  const changed = value !== undefined;
  return (
    <div className={`set-row ${changed ? 'changed' : ''} ${error ? 'invalid' : ''}`} id={field.key}>
      <div className="set-text">
        <span className="set-label">{field.label}</span>
        {field.help && <p className="set-help">{field.help}</p>}
        <div className="set-meta">
          <code title="The setting's name in .env">{field.key}</code>
          {field.startup && (
            <Badge icon={LOCK_ICON} title="Change it in .env, then restart the monitor">
              set in .env
            </Badge>
          )}
          {!field.startup && !field.live && (
            <Badge icon={RESTART_ICON} title="Saving this restarts the monitor">
              restarts
            </Badge>
          )}
          {!field.startup && SOURCE[field.source] && <Badge>{SOURCE[field.source]}</Badge>}
        </div>
      </div>
      <div className="set-control">
        <div className="set-control-line">
          <Control field={field} value={value} onChange={onChange} />
          {changed ? (
            <button type="button" className="btn-quiet set-undo" onClick={onUndo} title="Put back the saved value">
              Undo
            </button>
          ) : (
            !field.startup &&
            field.source === 'dashboard' && (
              <button type="button" className="btn-quiet set-undo" onClick={onReset} title="Use the .env value or the default again">
                Reset
              </button>
            )
          )}
        </div>
        {changed && value === '' && <div className="set-note">Back to the .env value or the default.</div>}
        {error && <div className="form-error">{error}</div>}
      </div>
    </div>
  );
}

// useInView follows which group is on screen, for the sidebar: the last one whose top has passed under the header.
// It measures on scroll rather than observing, since Account and Sections draw after their own requests.
function useInView(ids: string[]): string {
  const [current, setCurrent] = useState('');
  const key = ids.join(',');
  useEffect(() => {
    if (!ids.length) return;
    let frame = 0;
    const measure = () => {
      frame = 0;
      const atEnd = window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 4;
      let found = ids[0];
      for (const id of ids) {
        const top = document.getElementById(id)?.getBoundingClientRect().top;
        if (top !== undefined && top <= 200) found = id;
      }
      setCurrent(atEnd ? ids[ids.length - 1] : found);
    };
    const onScroll = () => {
      if (!frame) frame = requestAnimationFrame(measure);
    };
    measure();
    window.addEventListener('scroll', onScroll, { passive: true });
    window.addEventListener('resize', onScroll);
    return () => {
      window.removeEventListener('scroll', onScroll);
      window.removeEventListener('resize', onScroll);
      if (frame) cancelAnimationFrame(frame);
    };
  }, [key]);
  return current;
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
      return localStorage.getItem('nightjar.settings.advanced') === 'true';
    } catch {
      return false;
    }
  });
  const notify = useToast();
  const toggleAdvanced = (on: boolean) => {
    setShowAdvanced(on);
    try {
      localStorage.setItem('nightjar.settings.advanced', String(on));
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
  // A link to a part of the page (/settings#sections, from the docs) lands on it once it is drawn.
  const loaded = !!data;
  useEffect(() => {
    const id = window.location.hash.slice(1);
    if (loaded && id) document.getElementById(id)?.scrollIntoView?.({ block: 'start' });
  }, [loaded]);

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

  const restartNames = needsRestart.map((k) => fieldsByKey.get(k)?.label ?? k).join(', ');
  const changes = Object.keys(draft).length;
  // Many fields are custom pickers outside a form's inputs: hold off reloading onto a new build.
  useUnsaved(changes > 0);
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
  // An emptied secret box means "no change"; "reset" is how a saved secret is removed. A value put
  // back to the saved one is no change either, so there is nothing to save.
  // Editing a field also clears the error a failed save left on it: it may be fixed now, and
  // the next save says again if not.
  const editField = (f: SettingField, value: string) => {
    setErrors(({ [f.key]: _, ...rest }) => rest);
    if ((f.kind === 'secret' && value === '') || (f.kind !== 'secret' && value === f.value)) setDraft(({ [f.key]: _, ...rest }) => rest);
    else edit(f.key, value);
  };
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

  const navItems = [...(query ? [] : ['Account', 'Viewers', 'Sections', 'Appearance', 'Activity', 'Backup']), ...groups.map((g) => g.name)];
  const changedIn = (group: string) => (data?.groups.find((g) => g.name === group)?.fields ?? []).filter((f) => f.key in draft).length;

  return (
    <main className={`settings-page ${changes ? 'has-savebar' : ''}`}>
      <div className="page-title">
        <div>
          <h1>Settings</h1>
          <div className="page-sub">
            New here? The <Link to="/setup">setup guide</Link> finds Plex, qBittorrent and your apps for you.
          </div>
        </div>
        <div className="toolbar set-toolbar">
          <label className="set-search">
            <svg viewBox="0 0 24 24" aria-hidden="true">
              <circle cx="11" cy="11" r="7" />
              <path d="M20 20l-4-4" />
            </svg>
            <input type="search" placeholder="Find a setting" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Find a setting" />
          </label>
          <label className="check set-advanced" title="Tuning knobs most setups never change">
            <input type="checkbox" checked={showAdvanced} onChange={(e) => toggleAdvanced(e.target.checked)} />
            {!showAdvanced && hidden > 0 ? `Show advanced (${hidden} more)` : 'Show advanced'}
          </label>
        </div>
      </div>
      {loadError && <p className="alert">{loadError}</p>}
      {!data && !loadError && <Skeleton kind="form" />}
      {data && (
        <div className="settings-layout">
          <SettingsNav items={navItems} changedIn={changedIn} />
          <div className="settings-groups">
            {!query && <AccountSection />}
            {!query && <ViewersSection />}
            {!query && <SectionsPanel />}
            {!query && <AppearancePanel />}
            {!query && <ActivityPanel />}
            {!query && <BackupPanel />}
            {groups.map((g) => (
              <SettingsGroup
                key={g.name}
                id={slug(g.name)}
                name={g.name}
                aside={
                  <>
                    {g.name === 'Storage and logging' && (
                      <>
                        <Link className="btn-ghost" to="/plugins">
                          Plugins →
                        </Link>
                        <Link className="btn-ghost" to="/logs">
                          Open the monitor log →
                        </Link>
                      </>
                    )}
                    {g.name === 'Dashboard' && (
                      <Link className="btn-ghost" to="/status-page">
                        Design the status page →
                      </Link>
                    )}
                    {TESTS[g.name] && (
                      <button type="button" className="btn-ghost" onClick={() => runTest(TESTS[g.name].target)} disabled={!!testing} title={TESTS[g.name].title}>
                        {testing === TESTS[g.name].target ? 'Testing…' : TESTS[g.name].label}
                      </button>
                    )}
                  </>
                }
              >
                {g.fields.map((f) => (
                  <Field
                    key={f.key}
                    field={f}
                    value={draft[f.key]}
                    error={errors[f.key]}
                    onChange={(v) => editField(f, v)}
                    onReset={() => {
                      edit(f.key, '');
                      setErrors(({ [f.key]: _, ...rest }) => rest);
                    }}
                    onUndo={() => {
                      setDraft(({ [f.key]: _, ...rest }) => rest);
                      setErrors(({ [f.key]: _, ...rest }) => rest);
                    }}
                  />
                ))}
              </SettingsGroup>
            ))}
            {groups.length === 0 && (
              <div className="set-empty">
                <strong>No setting matches “{query}”</strong>
                <span>Try a shorter word, or the setting's name from .env.</span>
              </div>
            )}
          </div>
        </div>
      )}
      {changes > 0 && (
        <div className="savebar" role="region" aria-label="Unsaved changes">
          <span className="savebar-dot" aria-hidden="true" />
          <span className="savebar-text">
            <strong>
              {changes} unsaved {noun(changes, 'change')}
            </strong>
            {saveError ? (
              <span className="savebar-note error">{saveError}</span>
            ) : needsRestart.length ? (
              <span className="savebar-note" title={restartNames}>
                <svg viewBox="0 0 24 24" aria-hidden="true">
                  <path d={RESTART_ICON} />
                </svg>
                <span className="truncate">Restarts the monitor ({restartNames})</span>
              </span>
            ) : (
              <span className="savebar-note">Applies at once</span>
            )}
          </span>
          <span className="savebar-actions">
            <button type="button" className="btn-quiet" onClick={discard}>
              Discard
            </button>
            <button type="button" className="btn" onClick={save} disabled={state === 'saving'}>
              {state === 'saving' ? 'Saving…' : needsRestart.length ? 'Save and restart' : 'Save'}
            </button>
          </span>
        </div>
      )}
    </main>
  );
}

// SettingsNav lists the groups with their icons, marks the one on screen, and counts unsaved changes in each.
function SettingsNav({ items, changedIn }: { items: string[]; changedIn: (group: string) => number }) {
  const ids = items.map(slug);
  const current = useInView(ids);
  const nav = useRef<HTMLElement>(null);
  // On narrow screens the groups are a sideways row; keep the current one in view.
  useEffect(() => {
    const el = nav.current;
    const link = el?.querySelector<HTMLElement>('a.active');
    if (el && link && el.scrollWidth > el.clientWidth) el.scrollTo?.({ left: link.offsetLeft - 16, behavior: 'smooth' });
  }, [current]);
  return (
    <nav className="settings-nav" aria-label="Setting groups" ref={nav}>
      {items.map((name, i) => {
        const n = changedIn(name);
        return (
          <a key={name} href={`#${ids[i]}`} className={current === ids[i] ? 'active' : ''} aria-current={current === ids[i] ? 'location' : undefined}>
            <GroupIcon name={name} />
            <span className="min0">{name}</span>
            {n > 0 && (
              <span className="set-nav-count" title={`${n} unsaved`}>
                {n}
              </span>
            )}
          </a>
        );
      })}
    </nav>
  );
}
