import { useState } from 'react';
import { ApiError, api } from '../../api/client';
import type { QBAction, QBTorrent } from '../../api/types';
import { useToast } from '../../components/Toasts';
import { formatAgo, formatBytes, formatDuration, formatRate, plural } from '../../format';
import { useNow } from '../../hooks/useNow';
import { TONE, UNKNOWN_ETA, WORD, group } from './states';

// The torrent list's rows, and the actions on one torrent or a selection.

const DONE: Record<QBAction, string> = { start: 'Started', stop: 'Stopped', reannounce: 'Reannounced', recheck: 'Rechecking', delete: 'Deleted' };

// useActions runs torrent actions and reports how they went.
export function useActions(refresh: () => void) {
  const notify = useToast();
  const [busy, setBusy] = useState(false);
  const run = async (action: QBAction, torrents: QBTorrent[], deleteFiles = false) => {
    setBusy(true);
    try {
      await api.qbAction(
        action,
        torrents.map((t) => t.hash),
        deleteFiles,
      );
      const what = torrents.length === 1 ? torrents[0].name : plural(torrents.length, 'torrent');
      notify({ tone: 'ok', title: `${DONE[action]} ${what}`, body: action === 'delete' ? (deleteFiles ? 'Its files were deleted too.' : 'Its files were kept.') : undefined });
      refresh();
      return true;
    } catch (err) {
      notify({ tone: 'error', title: `Could not ${action} ${torrents.length === 1 ? torrents[0].name : 'the torrents'}`, body: err instanceof ApiError ? err.message : String(err) });
      return false;
    } finally {
      setBusy(false);
    }
  };
  return { run, busy };
}

// Actions are the buttons for one torrent or a selection. Deleting asks first, and whether to keep the files.
export function Actions({ torrents, run, busy, onDone }: { torrents: QBTorrent[]; run: ReturnType<typeof useActions>['run']; busy: boolean; onDone?: () => void }) {
  const [confirming, setConfirming] = useState(false);
  const stopped = torrents.every((t) => group(t.state) === 'paused');
  const act = async (action: QBAction, files = false) => {
    if (await run(action, torrents, files)) {
      setConfirming(false);
      onDone?.();
    }
  };
  if (confirming) {
    return (
      <span className="confirm qb-confirm">
        Delete {torrents.length === 1 ? 'this torrent' : plural(torrents.length, 'torrent')}?{' '}
        <button className="btn-quiet" disabled={busy} onClick={() => act('delete')}>
          keep files
        </button>
        <button className="btn-quiet danger" disabled={busy} onClick={() => act('delete', true)}>
          delete files too
        </button>
        <button className="btn-quiet" onClick={() => setConfirming(false)}>
          cancel
        </button>
      </span>
    );
  }
  return (
    <span className="qb-actions">
      {stopped ? (
        <button className="btn-quiet" disabled={busy} onClick={() => act('start')}>
          start
        </button>
      ) : (
        <>
          <button className="btn-quiet" disabled={busy} onClick={() => act('stop')}>
            stop
          </button>
          {torrents.some((t) => group(t.state) === 'paused') && (
            <button className="btn-quiet" disabled={busy} onClick={() => act('start')}>
              start
            </button>
          )}
        </>
      )}
      <button className="btn-quiet" disabled={busy} onClick={() => act('reannounce')}>
        reannounce
      </button>
      <button className="btn-quiet" disabled={busy} onClick={() => act('recheck')}>
        recheck
      </button>
      <button className="btn-quiet danger" disabled={busy} onClick={() => setConfirming(true)}>
        delete…
      </button>
    </span>
  );
}

export function TorrentRow({
  t,
  selected,
  onSelect,
  open,
  onToggle,
  actions,
}: {
  t: QBTorrent;
  selected: boolean;
  onSelect: (on: boolean) => void;
  open: boolean;
  onToggle: () => void;
  actions: ReturnType<typeof useActions>;
}) {
  const now = useNow(30000);
  const g = group(t.state);
  const pct = Math.round(t.progress * 1000) / 10;
  const done = t.progress >= 1;
  const sub = [t.category, formatBytes(t.size), !done && t.eta > 0 && t.eta < UNKNOWN_ETA ? `${formatDuration(t.eta)} left` : ''].filter(Boolean).join(' · ');
  return (
    <>
      <tr className={`qb-row ${selected ? 'selected' : ''} ${open ? 'open' : ''}`}>
        <td className="shrink qb-check">
          <input type="checkbox" className="select-box" aria-label={`Select ${t.name}`} checked={selected} onChange={(e) => onSelect(e.target.checked)} />
        </td>
        <td className="fill">
          <button className="qb-name" aria-expanded={open} onClick={onToggle}>
            <span className="truncate" title={t.name}>
              {t.name}
            </span>
          </button>
          <div className="sub truncate">
            {sub}
            {t.stalled_for ? (
              <span className="qb-stall">
                {' '}
                · stalled {formatDuration(t.stalled_for)}
                {t.recoveries ? `, ${t.recoveries} recovery ${t.recoveries === 1 ? 'try' : 'tries'}` : ''}
              </span>
            ) : null}
          </div>
          {/* Phones: state and speed sit under the name, so the name gets the row's width. */}
          <div className="show-sm qb-phone-line">
            <span className={`state ${TONE[g]}`}>{WORD[t.state] ?? t.state}</span>
            <span className="num ink-2">{[t.dlspeed ? `↓ ${formatRate(t.dlspeed)}` : '', t.upspeed ? `↑ ${formatRate(t.upspeed)}` : '', done ? '' : `${pct}%`].filter(Boolean).join(' · ')}</span>
          </div>
        </td>
        <td className="shrink hide-sm">
          <span className={`state ${TONE[g]}`}>{WORD[t.state] ?? t.state}</span>
        </td>
        <td className="hide-sm qb-progress">
          <div className="progress" role="progressbar" aria-label={`${t.name} progress`} aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100}>
            <span className={done ? 'done' : ''} style={{ width: `${Math.min(100, pct)}%` }} />
          </div>
          <div className="sub num">{done ? 'done' : `${pct}%`}</div>
        </td>
        <td className="r num shrink hide-sm">
          <div>{t.dlspeed ? `↓ ${formatRate(t.dlspeed)}` : <span className="ink-3">—</span>}</div>
          <div className="sub">{t.upspeed ? `↑ ${formatRate(t.upspeed)}` : ''}</div>
        </td>
        <td className="r num shrink hide-sm">
          <div>{t.ratio.toFixed(2)}</div>
          <div className="sub">
            {t.num_seeds} seeds · {t.num_leechs} peers
          </div>
        </td>
      </tr>
      {open && (
        <tr className="qb-detail">
          <td />
          <td colSpan={5}>
            <dl className="qb-facts">
              <dt>Saved in</dt>
              <dd className="mono" title={t.save_path}>
                {t.save_path}
              </dd>
              <dt>Added</dt>
              <dd>{formatAgo(t.added_on || null, now)}</dd>
              <dt>Completed</dt>
              <dd>{t.completion_on > 0 ? formatAgo(t.completion_on, now) : 'not yet'}</dd>
              <dt>Transferred</dt>
              <dd className="num">
                ↓ {formatBytes(t.downloaded)} · ↑ {formatBytes(t.uploaded)}
              </dd>
              <dt>Hash</dt>
              <dd className="mono">{t.hash}</dd>
            </dl>
            <Actions torrents={[t]} run={actions.run} busy={actions.busy} />
          </td>
        </tr>
      )}
    </>
  );
}
