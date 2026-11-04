import { useState } from 'react';
import { api } from '../api/client';
import { useServices } from '../api/ServicesContext';
import type { MaintenanceWindow } from '../api/types';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { Seg } from '../components/ui';
import { formatDuration } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';
import { IncidentsTabs } from './Incidents';

// Planned downtime. While a window is on, the checks it covers do not run: no alerts, no repairs,
// and nothing against their uptime. A window over everything also holds back every other alert.

const DAYS = ['Sun', 'Mon', 'Tue', 'Wed', 'Thu', 'Fri', 'Sat'];

const stamp = (ts: number) => new Date(ts * 1000).toLocaleString(undefined, { weekday: 'short', day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' });

// local is a unix time as a datetime-local input's value, and back.
const local = (ts?: number) => {
  if (!ts) return '';
  const d = new Date(ts * 1000);
  return new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0, 16);
};
const unix = (v: string) => (v ? Math.floor(new Date(v).getTime() / 1000) : 0);

export function describe(w: MaintenanceWindow): string {
  if (w.days?.length) {
    const days = w.days.length === 7 ? 'Every day' : w.days.length === 5 && !w.days.includes(0) && !w.days.includes(6) ? 'Weekdays' : w.days.map((d) => DAYS[d]).join(', ');
    return `${days}, ${w.from}–${w.to}`;
  }
  return `${stamp(w.start ?? 0)} – ${stamp(w.end ?? 0)}`;
}

function blank(): MaintenanceWindow {
  const start = Math.ceil(Date.now() / 1000 / 3600) * 3600;
  return { id: '', title: '', checks: [], start, end: start + 3600, public: true, note: '' };
}

function WindowForm({ initial, onDone }: { initial: MaintenanceWindow; onDone: (saved: boolean) => void }) {
  const [w, setW] = useState(initial);
  const [error, setError] = useState('');
  const checks = useServices().data?.services.map((s) => s.name) ?? [];
  const weekly = !!w.days?.length || (w.days !== undefined && w.from !== undefined);
  const set = (patch: Partial<MaintenanceWindow>) => setW((x) => ({ ...x, ...patch }));
  const notify = useToast();
  const toggle = <T,>(list: T[], v: T) => (list.includes(v) ? list.filter((x) => x !== v) : [...list, v]);
  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    try {
      await api.saveMaintenance(w);
      notify({ tone: 'ok', title: initial.id ? 'Window saved' : 'Window added' });
      onDone(true);
    } catch (err) {
      setError((err as Error).message);
    }
  };
  return (
    <form className="panel maint-form" onSubmit={submit}>
      <div className="section-head">
        <span className="label">{initial.id ? `Edit ${initial.title}` : 'Plan maintenance'}</span>
      </div>
      <div className="form-row">
        <div className="form-label">Title</div>
        <div className="form-control">
          <input className="field" aria-label="Title" placeholder="Updating the NAS" value={w.title} onChange={(e) => set({ title: e.target.value })} required autoFocus />
        </div>
      </div>
      <div className="form-row">
        <div className="form-label">When</div>
        <div className="form-control">
          <Seg<'once' | 'weekly'>
            label="How often"
            value={weekly ? 'weekly' : 'once'}
            onChange={(v) =>
              set(
                v === 'weekly'
                  ? { days: [0], from: '02:00', to: '03:00', start: undefined, end: undefined }
                  : { ...blank(), id: w.id, title: w.title, checks: w.checks, public: w.public, note: w.note, days: undefined, from: undefined, to: undefined },
              )
            }
            options={[
              { value: 'once', label: 'Once' },
              { value: 'weekly', label: 'Every week' },
            ]}
          />
          {weekly ? (
            <div className="maint-when">
              <span className="schedule-picks" role="group" aria-label="Days">
                {DAYS.map((d, i) => (
                  <button
                    key={d}
                    type="button"
                    className={`chip-btn ${w.days?.includes(i) ? 'on' : ''}`}
                    aria-pressed={w.days?.includes(i)}
                    onClick={() => set({ days: toggle(w.days ?? [], i).sort() })}
                  >
                    {d}
                  </button>
                ))}
              </span>
              <label>
                from <input className="field short" type="time" aria-label="From" value={w.from ?? ''} onChange={(e) => set({ from: e.target.value })} />
              </label>
              <label>
                to <input className="field short" type="time" aria-label="To" value={w.to ?? ''} onChange={(e) => set({ to: e.target.value })} />
              </label>
            </div>
          ) : (
            <div className="maint-when">
              <label>
                from <input className="field" type="datetime-local" aria-label="Starts" value={local(w.start)} onChange={(e) => set({ start: unix(e.target.value) })} />
              </label>
              <label>
                to <input className="field" type="datetime-local" aria-label="Ends" value={local(w.end)} onChange={(e) => set({ end: unix(e.target.value) })} />
              </label>
              {w.start && w.end && w.end > w.start && <span className="form-help">{formatDuration(w.end - w.start)}</span>}
            </div>
          )}
        </div>
      </div>
      <div className="form-row">
        <div className="form-label">
          Covers
          <div className="form-help">{w.checks.length ? 'Only these checks rest; other alerts carry on' : 'Everything: no check runs and every alert is held back'}</div>
        </div>
        <div className="form-control">
          <span className="schedule-picks" role="group" aria-label="Checks">
            <button type="button" className={`chip-btn ${w.checks.length === 0 ? 'on' : ''}`} onClick={() => set({ checks: [] })}>
              Everything
            </button>
            {checks.map((c) => (
              <button key={c} type="button" className={`chip-btn ${w.checks.includes(c) ? 'on' : ''}`} aria-pressed={w.checks.includes(c)} onClick={() => set({ checks: toggle(w.checks, c) })}>
                {c}
              </button>
            ))}
          </span>
        </div>
      </div>
      <div className="form-row">
        <div className="form-label">Status page</div>
        <div className="form-control">
          <label className="check">
            <input type="checkbox" checked={w.public} onChange={(e) => set({ public: e.target.checked })} /> Show it to visitors
          </label>
          {w.public && (
            <input className="field" aria-label="Note for visitors" placeholder="What visitors should know (optional)" value={w.note ?? ''} onChange={(e) => set({ note: e.target.value })} />
          )}
        </div>
      </div>
      <div className="form-actions">
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <button type="button" className="btn-quiet" onClick={() => onDone(false)}>
          Cancel
        </button>
        <button type="submit" className="btn">
          {initial.id ? 'Save window' : 'Add window'}
        </button>
      </div>
    </form>
  );
}

