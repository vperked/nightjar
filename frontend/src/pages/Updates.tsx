import { useEffect, useMemo, useRef, useState, type FormEvent } from 'react';
import { Link, useSearchParams } from 'react-router';
import { ApiError } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import type { HostUpdatesSummary, UpdateSettings, UpdateTarget, UpdatesResponse } from '../api/types';
import { useHostUpdates, useUpdates } from '../api/UpdatesContext';
import { hostUpdates, localUpdates, useUpdatesOf, type UpdatesSource } from '../api/updatesSource';
import { Editable } from '../components/Editable';
import { Select } from '../components/Select';
import { Skeleton } from '../components/Skeleton';
import { ServerTime, TimePicker } from '../components/TimePicker';
import { useToast } from '../components/Toasts';
import { AppsTabs, Seg } from '../components/ui';
import { formatAgo, plural } from '../format';
import { useDialog } from '../hooks/useDialog';
import { useNow } from '../hooks/useNow';
import { KIND_LABEL, UpdateForm, specOf } from './UpdateForm';

type Filter = 'all' | 'available' | 'auto';

type Tone = 'good' | 'warn' | 'crit' | 'busy' | 'idle';

// verdict is a target's state in a word and a tone, so colour never carries it alone.
export function verdict(t: UpdateTarget): [Tone, string] {
  if (t.busy === 'updating') return ['busy', 'Updating…'];
  if (t.busy === 'checking') return ['busy', 'Checking…'];
  if (t.busy === 'queued') return ['busy', 'Queued'];
  if (t.paused) return ['idle', 'Paused'];
  if (t.runs?.[0] && !t.runs[0].ok && t.available) return ['crit', 'Update failed'];
  if (t.error) return ['crit', 'Check failed'];
  if (!t.checked) return ['idle', 'Not checked'];
  if (t.available) return ['warn', 'Update available'];
  return ['good', 'Up to date'];
}

// version drops a release tag's leading "v", so "0.107.79 → v0.107.80" reads as one kind of number.
// Git describes (v2.3.0-2-g1c9e2aa) keep theirs.
const version = (t: UpdateTarget, v?: string) => (t.kind === 'git' || !v ? (v ?? '') : v.replace(/^v(?=\d)/, ''));

// change says what an update brings, in the units of its kind.
function change(t: UpdateTarget): string {
  const pending = t.pending?.length ?? 0;
  if (t.kind === 'system') return t.available ? plural(pending, 'package') : '';
  if (t.kind === 'snap') return t.available ? plural(pending, 'snap') : '';
  if (t.available && t.installed && t.latest) return `${version(t, t.installed)} → ${version(t, t.latest)}`;
  return version(t, t.installed);
}

// origin says where a target's updates come from.
function origin(t: UpdateTarget): string {
  switch (t.kind) {
    case 'apt':
      return `apt · ${t.package}`;
    case 'git':
      return `git · ${t.dir}`;
    case 'release':
      return `GitHub · ${t.repo}`;
    case 'arr':
      return 'built-in updater';
  }
  return KIND_LABEL[t.kind];
}

function Switch({ on, label, title = label, disabled, onChange }: { on: boolean; label: string; title?: string; disabled?: boolean; onChange: (on: boolean) => void }) {
  return (
    <label className={`switch ${disabled ? 'disabled' : ''}`} title={title}>
      <input type="checkbox" role="switch" aria-label={label} checked={on} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="switch-track" aria-hidden="true" />
      <span className="switch-text">Auto</span>
    </label>
  );
}

// Each part of the page acts through source, on the machine the page shows, then refreshes it.
interface On {
  source: UpdatesSource;
  refresh: () => void;
}

