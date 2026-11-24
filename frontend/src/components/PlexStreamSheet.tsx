import { useEffect, useRef, useState } from 'react';
import { api, errorText } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import type { PlexPlay, PlexResponse, PlexStream } from '../api/types';
import { formatAgo, formatClock, formatDuration, formatKbps } from '../format';
import { useDialog } from '../hooks/useDialog';
import { useNow } from '../hooks/useNow';
import { useToast } from './Toasts';

// PlexStreamSheet is one stream up close: who is watching what, where, and how the server delivers
// it, with a way to stop it and tell the viewer why. Given a finished play instead, it shows how
// that stream was delivered as it was last seen.

const DEFAULT_MESSAGE = 'The server owner stopped this stream.';
const PRESETS = [DEFAULT_MESSAGE, 'Server maintenance, back soon.', 'Too many streams right now, try again later.', 'Please set quality to Original to avoid transcoding.'];
const CUSTOM = -1;
const MAX = 200;

const KIND: Record<string, string> = { movie: 'Movie', episode: 'Episode', track: 'Music', clip: 'Clip', photo: 'Photo', live: 'Live TV' };

function delivery(s: PlexStream): string {
  if (s.decision === 'direct play') return 'Direct play';
  if (s.decision === 'direct stream') return 'Direct stream';
  return `Transcode on the ${s.hw_transcode ? 'GPU' : 'CPU'}`;
}

function deliveryDetail(s: PlexStream): string {
  if (s.decision === 'direct play') return 'The file as it is';
  if (s.decision === 'direct stream') return 'Repackaged, not re-encoded';
  return [s.video_decision && `video ${s.video_decision}`, s.audio_decision && `audio ${s.audio_decision}`].filter(Boolean).join(' · ');
}

// hardware says what converted a transcode: one name when the same hardware decodes and encodes.
function hardware(s: PlexStream): { value: string; detail?: string } {
  const dec = s.hw_decode;
  const enc = s.hw_encode;
  if (!dec && !enc) return s.hw_transcode ? { value: 'GPU' } : { value: 'CPU', detail: 'no hardware acceleration' };
  if (dec === enc) return { value: 'GPU', detail: enc };
  return { value: enc ? 'GPU' : 'CPU and GPU', detail: `${dec ?? 'CPU'} decodes · ${enc ?? 'CPU'} encodes` };
}

const DECISION: Record<string, string> = { transcode: 'Converted', copy: 'Sent as is', burn: 'Burned in' };

// language is the language of a track as Plex names it: "English (TRUEHD 7.1)" → "English".
const language = (track?: string) => track?.split(' (')[0] ?? '';

const join = (...parts: (string | undefined)[]) => parts.filter(Boolean).join(' · ');

// Formats is the file next to what the player is sent: video, audio, container and subtitles.
function Formats({ s }: { s: PlexStream }) {
  const src = s.source;
  const out = s.output;
  const rows: { label: string; file: string; sent?: string; decision?: string }[] = [];
  if (src?.video) rows.push({ label: 'Video', file: join(src.video, src.resolution), sent: out && join(out.video, out.resolution), decision: s.video_decision });
  if (src?.audio) rows.push({ label: 'Audio', file: join(src.audio, language(s.audio_track)), sent: out?.audio, decision: s.audio_decision });
  if (src?.container) rows.push({ label: 'Container', file: src.container, sent: out?.container, decision: out && out.container !== src.container ? 'repackaged' : 'copy' });
  if (s.subtitle_track) rows.push({ label: 'Subtitles', file: s.subtitle_track, sent: out ? (DECISION[s.subtitle_decision ?? ''] ?? 'Sent as is') : undefined, decision: s.subtitle_decision });
  if (!rows.length) return null;
  return (
    <div className={`stream-formats ${out ? 'two' : ''}`} role="table" aria-label="Formats">
      <div className="stream-formats-head" role="row">
        <span role="columnheader">
          <span className="sr-only">Track</span>
        </span>
        <span role="columnheader">{out ? 'File' : 'File, played as it is'}</span>
        {out && <span role="columnheader">Sent to the player</span>}
      </div>
      {rows.map((r) => (
        <div key={r.label} className="stream-format" role="row">
          <span role="rowheader">{r.label}</span>
          <span role="cell">{r.file}</span>
          {out && (
            <span role="cell" className={r.decision === 'transcode' || r.decision === 'burn' ? 'changed' : r.decision === 'copy' ? 'same' : undefined}>
              {r.sent || '—'}
            </span>
          )}
        </div>
      ))}
    </div>
  );
}

