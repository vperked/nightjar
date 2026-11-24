import { useEffect, useState } from 'react';
import { Switch } from '../components/ui';
import { Link } from 'react-router';
import { api, waitForRestart, errorText } from '../api/client';
import type { CheckResult, Plugin, PluginCard, PluginProvides, PluginSetting, PluginsResponse } from '../api/types';
import { PluginCardBody } from '../components/PluginCards';
import { Select } from '../components/Select';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { formatAgo } from '../format';
import { useNow } from '../hooks/useNow';

// Plugins: the owner's own checks, overview cards and alert channels, each a folder in the
// plugins folder. A plugin found there starts off; turning it on lets Nightjar run it.

const PROVIDES: Record<PluginProvides, string> = { check: 'Check', card: 'Overview card', notify: 'Alert channel' };

function Field({ s, value, secretSet, onChange, onClear }: { s: PluginSetting; value: string; secretSet: boolean; onChange: (v: string) => void; onClear: () => void }) {
  const id = `plugin-${s.key}`;
  let input;
  switch (s.type) {
    case 'bool':
      input = (
        <label className="check">
          <input id={id} type="checkbox" checked={value === 'true'} onChange={(e) => onChange(String(e.target.checked))} /> {s.label}
        </label>
      );
      break;
    case 'select':
      input = <Select label={s.label} value={value} onChange={onChange} options={(s.options ?? []).map((o) => ({ value: o, label: o }))} />;
      break;
    case 'secret':
      input = (
        <span className="plugin-secret">
          <input
            id={id}
            className="field"
            type="password"
            autoComplete="new-password"
            value={value}
            placeholder={secretSet ? 'Saved. Type to replace it' : (s.placeholder ?? '')}
            onChange={(e) => onChange(e.target.value)}
          />
          {secretSet && (
            <button type="button" className="btn-quiet" onClick={onClear}>
              Clear
            </button>
          )}
        </span>
      );
      break;
    default:
      input = (
        <input
          id={id}
          className="field"
          inputMode={s.type === 'number' ? 'decimal' : undefined}
          value={value}
          placeholder={s.placeholder ?? s.default ?? ''}
          onChange={(e) => onChange(e.target.value)}
        />
      );
  }
  return (
    <div className="plugin-field">
      {s.type !== 'bool' && (
        <label className="plugin-field-label" htmlFor={id}>
          {s.label}
          {s.required && <span className="plugin-required"> required</span>}
        </label>
      )}
      {input}
      {s.help && <span className="form-help">{s.help}</span>}
    </div>
  );
}

type Tried = { check?: CheckResult; card?: PluginCard; error?: string };

