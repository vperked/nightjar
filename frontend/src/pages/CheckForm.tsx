import { useState, type FormEvent, type ReactNode } from 'react';
import { ApiError, api } from '../api/client';
import type { CheckResult, CheckSpec, CheckType } from '../api/types';
import { useToast } from '../components/Toasts';
import { Seg } from '../components/ui';

const TYPES: CheckType[] = ['http', 'tcp', 'ping', 'tls'];

const TARGET_HINT: Record<CheckType, string> = {
  http: 'https://example.com/health',
  tcp: 'host:port',
  ping: 'host',
  tls: 'host:443',
};

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

function NumberInput({ value, onChange, unit, label, min }: { value: number; onChange: (v: number) => void; unit?: string; label: string; min?: number }) {
  return (
    <span className="with-unit">
      <input className="field short" type="number" aria-label={label} min={min} value={value} onChange={(e) => onChange(e.target.valueAsNumber)} />
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

// CheckForm adds a check, or edits the one in `initial` (which may rename it).
export function CheckForm({ initial, onSaved, onCancel }: { initial?: CheckSpec; onSaved: (spec: CheckSpec) => void; onCancel: () => void }) {
  const [draft, setDraft] = useState<CheckSpec>(initial ?? NEW_CHECK);
  const [statuses, setStatuses] = useState((initial?.expected_status ?? []).join(', '));
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  // The last test, and the check it ran: editing the check makes it stale.
  const [tested, setTested] = useState<{ result: CheckResult; key: string }>();
  const notify = useToast();
  const set = <K extends keyof CheckSpec>(key: K, value: CheckSpec[K]) => setDraft((d) => ({ ...d, [key]: value }));

  const spec = (): CheckSpec => ({ ...draft, expected_status: draft.type === 'http' ? parseStatuses(statuses) : null });
  const keyOf = (s: CheckSpec) => JSON.stringify([s.type, s.target, s.timeout, s.expected_status, s.keyword, s.verify_tls, s.tls_warn_days]);
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
    if (!s.paused && !current) {
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
      <Row label="Target" hint={draft.type === 'http' ? 'URL' : draft.type === 'ping' ? 'Host name or IP' : 'Host and port'}>
        <input className="field" value={draft.target} placeholder={TARGET_HINT[draft.type]} onChange={(e) => set('target', e.target.value)} required aria-label="Target" />
      </Row>
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
              <input type="checkbox" checked={draft.verify_tls} onChange={(e) => set('verify_tls', e.target.checked)} /> verify TLS
            </label>
          </Row>
        </>
      )}
      {draft.type === 'tls' && (
        <Row label="Warn before expiry">
          <NumberInput label="Days before expiry" value={draft.tls_warn_days} onChange={(v) => set('tls_warn_days', v)} unit="days" min={0} />
        </Row>
      )}
      <Row label="Paused" hint="A paused check does not run or alert">
        <label className="check">
          <input type="checkbox" checked={draft.paused} onChange={(e) => set('paused', e.target.checked)} /> paused
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
          cancel
        </button>
        <button type="button" className="btn-quiet" disabled={testing || !draft.target.trim()} onClick={() => runTest(spec())}>
          {testing ? 'testing…' : 'test'}
        </button>
        <button type="submit" className={`btn ${current && !current.up ? 'btn-warn' : ''}`} disabled={saving || testing}>
          {saveLabel}
        </button>
      </div>
    </form>
  );
}