// asStream reads a finished play as the stream it was, from how it was last seen when Nightjar kept that.
function asStream(p: PlexPlay): PlexStream {
  const base: PlexStream = p.last ?? {
    id: '',
    user: p.user,
    kind: p.kind ?? '',
    title: p.title,
    subtitle: p.subtitle,
    player: p.player,
    product: '',
    platform: '',
    state: 'stopped',
    local: p.local ?? true,
    decision: p.decision,
    hw_transcode: false,
    throttled: false,
    reason: p.reason,
    quality: '',
    bitrate_kbps: 0,
    bandwidth_kbps: 0,
    duration_ms: p.duration_ms ?? 0,
    offset_ms: ((p.duration_ms ?? 0) * p.progress) / 100,
    progress: p.progress,
    started_at: p.started,
  };
  return { ...base, started_at: p.started };
}

const CloseIcon = () => (
  <svg viewBox="0 0 24 24" aria-hidden="true">
    <path d="M7 7l10 10M17 7L7 17" />
  </svg>
);

function Fact({ label, value, detail, address }: { label: string; value: string; detail?: string; address?: boolean }) {
  return (
    <div className="stream-fact">
      <dt>{label}</dt>
      <dd>
        <span className="num">{value}</span>
        {detail && <span className={`stream-fact-detail ${address ? 'mono' : 'num'}`}>{detail}</span>}
      </dd>
    </div>
  );
}

