import { useMemo, useState } from 'react';
import { Link } from 'react-router';
import { ApiError, api } from '../api/client';
import type { UpdateTarget, UpdatesResponse } from '../api/types';
import { useUpdates } from '../api/UpdatesContext';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { AppsTabs, Seg } from '../components/ui';
import { formatAgo, plural } from '../format';
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

function source(t: UpdateTarget): string {
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

function Switch({ on, label, disabled, onChange }: { on: boolean; label: string; disabled?: boolean; onChange: (on: boolean) => void }) {
  return (
    <label className={`switch ${disabled ? 'disabled' : ''}`} title={label}>
      <input type="checkbox" role="switch" aria-label={label} checked={on} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="switch-track" aria-hidden="true" />
      <span className="switch-text">auto</span>
    </label>
  );
}

function Details({ t, data, onEdit, now }: { t: UpdateTarget; data: UpdatesResponse; onEdit: () => void; now: number }) {
  const { refresh } = useUpdates();
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
          <dd title={source(t)}>{source(t)}</dd>
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
      <div className="upd-actions">
        <button className="btn-quiet" disabled={!!t.busy} onClick={() => act(() => api.checkUpdates(t.id), 'Could not check')}>
          check now
        </button>
        <button className="btn-quiet" onClick={onEdit}>
          edit
        </button>
        {confirmDelete ? (
          <span className="confirm">
            stop tracking {t.name}?{' '}
            <button className="btn-quiet danger" onClick={() => act(() => api.deleteUpdate(t.id), 'Could not remove it')}>
              remove
            </button>{' '}
            <button className="btn-quiet" onClick={() => setConfirmDelete(false)}>
              keep
            </button>
          </span>
        ) : (
          <button className="btn-quiet" onClick={() => setConfirmDelete(true)}>
            remove
          </button>
        )}
      </div>
    </div>
  );
}