export function MaintenancePage() {
  const { data, error, refresh } = usePoll(api.maintenance, 30000);
  const [editing, setEditing] = useState<MaintenanceWindow>();
  const [confirm, setConfirm] = useState('');
  const now = useNow(15000);
  const notify = useToast();
  const remove = async (w: MaintenanceWindow) => {
    try {
      await api.deleteMaintenance(w.id);
      notify({ tone: 'ok', title: `${w.title} removed` });
      refresh();
    } catch (e) {
      notify({ tone: 'error', title: 'Could not remove it', body: (e as Error).message });
    }
  };
  const next = new Map((data?.upcoming ?? []).map((o) => [o.window.id, o]));
  return (
    <main>
      <div className="page-title">
        <div className="min0">
          <h1>Maintenance</h1>
          <IncidentsTabs current="/incidents/maintenance" />
          <div className="page-sub">Planned downtime: no alerts, no repairs, and nothing against uptime.</div>
        </div>
        {!editing && (
          <button className="btn" onClick={() => setEditing(blank())}>
            Plan maintenance
          </button>
        )}
      </div>
      {editing && (
        <WindowForm
          initial={editing}
          onDone={(saved) => {
            setEditing(undefined);
            if (saved) refresh();
          }}
        />
      )}
      {error && !data && <p className="alert">Could not load the windows: {error.message}</p>}
      {!data && !error && <Skeleton kind="list" />}
      {data && data.windows.length === 0 && !editing && (
        <div className="empty">
          <strong>No maintenance planned</strong>
          Plan a window before updating or moving something, and Nightjar stays quiet while you work.
        </div>
      )}
      {data && data.windows.length > 0 && (
        <section className="section">
          <div className="section-head">
            <span className="label">Windows</span>
          </div>
          <ul className="maint-list">
            {data.windows.map((w) => {
              const o = next.get(w.id);
              const tag = o?.on ? 'On now' : o ? `In ${formatDuration(o.start - now)}` : 'Over';
              return (
                <li key={w.id} className={o?.on ? 'on' : o ? '' : 'over'}>
                  <span className={`inc-state ${o?.on ? 'open' : 'closed'}`}>{tag}</span>
                  <span className="min0">
                    <strong>{w.title}</strong>
                    <span className="maint-meta">
                      {describe(w)} · {w.checks.length ? w.checks.join(', ') : 'everything'}
                      {w.public ? ' · on the status page' : ''}
                    </span>
                  </span>
                  <span className="maint-actions">
                    <button className="btn-quiet" onClick={() => setEditing(w)}>
                      Edit
                    </button>
                    {confirm === w.id ? (
                      <button className="btn-quiet danger" onClick={() => remove(w)}>
                        Remove it
                      </button>
                    ) : (
                      <button className="btn-quiet" onClick={() => setConfirm(w.id)}>
                        Remove
                      </button>
                    )}
                  </span>
                </li>
              );
            })}
          </ul>
        </section>
      )}
    </main>
  );
}