function PluginItem({ p, onChanged }: { p: Plugin; onChanged: (p: Plugin) => void }) {
  const notify = useToast();
  const now = useNow(30000);
  const [values, setValues] = useState<Record<string, string>>({});
  const [clear, setClear] = useState<string[]>([]);
  const [busy, setBusy] = useState('');
  const [argument, setArgument] = useState('');
  const [tried, setTried] = useState<Tried>();
  const [wantOn, setWantOn] = useState<boolean>();
  const settings = p.settings ?? [];
  // Only values that differ from the saved ones are changes; a secret typed is always one.
  const changed = Object.fromEntries(
    Object.entries(values).filter(([k, v]) => {
      const s = settings.find((x) => x.key === k);
      return s?.type === 'secret' ? v !== '' : v !== (p.values[k] ?? '');
    }),
  );
  const dirty = Object.keys(changed).length > 0 || clear.length > 0;
  const valueOf = (s: PluginSetting) => values[s.key] ?? (s.type === 'secret' ? '' : (p.values[s.key] ?? ''));

  // save sends the settings typed (Save), or only the on/off state (the switch): flipping it should
  // not also save half-typed fields, which stay in the form to save or discard.
  const save = async (enabled?: boolean) => {
    const toggling = enabled !== undefined;
    setBusy(toggling ? 'toggle' : 'save');
    // The switch moves at once; it goes back if the change is refused.
    if (toggling) setWantOn(enabled);
    try {
      const { plugin } = await api.configurePlugin(p.id, toggling ? { enabled } : { settings: Object.keys(changed).length ? changed : undefined, clear: clear.length ? clear : undefined });
      if (!toggling) {
        setValues({});
        setClear([]);
      }
      onChanged(plugin);
      notify({
        tone: 'ok',
        title: toggling ? `${p.name} is ${enabled ? 'on' : 'off'}` : `${p.name}: settings saved`,
        body: enabled && (p.provides ?? []).includes('notify') ? 'Choose what it hears about under Notifications.' : undefined,
      });
    } catch (err) {
      notify({ tone: 'error', title: `Could not change ${p.name}`, body: errorText(err) });
    } finally {
      setBusy('');
      setWantOn(undefined);
    }
  };
  const tryIt = async (mode: 'check' | 'card') => {
    setBusy(mode);
    setTried(undefined);
    try {
      setTried(await api.tryPlugin(p.id, mode, argument));
    } catch (err) {
      setTried({ error: errorText(err) });
    } finally {
      setBusy('');
    }
  };

  return (
    <li className={`plugin ${p.enabled ? 'on' : ''} ${p.problem ? 'broken' : ''}`}>
      <div className="plugin-head">
        <span className="plugin-tile" aria-hidden="true">
          {p.name.slice(0, 1).toUpperCase()}
        </span>
        <div className="min0 plugin-title">
          <div className="plugin-name">
            <strong>{p.name}</strong>
            {p.version && <span className="plugin-version num">v{p.version}</span>}
          </div>
          {p.description && <p className="plugin-desc">{p.description}</p>}
          <div className="plugin-provides">
            {(p.provides ?? [])
              .filter((x) => PROVIDES[x])
              .map((x) => (
                <span key={x} className="plugin-chip">
                  {PROVIDES[x]}
                </span>
              ))}
            {p.author && <span className="plugin-by">by {p.author}</span>}
          </div>
        </div>
        <Switch on={wantOn ?? p.enabled} label={`Turn ${p.name} ${(wantOn ?? p.enabled) ? 'off' : 'on'}`} disabled={!!p.problem || !!busy} onChange={(on) => save(on)} />
      </div>

      {p.problem && (
        <p className="plugin-problem" role="note">
          <strong>Can't run.</strong> {p.problem}
        </p>
      )}
      {!p.problem && !!p.missing?.length && !p.enabled && <p className="plugin-missing">Fill in {p.missing.join(', ')} to turn it on.</p>}

      {settings.length > 0 && !p.problem && (
        <div className="plugin-settings">
          {settings.map((s) => (
            <Field
              key={s.key}
              s={s}
              value={valueOf(s)}
              secretSet={p.secrets_set.includes(s.key) && !clear.includes(s.key)}
              onChange={(v) => setValues((x) => ({ ...x, [s.key]: v }))}
              onClear={() => setClear((c) => [...c, s.key])}
            />
          ))}
        </div>
      )}

      {!p.problem && (
        <div className="plugin-foot">
          <span className="plugin-last small">
            {p.last ? (
              <>
                <span className={`plugin-last-dot ${p.last.ok ? 'ok' : 'bad'}`} aria-hidden="true" />
                Last ran {formatAgo(p.last.at, now)} as {p.last.mode === 'notify' ? 'an alert' : `a ${p.last.mode}`}, {Math.round(p.last.ms)} ms
                {p.last.output ? <span className="plugin-last-out truncate">· {p.last.output}</span> : null}
              </>
            ) : (
              <span className="ink-3">Not run yet</span>
            )}
          </span>
          <span className="plugin-actions">
            {(p.provides ?? []).includes('check') && p.check?.argument && (
              <input className="field plugin-arg" aria-label={p.check.argument} placeholder={p.check.placeholder ?? p.check.argument} value={argument} onChange={(e) => setArgument(e.target.value)} />
            )}
            {(p.provides ?? []).includes('check') && (
              <button type="button" className="btn-ghost" disabled={!!busy || dirty} onClick={() => tryIt('check')} title={dirty ? 'Save first' : undefined}>
                {busy === 'check' ? 'Checking…' : 'Try the check'}
              </button>
            )}
            {(p.provides ?? []).includes('card') && (
              <button type="button" className="btn-ghost" disabled={!!busy || dirty} onClick={() => tryIt('card')} title={dirty ? 'Save first' : undefined}>
                {busy === 'card' ? 'Running…' : 'Try the card'}
              </button>
            )}
            {dirty && (
              <>
                <button
                  type="button"
                  className="btn-quiet"
                  onClick={() => {
                    setValues({});
                    setClear([]);
                  }}
                >
                  Discard
                </button>
                <button type="button" className="btn" disabled={!!busy} onClick={() => save()}>
                  {busy === 'save' ? 'Saving…' : 'Save'}
                </button>
              </>
            )}
          </span>
        </div>
      )}

      {tried && (
        <div className="plugin-tried" role="status">
          {tried.error && <p className="plugin-card-error">{tried.error}</p>}
          {tried.check && (
            <p className={`plugin-tried-check ${tried.check.up ? 'up' : 'down'}`}>
              <strong>{tried.check.up ? 'Up' : 'Down'}</strong>
              {tried.check.detail && <span> · {tried.check.detail}</span>}
              {tried.check.latency_ms != null && <span className="ink-3 num"> · {Math.round(tried.check.latency_ms)} ms</span>}
            </p>
          )}
          {tried.card && (
            <div className="plugin-tried-card">
              <span className="label">{tried.card.title}</span>
              <PluginCardBody card={tried.card} />
            </div>
          )}
        </div>
      )}
      {p.enabled && (p.provides ?? []).includes('check') && (
        <p className="form-help plugin-hint">
          Use it in <Link to="/services">Services</Link>: add a check of type <strong>plugin</strong> with the target <code>{p.id}</code>
          {p.check?.argument ? (
            <>
              {' '}
              or{' '}
              <code>
                {p.id}:{p.check.placeholder ?? 'argument'}
              </code>
            </>
          ) : null}
          .
        </p>
      )}
    </li>
  );
}