function TargetRow({ t, data, now, onEdit }: { t: UpdateTarget; data: UpdatesResponse; now: number; onEdit: () => void }) {
  const { refresh } = useUpdates();
  const notify = useToast();
  const [open, setOpen] = useState(false);
  const [tone, word] = verdict(t);

  const toggleAuto = async (auto: boolean) => {
    try {
      await api.saveUpdate({ ...specOf(t), auto }, false);
      notify({ tone: 'ok', title: auto ? `${t.name} updates automatically` : `${t.name} waits for you`, body: auto ? `Installs ${data.window.label}.` : undefined });
      refresh();
    } catch (err) {
      notify({ tone: 'error', title: 'Could not change it', body: (err as Error).message });
    }
  };
  const update = async () => {
    try {
      await api.applyUpdate(t.id);
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
            <span className="svc-target truncate">{source(t)}</span>
          </span>
          {change(t) && <span className="upd-change num truncate">{change(t)}</span>}
          <svg className="chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
            <path d="m6 9 6 6 6-6" />
          </svg>
        </button>
        <div className="upd-controls">
          <Switch on={t.auto} label={`Update ${t.name} automatically`} disabled={!t.can_update} onChange={toggleAuto} />
          {t.available && t.can_update ? (
            <button className="btn small-btn" disabled={!!t.busy} onClick={update}>
              {t.busy === 'updating' ? 'Updating…' : 'Update'}
            </button>
          ) : (
            <span className="small-btn-space" />
          )}
        </div>
      </div>
      {open && <Details t={t} data={data} now={now} onEdit={onEdit} />}
    </li>
  );
}

export function UpdatesPage() {
  const { data, loading, refresh } = useUpdates();
  const notify = useToast();
  const now = useNow(10000);
  const [filter, setFilter] = useState<Filter>('all');
  const [editing, setEditing] = useState<UpdateTarget | 'new'>();
  const list = useMemo(() => data?.targets ?? [], [data]);
  const counts = { all: list.length, available: list.filter((t) => t.available).length, auto: list.filter((t) => t.auto).length };
  const shown = list.filter((t) => filter === 'all' || (filter === 'available' ? t.available : t.auto));

  // Everything with an update that can install from here; the monitor itself goes last, since it restarts.
  const ready = list.filter((t) => t.available && t.can_update && !t.busy && !t.paused).sort((a, b) => Number(!!a.self) - Number(!!b.self));
  const [confirmAll, setConfirmAll] = useState(false);
  const updateAll = async () => {
    setConfirmAll(false);
    let failed = 0;
    for (const t of ready) {
      try {
        await api.applyUpdate(t.id);
      } catch {
        failed++;
      }
    }
    notify(failed ? { tone: 'error', title: `${failed} update(s) could not start` } : { tone: 'info', title: `Updating ${ready.length} items`, body: 'One at a time; you can leave this page.' });
    refresh();
  };

  const checkAll = async () => {
    try {
      await api.checkUpdates();
      refresh();
    } catch (err) {
      notify({ tone: 'error', title: 'Could not start the check', body: (err as Error).message });
    }
  };
  const detect = async () => {
    try {
      const { added } = await api.detectUpdates();
      notify({ tone: 'ok', title: added?.length ? `Found ${plural(added.length, 'new target')}` : 'Nothing new found', body: added?.map((a) => a.name).join(', ') });
      refresh();
    } catch (err) {
      notify({ tone: 'error', title: 'Could not look for apps', body: (err as Error).message });
    }
  };

  const sub = data ? [data.checking ? 'checking now…' : `checked ${formatAgo(data.last_check || null, now)}`, `auto-updates ${data.window.label}`].join(' · ') : '';
  return (
    <main>
      <div className="page-title">
        <div>
          <h1>Updates</h1>
          <div className="page-sub">{sub}</div>
          <AppsTabs current="/updates" />
        </div>
        <div className="toolbar">
          <Seg<Filter>
            label="Filter"
            value={filter}
            onChange={setFilter}
            options={(['all', 'available', 'auto'] as Filter[]).map((f) => ({
              value: f,
              label: (
                <>
                  {f}
                  <span className="n">{counts[f]}</span>
                </>
              ),
            }))}
          />
          <button className="btn-quiet" onClick={detect}>
            detect apps
          </button>
          <button className="btn-quiet" onClick={() => setEditing('new')}>
            add
          </button>
          {ready.length > 1 &&
            (confirmAll ? (
              <span className="confirm">
                update {ready.length}
                {ready.some((t) => t.self) ? ' (the monitor restarts last)' : ''}?{' '}
                <button className="btn-quiet" onClick={updateAll}>
                  yes, update all
                </button>
                <button className="btn-quiet" onClick={() => setConfirmAll(false)}>
                  cancel
                </button>
              </span>
            ) : (
              <button className="btn-quiet" onClick={() => setConfirmAll(true)}>
                update all ({ready.length})
              </button>
            ))}
          <button className="btn" onClick={checkAll} disabled={!data || data.checking}>
            {data?.checking ? 'Checking…' : 'Check now'}
          </button>
        </div>
      </div>
      {data && !data.enabled && (
        <p className="notice">
          Scheduled checks are off, so nothing updates automatically. Turn on <strong>Check for updates</strong> in <Link to="/settings#updates">Settings → Updates</Link>. Check now and Update still
          work.
        </p>
      )}
      {editing && data && (
        <UpdateForm
          key={editing === 'new' ? 'new' : editing.id}
          initial={editing === 'new' ? undefined : editing}
          apiKeySet={editing !== 'new' && editing.api_key_set}
          commandsEditable={data.commands_editable}
          onCancel={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            refresh();
          }}
        />
      )}
      {loading && !data ? (
        <Skeleton kind="list" rows={8} />
      ) : list.length === 0 ? (
        <div className="empty">
          <strong>Nothing to keep updated yet</strong>
          Detect finds OS packages, snaps, this monitor and apps such as Sonarr, Radarr, Plex and AdGuard Home.
          <div>
            <button className="btn" onClick={detect}>
              Detect apps
            </button>
          </div>
        </div>
      ) : shown.length === 0 ? (
        <p className="note">{filter === 'available' ? 'Everything is up to date.' : 'Nothing updates automatically yet. Turn on auto for a row.'}</p>
      ) : (
        <ul className="upd-list">
          {shown.map((t) => (
            <TargetRow key={t.id} t={t} data={data!} now={now} onEdit={() => setEditing(t)} />
          ))}
        </ul>
      )}
    </main>
  );
}