function Details({ t, data, onEdit, now, source, refresh }: On & { t: UpdateTarget; data: UpdatesResponse; onEdit: () => void; now: number }) {
  const notify = useToast();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const act = async (what: () => Promise<unknown>, failed: string) => {
    try {
      await what();
      refresh();
    } catch (err) {
      notify({ tone: 'error', title: failed, body: (err as Error).message });
    }
  };
  const pendingTitle = t.kind === 'git' ? 'New commits' : t.kind === 'system' ? 'Packages to upgrade' : t.kind === 'snap' ? 'Snaps to refresh' : '';
  return (
    <div className="upd-details">
      {t.error && <p className="notice crit-notice">{t.error}</p>}
      <div className="upd-detail-grid">
        <dl className="kv">
          <dt>Checked</dt>
          <dd>{formatAgo(t.checked ?? null, now)}</dd>
          <dt>Source</dt>
          <dd title={origin(t)}>{origin(t)}</dd>
          {t.unit && (
            <>
              <dt>Service</dt>
              <dd className="mono">{t.unit}</dd>
            </>
          )}
          <dt>Updates</dt>
          <dd>{t.can_update ? (t.auto ? `automatically, ${data.window.label}` : 'from here') : 'check only'}</dd>
          {t.update_command && (
            <>
              <dt>Command</dt>
              <dd className="mono" title={t.update_command}>
                {t.update_command}
              </dd>
            </>
          )}
        </dl>
        <div className="min0">
          <div className="label">History</div>
          {t.runs?.length ? (
            <ul className="upd-runs">
              {t.runs.map((r) => (
                <li key={r.at}>
                  <span className={`run-mark ${r.ok ? 'ok' : 'failed'}`}>{r.ok ? 'Updated' : 'Failed'}</span>
                  <span className="num">{r.from && r.to && r.from !== r.to ? `${version(t, r.from)} → ${version(t, r.to)}` : version(t, r.to)}</span>
                  <span className="ink-3">
                    {r.auto ? 'auto' : 'manual'} · {formatAgo(r.at, now)}
                  </span>
                  {r.detail && <span className="run-detail">{r.detail}</span>}
                  {!!r.log?.length && (
                    <details className="upd-run-log">
                      <summary>Log</summary>
                      <LogLines lines={r.log} />
                    </details>
                  )}
                </li>
              ))}
            </ul>
          ) : (
            <p className="ink-3 small">No updates installed from here yet.</p>
          )}
        </div>
      </div>
      {pendingTitle && t.pending && t.pending.length > 0 && (
        <div>
          <div className="label">{pendingTitle}</div>
          <ul className="upd-pending mono">
            {t.pending.map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
        </div>
      )}
      <Editable>
        <div className="upd-actions">
          <button className="btn-quiet" disabled={!!t.busy} onClick={() => act(() => source.check(t.id), 'Could not check')}>
            Check now
          </button>
          <button className="btn-quiet" onClick={onEdit}>
            Edit
          </button>
          {confirmDelete ? (
            <span className="confirm">
              stop tracking {t.name}?{' '}
              <button className="btn-quiet danger" onClick={() => act(() => source.remove(t.id), 'Could not remove it')}>
                Remove
              </button>{' '}
              <button className="btn-quiet" onClick={() => setConfirmDelete(false)}>
                Keep
              </button>
            </span>
          ) : (
            <button className="btn-quiet" onClick={() => setConfirmDelete(true)}>
              Remove
            </button>
          )}
        </div>
      </Editable>
    </div>
  );
}

// LogLines shows an update's commands, their output and its steps, a command and a step each marked.
function LogLines({ lines, follow }: { lines: string[]; follow?: boolean }) {
  const box = useRef<HTMLPreElement>(null);
  // While it runs, the newest line stays in view, unless you scrolled up to read.
  useEffect(() => {
    const el = box.current;
    if (follow && el && el.scrollHeight - el.scrollTop - el.clientHeight < 60) el.scrollTop = el.scrollHeight;
  }, [lines, follow]);
  return (
    <pre className="upd-log" ref={box}>
      {lines.map((l, i) => (
        <span key={i} className={l.startsWith('$ ') ? 'log-cmd' : /^(Updating |Checking |Asking |Waiting |It |Done$|Failed: )/.test(l) ? 'log-step' : undefined}>
          {l}
          {'\n'}
        </span>
      ))}
    </pre>
  );
}

// majorOf is a version's first number; isMajor an update to a new one, which can change how an app
// works and so is worth a look before it goes in.
const majorOf = (v?: string) => v?.replace(/^v/, '').match(/^(\d+)\./)?.[1];
export const isMajor = (t: UpdateTarget) => t.kind !== 'git' && !!majorOf(t.installed) && !!majorOf(t.latest) && majorOf(t.installed) !== majorOf(t.latest);

// A Batch is one Update all: what it was asked to update, and when. It is kept in this browser, so
// the page still follows it after a reload; the updates themselves run on the server either way.
// Each machine keeps its own, so an Update all on a server and one here never mix.
// before is each target's newest run when the batch began, by the server's clock: a later run is
// this batch's. started is this browser's clock and only stands in for a batch saved without it.
interface Batch {
  started: number;
  ids: string[];
  before?: Record<string, number>;
}
// batchKey is where a machine's batch is kept; this machine's has the name it always had.
export const batchKey = (machine: string) => (machine === 'local' ? 'nightjar.updateAll' : `nightjar.updateAll.${machine}`);
const loadBatch = (key: string): Batch | null => {
  try {
    return JSON.parse(localStorage.getItem(key) ?? 'null');
  } catch {
    return null;
  }
};
const saveBatch = (key: string, b: Batch | null) => {
  try {
    if (b) localStorage.setItem(key, JSON.stringify(b));
    else localStorage.removeItem(key);
  } catch {
    // Only following it after a reload is lost.
  }
};

type ItemState = 'waiting' | 'running' | 'updated' | 'failed' | 'skipped';

// itemState is where one update of a batch is, from the target as the server reports it. A run
// after since (a server time: its newest run when the batch began) is the batch's.
export function itemState(t: UpdateTarget | undefined, since: number): ItemState {
  if (!t) return 'skipped';
  if (t.busy === 'updating') return 'running';
  if (t.busy === 'queued' || t.busy === 'checking') return 'waiting';
  const run = t.runs?.[0];
  if (run && run.at > since) return run.ok ? 'updated' : 'failed';
  return t.available ? 'waiting' : 'skipped';
}

const ITEM_WORD: Record<ItemState, string> = { waiting: 'Waiting', running: 'Updating…', updated: 'Updated', failed: 'Failed', skipped: 'Nothing to install' };
const ITEM_TONE: Record<ItemState, Tone> = { waiting: 'idle', running: 'busy', updated: 'good', failed: 'crit', skipped: 'idle' };

// UpdateAllSheet lists what Update all would install, each ticked, a major version marked, before
// anything starts.
function UpdateAllSheet({ ready, onClose, onStart }: { ready: UpdateTarget[]; onClose: () => void; onStart: (ids: string[]) => void }) {
  const [picked, setPicked] = useState(() => new Set(ready.map((t) => t.id)));
  const chosen = ready.filter((t) => picked.has(t.id));
  const majors = chosen.filter(isMajor).length;
  // Focus moves into the sheet and stays there until it closes, then goes back to Update all.
  const sheet = useRef<HTMLDivElement>(null);
  useDialog(sheet, true);
  useEffect(() => {
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && onClose();
    window.addEventListener('keydown', esc);
    return () => window.removeEventListener('keydown', esc);
  }, [onClose]);
  return (
    <div className="sheet-backdrop" onMouseDown={onClose}>
      <div ref={sheet} className="sheet upd-all-sheet" role="dialog" aria-modal="true" aria-label="Update all" onMouseDown={(e) => e.stopPropagation()}>
        <div className="sheet-head">
          <div className="min0">
            <h2>Update all</h2>
            <span className="page-sub">One at a time, in this order. You can leave the page; this one follows along.</span>
          </div>
          <button type="button" className="icon-btn" aria-label="Close" onClick={onClose}>
            ×
          </button>
        </div>
        <ul className="upd-all-list">
          {ready.map((t) => (
            <li key={t.id}>
              <label className="upd-all-item">
                <input
                  type="checkbox"
                  className="select-box"
                  checked={picked.has(t.id)}
                  onChange={(e) =>
                    setPicked((p) => {
                      const next = new Set(p);
                      if (e.target.checked) next.add(t.id);
                      else next.delete(t.id);
                      return next;
                    })
                  }
                />
                <span className="min0">
                  <strong>{t.name}</strong>
                  <span className="upd-all-change num">{change(t)}</span>
                </span>
                {isMajor(t) && <span className="upd-major">Major version</span>}
                {t.self && <span className="small ink-3">restarts the monitor, so goes last</span>}
              </label>
            </li>
          ))}
        </ul>
        {majors > 0 && <p className="form-help">A major version can change how an app works or what it needs. Its release notes say what; untick it to leave it for later.</p>}
        <div className="sheet-actions">
          <button type="button" className="btn-quiet" onClick={onClose}>
            Cancel
          </button>
          <button type="button" className="btn" disabled={chosen.length === 0} onClick={() => onStart(chosen.map((t) => t.id))}>
            Update {plural(chosen.length, 'item')}
          </button>
        </div>
      </div>
    </div>
  );
}

// UpdateAllRun follows an Update all: how far it has got, the one running with its log as it goes,
// those still waiting, and how each went, with its log.
function UpdateAllRun({ batch, targets, onDismiss }: { batch: Batch; targets: UpdateTarget[]; onDismiss: () => void }) {
  const items = batch.ids.map((id) => {
    const t = targets.find((x) => x.id === id);
    return { id, t, state: itemState(t, batch.before?.[id] ?? batch.started - 5) };
  });
  const finished = items.filter((i) => i.state === 'updated' || i.state === 'failed' || i.state === 'skipped').length;
  const done = finished === items.length;
  // An item can sit waiting with nothing running (its update was refused, or the server forgot
  // it), so the batch can be put away whenever nothing in it is actually under way.
  const idle = !items.some((i) => i.state === 'running');
  const updated = items.filter((i) => i.state === 'updated').length;
  const failed = items.filter((i) => i.state === 'failed').length;
  const title = done ? `Update all finished · ${updated} updated${failed ? ` · ${failed} failed` : ''}` : `Updating all · ${finished} of ${items.length} done`;
  return (
    <section className={`section upd-batch ${done ? (failed ? 'bad' : 'good') : ''}`} aria-label="Update all">
      <div className="section-head">
        <span className="label">{title}</span>
        {idle && (
          <button type="button" className="btn-quiet" onClick={onDismiss}>
            Dismiss
          </button>
        )}
      </div>
      <div className="upd-batch-bar" role="progressbar" aria-valuemin={0} aria-valuemax={items.length} aria-valuenow={finished}>
        <span style={{ width: `${(100 * finished) / Math.max(1, items.length)}%` }} />
      </div>
      <ul className="upd-batch-list">
        {items.map(({ id, t, state }) => {
          const run = t?.runs?.[0];
          return (
            <li key={id} className={`upd-batch-item ${state}`}>
              <div className="upd-batch-row">
                <span className={`state ${ITEM_TONE[state]}`}>{ITEM_WORD[state]}</span>
                <strong className="truncate">{t?.name ?? id}</strong>
                {t && state !== 'updated' && state !== 'skipped' && <span className="small ink-3 num truncate">{change(t)}</span>}
                {state === 'updated' && run && <span className="small ink-3 num truncate">{run.to ? `now ${version(t!, run.to)}` : run.detail}</span>}
              </div>
              {state === 'running' && t?.live && <LogLines lines={t.live.lines} follow />}
              {state === 'failed' && run?.detail && <p className="upd-batch-why">{run.detail}</p>}
              {(state === 'updated' || state === 'failed') && !!run?.log?.length && (
                <details className="upd-run-log">
                  <summary>Log</summary>
                  <LogLines lines={run.log} />
                </details>
              )}
            </li>
          );
        })}
      </ul>
    </section>
  );
}

// LiveUpdate is the update under way: how long it has run, and what it is doing.
function LiveUpdate({ t }: { t: UpdateTarget }) {
  const now = useNow(1000);
  const live = t.live!;
  // How long it has run, on the machine's clock: what it said as it answered, and the time since this
  // page heard it. Without its clock (an older agent), the browser's has to do.
  // eslint-disable-next-line react-hooks/exhaustive-deps -- noted once per answer, as it arrives
  const heard = useMemo(() => Date.now() / 1000, [live.now]);
  const secs = Math.max(0, Math.round(live.now ? live.now - live.started + (now - heard) : now - live.started));
  return (
    <div className="upd-live" role="status" aria-live="polite">
      <div className="upd-live-head">
        <span className="state busy">Updating {t.name}</span>
        <span className="small ink-3 num">{secs >= 60 ? `${Math.floor(secs / 60)}m ${secs % 60}s` : `${secs}s`} so far</span>
      </div>
      {live.lines.length ? <LogLines lines={live.lines} follow /> : <p className="small ink-3">Starting…</p>}
    </div>
  );
}

function TargetRow({ t, data, now, onEdit, followed, source, refresh }: On & { t: UpdateTarget; data: UpdatesResponse; now: number; onEdit: () => void; followed?: boolean }) {
  const notify = useToast();
  const canEdit = useCanEdit();
  const [open, setOpen] = useState(false);
  const [tone, word] = verdict(t);

  const toggleAuto = async (auto: boolean) => {
    try {
      await source.save({ ...specOf(t), auto }, false);
      notify({ tone: 'ok', title: auto ? `${t.name} updates automatically` : `${t.name} waits for you`, body: auto ? `Installs ${data.window.label}.` : undefined });
      refresh();
    } catch (err) {
      notify({ tone: 'error', title: 'Could not change it', body: (err as Error).message });
    }
  };
  const update = async () => {
    try {
      await source.apply(t.id);
      notify({ tone: 'info', title: `Updating ${t.name}`, body: t.self ? 'The monitor restarts when it is done.' : 'You can leave this page.' });
      refresh();
    } catch (err) {
      notify({ tone: 'error', title: 'Could not start the update', body: err instanceof ApiError ? err.message : String(err) });
    }
  };

  return (
    <li className={`upd ${open ? 'open' : ''}`}>
      <div className="upd-row">
        <button className="upd-toggle" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
          <span className={`state ${tone}`}>{word}</span>
          <span className="upd-name min0">
            <strong className="truncate">{t.name}</strong>
            <span className="svc-target truncate">{origin(t)}</span>
          </span>
          {change(t) && <span className="upd-change num truncate">{change(t)}</span>}
          <svg className="chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
            <path d="m6 9 6 6 6-6" />
          </svg>
        </button>
        <div className="upd-controls">
          {/* A viewer still sees whether it updates on its own. */}
          <Switch on={t.auto} label={`Update ${t.name} automatically`} title={canEdit ? undefined : "Viewers can't change this"} disabled={!t.can_update || !canEdit} onChange={toggleAuto} />
          {t.available && t.can_update && canEdit ? (
            <button className="btn small-btn" disabled={!!t.busy} onClick={update}>
              {t.busy === 'updating' ? 'Updating…' : 'Update'}
            </button>
          ) : (
            <span className="small-btn-space" />
          )}
        </div>
      </div>
      {/* During Update all, its panel shows the log. */}
      {t.busy === 'updating' && t.live && !followed && <LiveUpdate t={t} />}
      {open && <Details t={t} data={data} now={now} onEdit={onEdit} source={source} refresh={refresh} />}
    </li>
  );
}

// tooOld is a server's answer that its agent predates updates on agents (the dashboard updates it
// by itself, so this passes).
const tooOld = (err: unknown) => err instanceof Error && /too old/i.test(err.message);

const pad = (n: number) => String(n).padStart(2, '0');
// An agent server's window is in minutes after its midnight; the time fields speak "HH:MM".
const hhmm = (minutes: number) => `${pad(Math.floor(minutes / 60) % 24)}:${pad(minutes % 60)}`;
const toMinutes = (v: string) => {
  const [h, m] = v.split(':').map(Number);
  return (h || 0) * 60 + (m || 0);
};
const settingsOf = (d: UpdatesResponse): UpdateSettings => ({ enabled: d.enabled, interval: d.interval, window: { start: d.window.start, end: d.window.end }, notify: d.notify ?? true });

// HostSettings is an agent server's own update settings, which the Settings page (this machine's)
// does not cover: whether it checks and how often, when it may update by itself (on its clock),
// and whether its updates are announced.
function HostSettings({ source, data, refresh }: On & { data: UpdatesResponse }) {
  const canEdit = useCanEdit();
  const notify = useToast();
  const current = settingsOf(data);
  const currentKey = JSON.stringify(current);
  const [draft, setDraft] = useState(current);
  // from is the server's settings the draft began from: when a poll brings others (changed from
  // another browser), an untouched draft takes them, and one being edited is left alone.
  const [from, setFrom] = useState(currentKey);
  if (from !== currentKey) {
    setFrom(currentKey);
    if (JSON.stringify(draft) === from) setDraft(current);
  }
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const dirty = JSON.stringify(draft) !== currentKey;
  const set = (patch: Partial<UpdateSettings>) => setDraft((d) => ({ ...d, ...patch }));
  const readOnly = canEdit ? undefined : "Viewers can't change this";

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!(draft.interval >= 3600)) {
      setError('Check at least an hour apart.');
      return;
    }
    setSaving(true);
    setError('');
    try {
      await source.saveSettings!(draft);
      notify({ tone: 'ok', title: `Saved for ${source.name}` });
      refresh();
    } catch (err) {
      // An agent too old for updates is the likely refusal here, so it is said plainly.
      setError(tooOld(err) ? `${source.name}'s agent is too old for these settings. It updates itself shortly; try again then.` : err instanceof ApiError ? err.message : String(err));
    } finally {
      setSaving(false);
    }
  };

  const start = hhmm(draft.window.start);
  const end = hhmm(draft.window.end);
  return (
    <form className="panel upd-host-settings" onSubmit={submit} aria-label={`Update settings on ${source.name}`}>
      <div className="section-head">
        <span className="label">Settings on {source.name}</span>
      </div>
      <div className="upd-settings" title={readOnly}>
        <label className="check">
          <input type="checkbox" checked={draft.enabled} disabled={!canEdit} onChange={(e) => set({ enabled: e.target.checked })} /> Check for updates
        </label>
        <label className="upd-settings-field">
          every
          <input
            className="field short"
            type="number"
            min={1}
            max={168}
            aria-label="Hours between checks"
            disabled={!draft.enabled || !canEdit}
            value={Math.round((draft.interval / 3600) * 10) / 10}
            onChange={(e) => set({ interval: Math.round(Number(e.target.value) * 3600) })}
          />
          hours
        </label>
        <span className="upd-settings-field">
          auto-update from
          <TimePicker value={start} disabled={!draft.enabled || !canEdit} onChange={(v) => set({ window: { ...draft.window, start: toMinutes(v) } })} label="Auto-update from" />
          to
          <TimePicker value={end} disabled={!draft.enabled || !canEdit} onChange={(v) => set({ window: { ...draft.window, end: toMinutes(v) } })} label="Auto-update until" />
        </span>
        <label className="check">
          <input type="checkbox" checked={draft.notify} disabled={!canEdit} onChange={(e) => set({ notify: e.target.checked })} /> Notify me
        </label>
      </div>
      {/* The window is on the server's own clock, which may be neither this Nightjar's nor this browser's. */}
      <ServerTime from={start} to={end} tz={data.time_zone} />
      {canEdit && (dirty || error) && (
        <div className="form-actions">
          {error && (
            <p className="form-error" role="alert">
              {error}
            </p>
          )}
          {dirty && (
            <>
              <button type="button" className="btn-quiet" onClick={() => (setDraft(current), setError(''))}>
                Undo
              </button>
              <button type="submit" className="btn" disabled={saving}>
                {saving ? 'Saving…' : 'Save'}
              </button>
            </>
          )}
        </div>
      )}
    </form>
  );
}

