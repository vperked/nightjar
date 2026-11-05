import { useEffect, useState } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import type { PlexState, PlexStream } from '../api/types';
import { formatClock, formatKbps, plural } from '../format';
import { usePoll } from '../hooks/usePoll';
import { Section, State } from './ui';

// What is playing on Plex: shared by the overview and the Plex page, so the overview does not
// load the whole Plex page.

export const plexState = (s: PlexState['status']) => (s === 'pending' ? 'unknown' : s);

// Decision names how the server is delivering a stream; transcodes are the ones that cost CPU.
export function Decision({ stream }: { stream: Pick<PlexStream, 'decision' | 'hw_transcode'> }) {
  return (
    <span className={`decision ${stream.decision === 'transcode' ? 'transcode' : ''}`}>
      {stream.decision}
      {stream.decision === 'transcode' && stream.hw_transcode ? ' (hw)' : ''}
    </span>
  );
}

function Progress({ stream }: { stream: PlexStream }) {
  return (
    <div className="playback">
      <div className="progress" role="progressbar" aria-label={`${stream.title} progress`} aria-valuenow={Math.round(stream.progress)} aria-valuemin={0} aria-valuemax={100}>
        <span style={{ width: `${Math.max(0, Math.min(100, stream.progress))}%` }} />
      </div>
      <div className="playback-line num">
        <span>{stream.state !== 'playing' ? stream.state : ''}</span>
        <span>
          {formatClock(stream.offset_ms)}
          {stream.duration_ms > 0 && <span className="ink-3"> / {formatClock(stream.duration_ms)}</span>}
        </span>
      </div>
    </div>
  );
}

// With onManage, each row opens that stream's details and controls.
export function NowPlaying({ streams, compact = false, onManage }: { streams: PlexStream[]; compact?: boolean; onManage?: (s: PlexStream) => void }) {
  if (streams.length === 0)
    return (
      <p className="note" style={{ padding: '4px 0 0' }}>
        Nothing playing.
      </p>
    );
  return (
    <table className="table np-table">
      <thead>
        <tr>
          <th>User</th>
          <th>Title</th>
          <th>Stream</th>
          {!compact && <th className="hide-sm">Progress</th>}
          <th className="r">Bandwidth</th>
          {onManage && (
            <th className="shrink hide-sm">
              <span className="sr-only">Manage</span>
            </th>
          )}
        </tr>
      </thead>
      <tbody>
        {streams.map((s) => (
          <tr key={s.id} className={onManage ? 'link' : undefined} onClick={onManage && (() => onManage(s))}>
            <td className="shrink np-user">
              <div>{s.user}</div>
              {!compact && (
                <div className="sub truncate" title={`${s.player} · ${s.product}`}>
                  {s.player || s.product}
                </div>
              )}
            </td>
            <td className="fill np-title">
              <div className="truncate" title={s.title}>
                {s.title}
              </div>
              <div className="sub truncate">{s.subtitle}</div>
            </td>
            <td className="shrink np-stream">
              <Decision stream={s} />
              {!compact && <div className="sub">{[s.quality, s.throttled ? 'throttled' : s.speed ? `${s.speed.toFixed(1)}×` : ''].filter(Boolean).join(' · ')}</div>}
              {!compact && s.reason && (
                <span className="stream-reason" title={s.fix}>
                  {s.reason}
                </span>
              )}
            </td>
            {!compact && (
              <td className="hide-sm" style={{ width: '22%' }}>
                <Progress stream={s} />
              </td>
            )}
            <td className="r shrink np-bw">
              <div className="num">{formatKbps(s.bandwidth_kbps)}</div>
              {!compact && <div className="sub">{s.local ? 'local' : 'remote'}</div>}
            </td>
            {onManage && (
              <td className="shrink r hide-sm">
                <button
                  type="button"
                  className="btn-ghost np-manage"
                  aria-label={`Manage ${s.user}'s stream of ${s.title}`}
                  onClick={(e) => {
                    e.stopPropagation();
                    onManage(s);
                  }}
                >
                  Manage
                </button>
              </td>
            )}
          </tr>
        ))}
      </tbody>
    </table>
  );
}

const fetchSummary = () => api.plex('summary');

// PlexSummary is the overview's Plex block: what's playing right now.
export function PlexSummary() {
  // With Plex monitoring off there is nothing to poll for; turning it on restarts the monitor and reloads the page.
  const [on, setOn] = useState(true);
  const plex = usePoll(fetchSummary, 10000, on);
  const off = plex.data?.enabled === false;
  useEffect(() => {
    if (off) setOn(false);
  }, [off]);
  const p = plex.data?.plex;
  if (!plex.data?.enabled || !p) return null;
  return (
    <Section
      label="Plex"
      aside={
        <span className="small ink-3">
          {p.status === 'up' ? (
            <>
              <span className="num">{plural(p.streams.length, 'stream')}</span> · <span className="num">{formatKbps(p.bandwidth_kbps)}</span>
            </>
          ) : (
            <State status={plexState(p.status)} />
          )}{' '}
          · <Link to="/plex">details →</Link>
        </span>
      }
    >
      {p.status === 'down' ? (
        <p className="note" style={{ padding: '4px 0 0' }}>
          Unreachable: {p.detail}
        </p>
      ) : (
        <NowPlaying streams={p.streams.slice(0, 5)} compact />
      )}
    </Section>
  );
}