export function PluginsPage() {
  const [data, setData] = useState<PluginsResponse>();
  const [error, setError] = useState('');
  const [scanning, setScanning] = useState(false);
  const notify = useToast();
  useEffect(() => {
    api.plugins().then(setData, (e: Error) => setError(e.message));
  }, []);
  // Look again also retries a first read that failed, so the page is not stuck on its error.
  const rescan = async () => {
    setScanning(true);
    try {
      const next = await api.rescanPlugins();
      const added = data ? next.plugins.length - data.plugins.length : 0;
      setData(next);
      setError('');
      const found = added > 0 ? `Found ${added} new.` : `${next.plugins.length} plugins.`;
      // A plugin that delivers alerts, added, renamed or removed, gets its channel when Nightjar starts.
      notify({ tone: 'ok', title: 'Looked in the plugins folder', body: next.restarting ? `${found} Restarting to set up its alert channel; the page reconnects by itself.` : found });
      if (next.restarting) await waitForRestart();
    } catch (err) {
      notify({ tone: 'error', title: 'Could not read the plugins folder', body: errorText(err) });
    } finally {
      setScanning(false);
    }
  };
  const replace = (p: Plugin) => setData((d) => d && { ...d, plugins: d.plugins.map((x) => (x.id === p.id ? p : x)) });

  return (
    <main>
      <div className="page-title">
        <div>
          <div className="crumb">
            <Link to="/settings">Settings</Link> /
          </div>
          <h1>Plugins</h1>
          <div className="page-sub">Your own checks, overview cards and alert channels. Nightjar runs a plugin only once you turn it on.</div>
        </div>
        <div className="toolbar">
          <Link className="btn-quiet" to="/docs/plugins">
            Write a plugin
          </Link>
          <button type="button" className="btn-quiet" onClick={rescan} disabled={scanning || (!data && !error)}>
            {scanning ? 'Looking…' : 'Look again'}
          </button>
        </div>
      </div>
      {error && <p className="alert">{error}</p>}
      {!data && !error && <Skeleton kind="list" rows={3} />}
      {data && (
        <p className="plugin-dir small ink-3">
          Plugins folder <code className="mono">{data.dir}</code>. Put each plugin in its own folder there, then press <strong>Look again</strong>.
        </p>
      )}
      {data && data.plugins.length === 0 && (
        <div className="empty plugin-empty">
          <strong>No plugins yet</strong>
          <span>A plugin is a folder with a plugin.json and a program. To start, copy the examples from the Nightjar source:</span>
          <pre className="mono">cp -r examples/plugins/* {data.dir}/</pre>
          <Link to="/docs/plugins">How plugins work →</Link>
        </div>
      )}
      {data && data.plugins.length > 0 && (
        <ul className="plugin-list">
          {data.plugins.map((p) => (
            <PluginItem key={p.id + p.dir} p={p} onChanged={replace} />
          ))}
        </ul>
      )}
    </main>
  );
}