export function PlexStreamSheet({ stream, play, onClose, onStopped }: { stream?: PlexStream; play?: PlexPlay; onClose: () => void; onStopped?: (r: PlexResponse) => void }) {
  const now = useNow(5000);
  const notify = useToast();
  const canEdit = useCanEdit();
  const [choice, setChoice] = useState(0);
  const [custom, setCustom] = useState('');
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  // The stream as it was when the sheet opened, so it still reads right after it ends.
  const [last, setLast] = useState(stream);
  useEffect(() => {
    if (stream) setLast(stream);
  }, [stream]);
  // Focus moves into the sheet and stays there until it closes, then goes back to what opened it.
  const sheet = useRef<HTMLDivElement>(null);
  useDialog(sheet, !!(play || stream || last));

  useEffect(() => {
    // An Escape something on top already used (the finder) is not for the sheet.
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && !e.defaultPrevented && !busy && onClose();
    document.addEventListener('keydown', esc);
    return () => document.removeEventListener('keydown', esc);
  }, [busy, onClose]);

  const s = play ? asStream(play) : (stream ?? last);
  if (!s) return null;
  const ended = !stream;
  // Watched for: no longer than what was played runs, so one left paused overnight reads right.
  const watched = play ? Math.min(play.ended - play.started, play.duration_ms ? play.duration_ms / 1000 : Infinity) : 0;
  const hw = s.decision === 'transcode' ? hardware(s) : undefined;
  const message = choice === CUSTOM ? custom.trim() || DEFAULT_MESSAGE : PRESETS[choice];
  const player = s.player || s.product || 'the player';

  const stop = async () => {
    setBusy(true);
    setError('');
    try {
      const r = await api.stopPlexStream(s.id, message);
      notify({ tone: 'ok', title: `Stopped ${s.title} for ${s.user}`, body: `${player} shows: ${message}` });
      onStopped?.(r);
      onClose();
    } catch (err) {
      setError(errorText(err));
      setConfirming(false);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div className="sheet-backdrop" onMouseDown={() => !busy && onClose()}>
      <div ref={sheet} className="sheet stream-sheet" role="dialog" aria-modal="true" aria-label={`${s.title}, watched by ${s.user}`} onMouseDown={(e) => e.stopPropagation()}>
        <div className="stream-body">
          <div className="stream-head">
            <div className="min0">
              <span className="stream-kind">{KIND[s.kind] ?? s.kind}</span>
              <h2 className="truncate">{s.title}</h2>
              {s.subtitle && <span className="stream-sub truncate">{s.subtitle}</span>}
            </div>
            <button type="button" className="stream-close" aria-label="Close" onClick={onClose} disabled={busy}>
              <CloseIcon />
            </button>
          </div>

          <div className="stream-now">
            <div className="stream-who">
              <span className="avatar" aria-hidden="true">
                {s.user.slice(0, 1).toUpperCase()}
              </span>
              <div className="min0">
                <strong>{s.user}</strong>
                <span className="truncate">
                  {player}
                  {s.player && s.product ? ` · ${s.product}` : ''}
                </span>
              </div>
              <span className={`stream-state ${ended ? 'ended' : s.state}`}>{ended ? 'Ended' : s.state}</span>
            </div>
            <div className="progress" role="progressbar" aria-label="Progress" aria-valuenow={Math.round(s.progress)} aria-valuemin={0} aria-valuemax={100}>
              <span style={{ width: `${Math.max(0, Math.min(100, s.progress))}%` }} />
            </div>
            <div className="playback-line num">
              <span>{formatClock(s.offset_ms)}</span>
              <span className="ink-3">{s.duration_ms > 0 ? (play ? `${Math.round(s.progress)}% watched` : `${formatClock(Math.max(0, s.duration_ms - s.offset_ms))} left`) : ''}</span>
            </div>
          </div>

          {s.reason && (
            <div className="stream-why">
              <strong>{s.reason}</strong>
              {s.fix && <span>{s.fix}</span>}
            </div>
          )}
          <Formats s={s} />
          <dl className="stream-facts">
            <Fact label="Delivery" value={delivery(s)} detail={deliveryDetail(s)} />
            {s.decision === 'transcode' && !play && <Fact label="Transcoder" value={s.throttled ? 'Ahead, resting' : s.speed ? `${s.speed.toFixed(1)}× real time` : 'Working'} />}
            {hw && <Fact label="Converted on" value={hw.value} detail={hw.detail} />}
            <Fact label="Quality" value={s.quality || '—'} detail={s.bitrate_kbps ? formatKbps(s.bitrate_kbps) : undefined} />
            {play ? (
              <Fact label="Bandwidth" value={s.peak_kbps ? formatKbps(s.peak_kbps) : '—'} detail={s.peak_kbps ? 'at its peak' : undefined} />
            ) : (
              <Fact label="Bandwidth" value={formatKbps(s.bandwidth_kbps)} detail={s.peak_kbps && s.peak_kbps > s.bandwidth_kbps * 1.1 ? `peak ${formatKbps(s.peak_kbps)}` : undefined} />
            )}
            <Fact label="Network" value={s.local ? 'Local' : 'Remote'} detail={s.address} address />
            <Fact label="Platform" value={s.platform || '—'} />
            {play ? (
              <>
                <Fact label="Started" value={new Date(play.started * 1000).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' })} detail={`watched for ${formatDuration(watched)}`} />
                <Fact label="Finished" value={formatAgo(play.ended, now)} />
              </>
            ) : (
              <Fact label="Started" value={formatAgo(s.started_at, now)} />
            )}
          </dl>

          {!ended && canEdit && (
            <fieldset className="stream-messages" disabled={busy}>
              <legend>Message for {s.user}</legend>
              <p className="stream-hint">Shown on {player} when the stream stops.</p>
              <div className="stream-options">
                {PRESETS.map((p, i) => (
                  <label key={p} className={`stream-option ${choice === i ? 'on' : ''}`}>
                    <input type="radio" name="stream-message" checked={choice === i} onChange={() => setChoice(i)} />
                    <span>{p}</span>
                  </label>
                ))}
                <label className={`stream-option ${choice === CUSTOM ? 'on' : ''}`}>
                  <input type="radio" name="stream-message" checked={choice === CUSTOM} onChange={() => setChoice(CUSTOM)} />
                  <span>Write your own</span>
                </label>
              </div>
              {choice === CUSTOM && (
                <div className="stream-custom">
                  <textarea className="field" rows={2} maxLength={MAX} value={custom} placeholder={DEFAULT_MESSAGE} aria-label="Your message" autoFocus onChange={(e) => setCustom(e.target.value)} />
                  <span className="stream-count num">
                    {custom.length}/{MAX}
                  </span>
                </div>
              )}
            </fieldset>
          )}
        </div>

        {/* A viewer can't stop a stream, so a live one has no foot for them. */}
        {!play?.last && (play || ended || canEdit) && (
          <div className="stream-foot">
            {play ? (
              <p className="stream-ended">Finished before Nightjar kept how streams are delivered.</p>
            ) : ended ? (
              <p className="stream-ended">This stream has ended.</p>
            ) : (
              <>
                {error && (
                  <p className="form-error stream-error" role="alert">
                    {error}
                  </p>
                )}
                <div className="stream-foot-line">
                  <span className="stream-foot-text">{confirming ? `Stop ${s.user}'s stream now?` : 'Needs Plex Pass on the server owner’s account.'}</span>
                  {confirming ? (
                    <>
                      <button type="button" className="btn-quiet" onClick={() => setConfirming(false)} disabled={busy}>
                        Cancel
                      </button>
                      <button type="button" className="btn btn-stop" onClick={stop} disabled={busy}>
                        {busy ? 'Stopping…' : 'Yes, stop it'}
                      </button>
                    </>
                  ) : (
                    <button type="button" className="btn-stop-quiet" onClick={() => setConfirming(true)}>
                      Stop stream
                    </button>
                  )}
                </div>
              </>
            )}
          </div>
        )}
      </div>
    </div>
  );
}