// waiting is how many updates wait to be installed: the nav badge's count.
const waiting = (d: UpdatesResponse | undefined) => d?.targets.filter((t) => t.available && !t.paused).length ?? 0;
const withCount = (name: string, n: number) => (n > 0 ? `${name} · ${n}` : name);

// MachinePicker chooses whose updates the page shows: this machine, or a server with an agent,
// each with how many updates wait there. The machine shown counts from what the page just loaded,
// the others from the main Nightjar's last look at them.
function MachinePicker({ value, hosts, shown, onPick }: { value: string; hosts: HostUpdatesSummary[]; shown?: UpdatesResponse; onPick: (v: string) => void }) {
  const { data: localData } = useUpdates();
  const count = (id: string, summary: number) => (id === value && shown ? waiting(shown) : summary);
  return (
    <Select
      label="Machine"
      className="short"
      value={value}
      onChange={onPick}
      options={[
        { value: 'local', label: withCount('This machine', waiting(localData)), text: 'This machine' },
        ...hosts.map((h) => ({ value: h.id, label: withCount(h.name, count(h.id, h.available)), text: h.name, hint: h.error || (h.applying ? `Updating ${h.applying}` : undefined) })),
      ]}
    />
  );
}

// UpdatesPage lists what is kept updated on this machine, or on a server through its agent
// (?host=<id>, so a link or a reload stays on it), and installs, checks and tunes it there.
export function UpdatesPage() {
  const [params, setParams] = useSearchParams();
  const picked = params.get('host') || 'local';
  const { hosts, loaded } = useHostUpdates();
  const hostName = hosts.find((h) => h.id === picked)?.name;
  // One source per machine, kept while its name stays, since its load is what the page polls.
  const source = useMemo(() => (picked === 'local' ? localUpdates : hostName !== undefined ? hostUpdates({ id: picked, name: hostName }) : undefined), [picked, hostName]);
  const pick = (v: string) => setParams(v === 'local' ? {} : { host: v }, { replace: true });
  if (source) return <UpdatesView key={source.key} source={source} hosts={hosts} onPick={pick} />;
  return (
    <main>
      <div className="page-title">
        <div>
          <h1>Updates</h1>
          <AppsTabs current="/updates" />
        </div>
        {hosts.length > 0 && (
          <div className="toolbar">
            <MachinePicker value={picked} hosts={hosts} onPick={pick} />
          </div>
        )}
      </div>
      {loaded ? (
        <p className="note">
          No server with an agent by that name. Pick one above, or see <Link to="/updates">this machine's updates</Link>.
        </p>
      ) : (
        <Skeleton kind="list" rows={8} />
      )}
    </main>
  );
}

