import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import { ApiError, api } from '../api/client';
import type { CheckResult, CheckSpec, CheckType, ContainersResponse, Plugin, Repair, RepairsResponse } from '../api/types';
import { pingURL } from '../components/CheckExtras';
import { Select } from '../components/Select';
import { useToast } from '../components/Toasts';
import { Seg } from '../components/ui';

const TYPES: CheckType[] = ['http', 'tcp', 'ping', 'tls', 'docker', 'plugin', 'heartbeat'];

const TARGET_HINT: Record<CheckType, string> = {
  http: 'https://example.com/health',
  tcp: 'host:port',
  ping: 'host',
  tls: 'host:443',
  plugin: 'plugin-id:argument',
  heartbeat: '',
  docker: 'container name',
};

// ContainerPick chooses a Docker container by name, from those Docker has; without Docker the
// name is typed.
function ContainerPick({ value, onChange, label }: { value: string; onChange: (v: string) => void; label: string }) {
  const [list, setList] = useState<ContainersResponse>();
  useEffect(() => {
    api.containers().then(setList, () => setList({ available: false, containers: [] }));
  }, []);
  if (list?.available && list.containers.length > 0)
    return (
      <Select
        label={label}
        value={value}
        onChange={onChange}
        options={[{ value: '', label: 'Choose a container' }, ...list.containers.map((c) => ({ value: c.name, label: c.name, hint: c.state === 'running' ? c.health : c.state }))]}
      />
    );
  return (
    <span className="inline-fields">
      <input className="field" aria-label={label} placeholder="sonarr" value={value} onChange={(e) => onChange(e.target.value)} required />
      {list && !list.available && <span className="form-help">{list.error ?? 'Docker is not reachable'}. Type the container’s name.</span>}
    </span>
  );
}

// Schedules most jobs use, offered as one click.
const SCHEDULES = [
  { value: '0 3 * * *', label: 'nightly at 3:00' },
  { value: '@hourly', label: 'hourly' },
  { value: '*/15 * * * *', label: 'every 15 min' },
  { value: 'every 1d', label: 'once a day' },
  { value: '0 4 * * 0', label: 'Sundays at 4:00' },
];

const NEW_CHECK: CheckSpec = {
  name: '',
  type: 'http',
  target: '',
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
  schedule: '',
  grace: 600,
  max_runtime: 0,
};

function Row({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="form-row">
      <div className="form-label">
        {label}
        {hint && <div className="form-help">{hint}</div>}
      </div>
      <div className="form-control">{children}</div>
    </div>
  );
}

function NumberInput({ value, onChange, unit, label, min, max, step }: { value: number; onChange: (v: number) => void; unit?: string; label: string; min?: number; max?: number; step?: number }) {
  return (
    <span className="with-unit">
      <input className="field short" type="number" aria-label={label} min={min} max={max} step={step} value={value} onChange={(e) => onChange(e.target.valueAsNumber)} />
      {unit && <span className="unit">{unit}</span>}
    </span>
  );
}

const parseStatuses = (raw: string) =>
  raw
    .split(/[\s,]+/)
    .filter(Boolean)
    .map(Number)
    .filter((n) => Number.isInteger(n));

// TestResult says whether the target answered the test, in words as well as colour.
function TestResult({ result, target }: { result: CheckResult; target: string }) {
  const ms = result.latency_ms === null ? '' : ` · ${Math.round(result.latency_ms)} ms`;
  return (
    <p className={`test-result ${result.up ? 'ok' : 'fail'}`} role="status">
      <strong>{result.up ? 'Reachable' : 'Not reachable'}</strong>
      <span>{result.up ? `${result.detail}${ms}` : `${target} did not answer: ${result.detail}. Fix the target, or save anyway to watch for it coming up.`}</span>
    </p>
  );
}

// PluginTarget picks a plugin's check and its argument; the target is the plugin's id, then a colon
// and the argument when there is one.
function PluginTarget({ target, onChange }: { target: string; onChange: (t: string) => void }) {
  const [plugins, setPlugins] = useState<Plugin[]>();
  useEffect(() => {
    api.plugins().then(
      (r) => setPlugins(r.plugins.filter((p) => p.provides.includes('check'))),
      () => setPlugins([]),
    );
  }, []);
  const [id, ...rest] = target.split(':');
  const argument = rest.join(':');
  const plugin = plugins?.find((p) => p.id === id);
  const join = (i: string, a: string) => onChange(a.trim() ? `${i}:${a}` : i);
  if (plugins && plugins.length === 0)
    return (
      <p className="form-help">
        No plugin adds a check yet. Install one under <Link to="/plugins">Plugins</Link>.
      </p>
    );
  return (
    <span className="inline-fields plugin-target">
      <Select
        label="Plugin"
        value={id}
        onChange={(v) => join(v, argument)}
        options={[{ value: '', label: plugins ? 'Choose a plugin' : 'Loading…' }, ...(plugins ?? []).map((p) => ({ value: p.id, label: p.name, hint: p.enabled ? undefined : 'off' }))]}
      />
      {(plugin?.check?.argument || argument) && (
        <input
          className="field"
          aria-label={plugin?.check?.argument || 'Argument'}
          placeholder={plugin?.check?.placeholder ?? plugin?.check?.argument ?? ''}
          value={argument}
          onChange={(e) => join(id, e.target.value)}
        />
      )}
      {plugin && !plugin.enabled && (
        <span className="form-help">
          {plugin.name} is off. Turn it on in <Link to="/plugins">Plugins</Link>, or the check stays down.
        </span>
      )}
    </span>
  );
}

