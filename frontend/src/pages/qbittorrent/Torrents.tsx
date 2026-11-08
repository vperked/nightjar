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

const ICON: Record<string, string> = {
  start: 'M8 5.5v13l10.5-6.5z',
  stop: 'M8 6h3v12H8zM13 6h3v12h-3z',
  reannounce: 'M12 12m-2 0a2 2 0 1 0 4 0a2 2 0 1 0-4 0M7.8 7.8a6 6 0 0 0 0 8.4M16.2 7.8a6 6 0 0 1 0 8.4M5 5a10 10 0 0 0 0 14M19 5a10 10 0 0 1 0 14',
  recheck: 'M20 11a8 8 0 0 0-14.6-4.5M4 4v4h4M4 13a8 8 0 0 0 14.6 4.5M20 20v-4h-4',
  delete: 'M5 7h14M10 7V5h4v2M7 7l1 12h8l1-12',
};

const ActionIcon = ({ name }: { name: string }) => (
  <svg className="qb-btn-icon" viewBox="0 0 24 24" aria-hidden="true">
    <path d={ICON[name]} />
  </svg>
);

// Actions are the buttons for one torrent or a selection. Deleting asks first, and whether to keep the files.
// In a torrent's details they are full buttons with icons; the selection bar keeps them plain.
export function Actions({ torrents, run, busy, onDone, panel = false }: { torrents: QBTorrent[]; run: ReturnType<typeof useActions>['run']; busy: boolean; onDone?: () => void; panel?: boolean }) {
  const [confirming, setConfirming] = useState(false);
  const stopped = torrents.every((t) => group(t.state) === 'paused');
  const act = async (action: QBAction, files = false) => {
    if (await run(action, torrents, files)) {
      setConfirming(false);
      onDone?.();
    }
  };
  const cls = panel ? 'qb-btn' : 'btn-quiet';
  const button = (action: QBAction | 'ask', label: string, danger = false) => (
    <button type="button" className={`${cls} ${danger ? 'danger' : ''}`} disabled={busy && action !== 'ask'} onClick={() => (action === 'ask' ? setConfirming(true) : act(action))}>
      {panel && <ActionIcon name={action === 'ask' ? 'delete' : action} />}
      {label}
    </button>
  );
  if (confirming) {
    return (
      <span className={`confirm qb-confirm ${panel ? 'in-panel' : ''}`}>
        <span className="qb-confirm-text">Delete {torrents.length === 1 ? 'this torrent' : plural(torrents.length, 'torrent')}?</span>
        <button type="button" className={cls} onClick={() => setConfirming(false)}>
          Cancel
        </button>
        <button type="button" className={cls} disabled={busy} onClick={() => act('delete')}>
          Keep files
        </button>
        <button type="button" className={`${cls} danger ${panel ? 'solid' : ''}`} disabled={busy} onClick={() => act('delete', true)}>
          Delete files too
        </button>
      </span>
    );
  }
  return (
    <span className={`qb-actions ${panel ? 'in-panel' : ''}`}>
      {stopped ? (
        button('start', 'Start')
      ) : (
        <>
          {button('stop', 'Stop')}
          {torrents.some((t) => group(t.state) === 'paused') && button('start', 'Start')}
        </>
      )}
      {button('reannounce', 'Reannounce')}
      {button('recheck', 'Recheck')}
      {panel && <span className="qb-actions-gap" />}
      {button('ask', 'Delete…', true)}
    </span>
  );
}

// CopyValue is a long value (a path, a hash) with a button that copies it.
function CopyValue({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(value);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {
      /* clipboard unavailable */
    }
  };
  return (
    <div className="qb-line">
      <span className="qb-line-label">{label}</span>
      <span className="qb-line-value mono truncate" title={value}>
        {value}
      </span>
      <button type="button" className="qb-copy" onClick={copy} aria-label={`Copy ${label.toLowerCase()}`}>
        {copied ? 'Copied' : 'Copy'}
      </button>
    </div>
  );
}

export function TorrentRow({
  t,
  selected,
  onSelect,
  open,
  onToggle,
  actions,
  arrived = false,
}: {
  t: QBTorrent;
  selected: boolean;
  onSelect: (on: boolean) => void;
  open: boolean;
  onToggle: () => void;
  actions: ReturnType<typeof useActions>;
  arrived?: boolean;
}) {
  const now = useNow(30000);
  const g = group(t.state);
  const pct = Math.round(t.progress * 1000) / 10;
  const done = t.progress >= 1;
  const sub = [t.category, formatBytes(t.size), !done && t.eta > 0 && t.eta < UNKNOWN_ETA ? `${formatDuration(t.eta)} left` : ''].filter(Boolean).join(' · ');
  return (
    <>
      <tr className={`qb-row ${selected ? 'selected' : ''} ${open ? 'open' : ''}${arrived ? ' arrive' : ''}`}>
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
          <td colSpan={6}>
            <div className="qb-panel">
              {t.stalled_for ? (
                <div className="qb-panel-stall">
                  Stalled for {formatDuration(t.stalled_for)}
                  {t.recoveries ? ` · the monitor has tried to recover it ${t.recoveries === 1 ? 'once' : `${t.recoveries} times`}` : ''}
                </div>
              ) : null}
              <dl className="qb-tiles">
                <div>
                  <dt>Added</dt>
                  <dd>{formatAgo(t.added_on || null, now)}</dd>
                </div>
                <div>
                  <dt>Completed</dt>
                  <dd>{t.completion_on > 0 ? formatAgo(t.completion_on, now) : <span className="ink-3">Not yet</span>}</dd>
                </div>
                <div>
                  <dt>Downloaded</dt>
                  <dd className="num">{formatBytes(t.downloaded)}</dd>
                </div>
                <div>
                  <dt>Uploaded</dt>
                  <dd className="num">{formatBytes(t.uploaded)}</dd>
                </div>
              </dl>
              <div className="qb-lines">
                <CopyValue label="Saved in" value={t.save_path} />
                <CopyValue label="Hash" value={t.hash} />
              </div>
              <Actions torrents={[t]} run={actions.run} busy={actions.busy} panel />
            </div>
          </td>
        </tr>
      )}
    </>
  );
}