function UpdatesView({ source, hosts, onPick }: { source: UpdatesSource; hosts: HostUpdatesSummary[]; onPick: (v: string) => void }) {
  const { data, loading, error, refresh: reload } = useUpdatesOf(source);
  const { refresh: reloadHosts } = useHostUpdates();
  // A change on a server also moves its count in the picker and the nav badge.
  const refresh = () => {
    reload();
    if (source.host) reloadHosts();
  };
  const remote = !!source.host;
  // When the server's own count moves (an update finished), the badge's summary is asked again.
  const count = data ? waiting(data) : undefined;
  useEffect(() => {
    if (remote && count !== undefined) reloadHosts();
  }, [remote, count, reloadHosts]);
  const notify = useToast();
  const now = useNow(10000);
  const [filter, setFilter] = useState<Filter>('all');
  const [editing, setEditing] = useState<UpdateTarget | 'new'>();
  const list = useMemo(() => data?.targets ?? [], [data]);
  const counts = { all: list.length, available: list.filter((t) => t.available).length, auto: list.filter((t) => t.auto).length };
  const shown = list.filter((t) => filter === 'all' || (filter === 'available' ? t.available : t.auto));

  // Everything with an update that can install from here; the monitor itself goes last, since it restarts.
  const ready = list.filter((t) => t.available && t.can_update && !t.busy && !t.paused).sort((a, b) => Number(!!a.self) - Number(!!b.self));
  const [choosing, setChoosing] = useState(false);
  const key = batchKey(source.key);
  const [batch, setBatch] = useState<Batch | null>(() => loadBatch(key));
  const updateAll = async (ids: string[]) => {
    setChoosing(false);
    const before = Object.fromEntries(ids.map((id) => [id, list.find((t) => t.id === id)?.runs?.[0]?.at ?? 0]));
    const b = { started: Date.now() / 1000, ids, before };
    setBatch(b);
    saveBatch(key, b);
    const failed: { id: string; why: string }[] = [];
    for (const id of ids) {
      try {
        await source.apply(id);
      } catch (err) {
        failed.push({ id, why: err instanceof ApiError ? err.message : String(err) });
      }
    }
    if (failed.length) {
      // One that never started would wait in the batch for ever, so it leaves it, and is named.
      const left = ids.filter((id) => !failed.some((f) => f.id === id));
      const next = left.length ? { ...b, ids: left } : null;
      // Unless it was dismissed or replaced meanwhile (saving twice is harmless).
      setBatch((cur) => {
        if (cur !== b) return cur;
        saveBatch(key, next);
        return next;
      });
      const names = failed.map((f) => list.find((t) => t.id === f.id)?.name ?? f.id);
      notify({
        tone: 'error',
        title: failed.length === 1 ? `Could not start updating ${names[0]}` : `${failed.length} updates could not start`,
        body: failed.length === 1 ? failed[0].why : failed.map((f, i) => `${names[i]}: ${f.why}`).join('; '),
      });
    }
    refresh();
  };
  const dismissBatch = () => {
    setBatch(null);
    saveBatch(key, null);
  };

  const checkAll = async () => {
    try {
      await source.check();
      refresh();
    } catch (err) {
      notify({ tone: 'error', title: 'Could not start the check', body: (err as Error).message });
    }
  };
  const detect = async () => {
    try {
      const { added } = await source.detect();
      notify({ tone: 'ok', title: added?.length ? `Found ${plural(added.length, 'new target')}` : 'Nothing new found', body: added?.map((a) => a.name).join(', ') });
      refresh();
    } catch (err) {
      notify({ tone: 'error', title: 'Could not look for apps', body: (err as Error).message });
    }
  };

  const sub = data ? [data.checking ? 'checking now…' : `checked ${formatAgo(data.last_check || null, now)}`, `auto-updates ${data.window.label}`].join(' · ') : '';
  // A server that cannot be asked (offline, or an agent too old for updates) says why, in place of
  // its list; one that answered before keeps its last list under the reason.
  const unreachable = remote && error && (
    <p className="notice crit-notice" role="alert">
      {tooOld(error) ? (
        <>
          <strong>{source.name}'s agent is too old for updates.</strong> It updates itself shortly; if it does not, reinstall it from{' '}
          <Link to={`/servers/${encodeURIComponent(source.host!)}`}>its server page</Link>.
        </>
      ) : (
        `Could not reach ${source.name}: ${error.message}`
      )}
    </p>
  );
  return (
    <main>
      <div className="page-title">
        <div>
          <h1>Updates</h1>
          <div className="page-sub">{sub}</div>
          <AppsTabs current="/updates" />
        </div>
        <div className="toolbar">
          {/* Only when there is another machine to pick. */}
          {hosts.length > 0 && <MachinePicker value={source.key} hosts={hosts} shown={data} onPick={onPick} />}
          {data && (
            <Seg<Filter>
              label="Filter"
              value={filter}
              onChange={setFilter}
              options={(['all', 'available', 'auto'] as Filter[]).map((f) => ({
                value: f,
                label: (
                  <>
                    {f[0].toUpperCase() + f.slice(1)}
                    <span className="n">{counts[f]}</span>
                  </>
                ),
              }))}
            />
          )}
          {(data || !remote) && (
            <Editable>
              <button className="btn-quiet" onClick={detect}>
                Detect apps
              </button>
              <button className="btn-quiet" onClick={() => setEditing('new')}>
                Add
              </button>
              {ready.length > 1 && (
                <button className="btn-quiet" onClick={() => setChoosing(true)}>
                  Update all ({ready.length})
                </button>
              )}
              <button className="btn" onClick={checkAll} disabled={!data || data.checking}>
                {data?.checking ? 'Checking…' : 'Check now'}
              </button>
            </Editable>
          )}
        </div>
      </div>
      {unreachable}
      {choosing && <UpdateAllSheet ready={ready} onClose={() => setChoosing(false)} onStart={updateAll} />}
      {batch && data && <UpdateAllRun batch={batch} targets={list} onDismiss={dismissBatch} />}
      {data &&
        !data.enabled &&
        (remote ? (
          <p className="notice">
            Scheduled checks are off on {source.name}, so nothing there updates automatically. Turn on <strong>Check for updates</strong> under Settings on {source.name}, below. Check now and Update
            still work.
          </p>
        ) : (
          <p className="notice">
            Scheduled checks are off, so nothing updates automatically. Turn on <strong>Check for updates</strong> in <Link to="/settings#updates">Settings → Updates</Link>. Check now and Update still
            work.
          </p>
        ))}
      {editing && data && (
        <UpdateForm
          key={editing === 'new' ? 'new' : editing.id}
          initial={editing === 'new' ? undefined : editing}
          apiKeySet={editing !== 'new' && editing.api_key_set}
          commandsEditable={data.commands_editable}
          save={source.save}
          onCancel={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            refresh();
          }}
        />
      )}
      {!data && (remote ? !error : loading) ? (
        <Skeleton kind="list" rows={8} />
      ) : !data && remote ? null : list.length === 0 ? (
        <div className="empty">
          <strong>Nothing to keep updated yet</strong>
          {remote
            ? `Detect finds OS packages, snaps and apps such as Sonarr, Radarr, Plex and AdGuard Home on ${source.name}.`
            : 'Detect finds OS packages, snaps, this monitor and apps such as Sonarr, Radarr, Plex and AdGuard Home.'}
          <Editable>
            <div>
              <button className="btn" onClick={detect}>
                Detect apps
              </button>
            </div>
          </Editable>
        </div>
      ) : shown.length === 0 ? (
        <p className="note">{filter === 'available' ? 'Everything is up to date.' : 'Nothing updates automatically yet. Turn on auto for a row.'}</p>
      ) : (
        <ul className="upd-list">
          {shown.map((t) => (
            <TargetRow key={t.id} t={t} data={data!} now={now} onEdit={() => setEditing(t)} followed={!!batch?.ids.includes(t.id)} source={source} refresh={refresh} />
          ))}
        </ul>
      )}
      {remote && data && source.saveSettings && <HostSettings source={source} data={data} refresh={refresh} />}
    </main>
  );
}