// ServerField says which watched server the checked thing runs on, so that server's page lists it.
// It shows only once there are servers with the agent to choose from.
function ServerField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const [hosts, setHosts] = useState<{ id: string; name: string }[]>();
  useEffect(() => {
    api.hosts().then(
      (r) => setHosts((r.hosts ?? []).map((h) => ({ id: h.id, name: h.name }))),
      () => setHosts([]),
    );
  }, []);
  if (!hosts || (hosts.length === 0 && !value)) return null;
  const options = [{ value: '', label: 'None', hint: 'This machine, or one not watched with the agent' }, ...hosts.map((h) => ({ value: h.id, label: h.name }))];
  if (value && !hosts.some((h) => h.id === value)) options.push({ value, label: 'a removed server' });
  return (
    <Row label="Server" hint="What it runs on; that server's page lists it">
      <Select value={value} options={options} onChange={onChange} label="Server" />
    </Row>
  );
}

// RepairFields says what Nightjar does when the check goes down: nothing, restart a system
// service, or run a script from the repairs folder.
function RepairFields({ repair, onChange }: { repair?: Repair; onChange: (r: Repair | undefined) => void }) {
  const [units, setUnits] = useState<string[]>();
  const [scripts, setScripts] = useState<RepairsResponse>();
  const action = repair?.action ?? '';
  useEffect(() => {
    if (action === 'restart' && !units)
      api.units().then(
        (r) => setUnits(r.units.map((u) => u.name)),
        () => setUnits([]),
      );
    if (action === 'script' && !scripts) api.repairs().then(setScripts, () => setScripts({ repairs: [], dir: '', scripts: [] }));
  }, [action, units, scripts]);
  const pick = (a: '' | Repair['action']) =>
    onChange(a ? { action: a, unit: repair?.unit ?? '', container: repair?.container ?? '', script: repair?.script ?? '', max: repair?.max ?? 2, wait: repair?.wait ?? 120 } : undefined);
  const set = (patch: Partial<Repair>) => repair && onChange({ ...repair, ...patch });
  const runnable = scripts?.scripts.filter((x) => !x.problem) ?? [];
  return (
    <>
      <Row label="When it goes down" hint="Nightjar can repair it, then checks again at once">
        <Seg<'' | Repair['action']>
          label="Repair"
          value={action}
          onChange={pick}
          options={[
            { value: '', label: 'Only alert' },
            { value: 'restart', label: 'Restart a service' },
            { value: 'container', label: 'Restart a container' },
            { value: 'script', label: 'Run a script' },
          ]}
        />
      </Row>
      {repair?.action === 'restart' && (
        <Row label="Service" hint="The systemd service behind the check">
          {units && units.length > 0 ? (
            <Select
              label="Service to restart"
              value={repair.unit ?? ''}
              onChange={(unit) => set({ unit })}
              options={[{ value: '', label: 'Choose a service' }, ...units.map((u) => ({ value: u, label: u.replace(/\.service$/, '') }))]}
            />
          ) : (
            <input className="field" aria-label="Service to restart" placeholder="plexmediaserver.service" value={repair.unit ?? ''} onChange={(e) => set({ unit: e.target.value })} />
          )}
        </Row>
      )}
      {repair?.action === 'container' && (
        <Row label="Container" hint="The Docker container behind the check">
          <ContainerPick label="Container to restart" value={repair.container ?? ''} onChange={(container) => set({ container })} />
        </Row>
      )}
      {repair?.action === 'script' && (
        <Row label="Script" hint={scripts?.dir ? `From ${scripts.dir}` : 'From the repairs folder'}>
          {runnable.length > 0 ? (
            <Select
              label="Script to run"
              value={repair.script ?? ''}
              onChange={(script) => set({ script })}
              options={[{ value: '', label: 'Choose a script' }, ...runnable.map((x) => ({ value: x.name, label: x.name }))]}
            />
          ) : (
            <p className="form-help">
              {scripts ? 'No script there can run yet.' : 'Loading…'} Put an executable script in the repairs folder, owned by root and not writable by others.{' '}
              <Link to="/docs/uptime-checks#repairs">How repairs work →</Link>
            </p>
          )}
          {scripts?.scripts
            .filter((x) => x.problem)
            .map((x) => (
              <p key={x.name} className="form-help">
                {x.name} cannot run: {x.problem}
              </p>
            ))}
        </Row>
      )}
      {repair && (
        <Row label="Attempts" hint="Per outage; it alerts as usual either way">
          <span className="inline-fields">
            <label>
              up to <NumberInput label="Repair attempts" value={repair.max} onChange={(max) => set({ max })} min={1} max={5} />
            </label>
            <label>
              <NumberInput label="Seconds between attempts" value={repair.wait} onChange={(wait) => set({ wait })} unit="s" min={30} max={3600} /> apart
            </label>
          </span>
        </Row>
      )}
    </>
  );
}

