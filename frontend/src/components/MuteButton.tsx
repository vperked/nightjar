import { useEffect, useRef, useState, type MouseEvent } from 'react';
import { useNotifications } from '../api/NotificationsContext';
import { useNow } from '../hooks/useNow';
import { MUTE_FOR, timeLeft } from './notifications';

export const BellOffIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M8.7 3.9A6 6 0 0 1 18 9c0 3.1.6 5.2 1.3 6.6M17 17H4.5S6 15.5 6 9c0-.6.1-1.2.3-1.8M10.3 21a1.9 1.9 0 0 0 3.4 0M3 3l18 18" />
  </svg>
);

// MuteButton snoozes one check, service, drive or area: its alerts are still kept in the
// notification center but go to no channel until the mute runs out.
export function MuteButton({ subject, label, compact = false }: { subject: string; label: string; compact?: boolean }) {
  const { summary, mutedUntil, mute } = useNotifications();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLSpanElement>(null);
  const until = mutedUntil(subject);

  useEffect(() => {
    if (!open) return;
    const close = (e: globalThis.MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [open]);

  if (!summary) return null;

  if (until) return <MutedTag subject={subject} label={label} compact={compact} />;

  return (
    <span className={`mute ${open ? 'open' : ''}`} ref={root} onClick={stop}>
      <button
        type="button"
        className={compact ? 'icon-btn mute-btn' : 'btn-quiet mute-btn'}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Mute ${label}`}
        title={`Mute ${label}'s alerts for a while`}
        onClick={() => setOpen((o) => !o)}
      >
        <BellOffIcon />
        {!compact && <span>mute</span>}
      </button>
      {open && (
        <div
          className="mute-menu"
          role="menu"
          aria-label={`Mute ${label} for`}
          onKeyDown={(e) => {
            if (e.key === 'Escape') {
              e.stopPropagation();
              setOpen(false);
            }
          }}
        >
          <div className="mute-menu-head">Mute alerts for</div>
          {MUTE_FOR.map((m, i) => (
            <button
              key={m.minutes}
              type="button"
              role="menuitem"
              autoFocus={i === 0}
              onClick={() => {
                setOpen(false);
                mute(subject, label, m.minutes);
              }}
            >
              {m.label}
            </button>
          ))}
        </div>
      )}
    </span>
  );
}

// Rows that open on click must not open from a click on their mute button.
const stop = (e: MouseEvent) => e.stopPropagation();

// MutedTag shows how long a mute has left, and unmutes on a click. It shows nothing when the
// subject is not muted, so lists can carry it on every row.
export function MutedTag({ subject, label, compact = false }: { subject: string; label: string; compact?: boolean }) {
  const { mutedUntil, mute } = useNotifications();
  const now = useNow(30000);
  const until = mutedUntil(subject);
  if (!until) return null;
  return (
    <span className="mute" onClick={stop}>
      <button type="button" className="mute-on" onClick={() => mute(subject, label, 0)} title={`Alerts for ${label} are muted. Click to unmute.`} aria-label={`Unmute ${label}`}>
        <BellOffIcon />
        <span className="num">{compact ? timeLeft(until, now).replace(' left', '') : `muted · ${timeLeft(until, now)}`}</span>
      </button>
    </span>
  );
}