// CheckForm adds a check, or edits the one in `initial` (which may rename it).
export function CheckForm({ initial, server, onSaved, onCancel }: { initial?: CheckSpec; server?: string; onSaved: (spec: CheckSpec) => void; onCancel: () => void }) {
  const [draft, setDraft] = useState<CheckSpec>(initial ?? { ...NEW_CHECK, server });
  const [statuses, setStatuses] = useState((initial?.expected_status ?? []).join(', '));
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  // The last test, and the check it ran: editing the check makes it stale.
  const [tested, setTested] = useState<{ result: CheckResult; key: string }>();
  const notify = useToast();
  const set = <K extends keyof CheckSpec>(key: K, value: CheckSpec[K]) => setDraft((d) => ({ ...d, [key]: value }));

  const spec = (): CheckSpec => ({ ...draft, expected_status: draft.type === 'http' ? parseStatuses(statuses) : null });
  const keyOf = (s: CheckSpec) => JSON.stringify([s.type, s.target, s.timeout, s.expected_status, s.keyword, s.verify_tls, s.tls_warn_days, s.ping_count, s.ping_interval]);
  const current = tested && tested.key === keyOf(spec()) ? tested.result : undefined;

  const runTest = async (s: CheckSpec): Promise<CheckResult | undefined> => {
    setTesting(true);
    setError('');
    try {
      const { result } = await api.testCheck({ ...s, name: s.name || 'test' });
      setTested({ result, key: keyOf(s) });
      return result;
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not test the check.');
    } finally {
      setTesting(false);
    }
  };

  // Saving tests the target first; one that does not answer needs a second click ("Save anyway").
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    const s = spec();
    // A heartbeat has nothing to reach; it waits for its job's pings.
    if (!s.paused && !current && s.type !== 'heartbeat') {
      const result = await runTest(s);
      if (!result?.up) return;
    }
    setSaving(true);
    setError('');
    try {
      const { check } = await api.saveCheck(s, initial?.name);
      notify({ tone: 'ok', title: initial ? 'Check saved' : 'Check added', body: check.paused ? `${check.name} is paused` : `${check.name} runs on the next cycle` });
      onSaved(check);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not save the check.');
    } finally {
      setSaving(false);
    }
  };

  const saveLabel = saving ? 'saving…' : testing ? 'testing…' : current && !current.up ? 'Save anyway' : initial ? 'Save check' : 'Add check';
  return (
    <form className="panel" onSubmit={submit} onKeyDown={(e) => e.key === 'Escape' && !e.defaultPrevented && onCancel()}>
      <div className="section-head">
        <span className="label">{initial ? `Edit ${initial.name}` : 'New check'}</span>
      </div>
      <Row label="Name">
        <input className="field" value={draft.name} onChange={(e) => set('name', e.target.value)} required autoFocus={!initial} aria-label="Name" />
      </Row>
      <Row label="Type">
        <Seg<CheckType> label="Check type" value={draft.type} onChange={(t) => set('type', t)} options={TYPES.map((t) => ({ value: t, label: t }))} />
      </Row>
      {draft.type === 'heartbeat' ? (
        <>
          <Row label="Schedule" hint='When the job runs: cron, @daily, or "every 6h"'>
            <input className="field" value={draft.schedule ?? ''} placeholder="0 3 * * *" onChange={(e) => set('schedule', e.target.value)} required aria-label="Schedule" />
            <span className="schedule-picks">
              {SCHEDULES.map((x) => (
                <button key={x.value} type="button" className={`chip-btn ${draft.schedule === x.value ? 'on' : ''}`} onClick={() => set('schedule', x.value)}>
                  {x.label}
                </button>
              ))}
            </span>
          </Row>
          <Row label="Grace" hint="How late a ping may be before it counts as missed">
            <NumberInput label="Grace in minutes" value={Math.round((draft.grace ?? 600) / 60)} onChange={(v) => set('grace', v * 60)} unit="min" min={1} />
          </Row>
          <Row label="May run for" hint="After a /start ping; 0 for no limit">
            <NumberInput label="Longest run in minutes" value={Math.round((draft.max_runtime ?? 0) / 60)} onChange={(v) => set('max_runtime', v * 60)} unit="min" min={0} />
          </Row>
          <Row label="Ping URL" hint="The job calls it when it finishes">
            {draft.target ? <code className="ping-url">{pingURL(draft.target)}</code> : <span className="form-help">Made when the check is added.</span>}
          </Row>
        </>
      ) : (
        <Row
          label="Target"
          hint={
            draft.type === 'http'
              ? 'URL'
              : draft.type === 'ping'
                ? 'Host name or IP'
                : draft.type === 'plugin'
                  ? 'The plugin that checks'
                  : draft.type === 'docker'
                    ? 'Up while running and not unhealthy'
                    : 'Host and port'
          }
        >
          {draft.type === 'plugin' ? (
            <PluginTarget target={draft.target} onChange={(t) => set('target', t)} />
          ) : draft.type === 'docker' ? (
            <ContainerPick label="Target" value={draft.target} onChange={(t) => set('target', t)} />
          ) : (
            <input className="field" value={draft.target} placeholder={TARGET_HINT[draft.type]} onChange={(e) => set('target', e.target.value)} required aria-label="Target" />
          )}
        </Row>
      )}
      {draft.type !== 'heartbeat' && (
        <>
          <Row label="Timing" hint="Down after this many failed retries">
            <span className="inline-fields">
              <label>
                every <NumberInput label="Interval" value={draft.interval} onChange={(v) => set('interval', v)} unit="s" min={5} />
              </label>
              <label>
                timeout <NumberInput label="Timeout" value={draft.timeout} onChange={(v) => set('timeout', v)} unit="s" min={1} />
              </label>
              <label>
                retries <NumberInput label="Retries" value={draft.retries} onChange={(v) => set('retries', v)} min={0} />
              </label>
            </span>
          </Row>
          <Row label="Retry" hint="How soon a failed check runs again before it counts as down">
            <label>
              after <NumberInput label="Retry interval" value={draft.retry_interval} onChange={(v) => set('retry_interval', v)} unit="s" min={5} />
            </label>
          </Row>
        </>
      )}
      {draft.type === 'ping' && (
        <Row label="Pings" hint="Up when any is answered, so one lost packet is not an outage">
          <span className="inline-fields">
            <label>
              send <NumberInput label="Pings per check" value={draft.ping_count} onChange={(v) => set('ping_count', v)} min={1} max={10} />
            </label>
            <label>
              every <NumberInput label="Seconds between pings" value={draft.ping_interval} onChange={(v) => set('ping_interval', v)} unit="s" min={0.2} max={5} step={0.1} />
            </label>
          </span>
        </Row>
      )}
      {draft.type === 'http' && (
        <>
          <Row label="Expected status" hint="Empty accepts any status below 400">
            <input className="field" value={statuses} placeholder="200, 204" onChange={(e) => setStatuses(e.target.value)} aria-label="Expected status codes" />
          </Row>
          <Row label="Keyword" hint="The response must contain it">
            <input className="field" value={draft.keyword} onChange={(e) => set('keyword', e.target.value)} aria-label="Keyword" />
          </Row>
          <Row label="Certificate">
            <label className="check">
              <input type="checkbox" checked={draft.verify_tls} onChange={(e) => set('verify_tls', e.target.checked)} /> Verify TLS
            </label>
          </Row>
        </>
      )}
      {draft.type === 'tls' && (
        <Row label="Warn before expiry">
          <NumberInput label="Days before expiry" value={draft.tls_warn_days} onChange={(v) => set('tls_warn_days', v)} unit="days" min={0} />
        </Row>
      )}
      <ServerField value={draft.server ?? ''} onChange={(v) => set('server', v || undefined)} />
      <RepairFields repair={draft.repair} onChange={(r) => set('repair', r)} />
      <Row label="Paused" hint="A paused check does not run or alert">
        <label className="check">
          <input type="checkbox" checked={draft.paused} onChange={(e) => set('paused', e.target.checked)} /> Paused
        </label>
      </Row>
      {current && <TestResult result={current} target={draft.target} />}
      <div className="form-actions">
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <button type="button" className="btn-quiet" onClick={onCancel}>
          Cancel
        </button>
        {draft.type !== 'heartbeat' && (
          <button type="button" className="btn-quiet" disabled={testing || !draft.target.trim()} onClick={() => runTest(spec())}>
            {testing ? 'Testing…' : 'Test'}
          </button>
        )}
        <button type="submit" className={`btn ${current && !current.up ? 'btn-warn' : ''}`} disabled={saving || testing}>
          {saveLabel}
        </button>
      </div>
    </form>
  );
}
