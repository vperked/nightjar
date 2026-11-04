import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type MouseEvent } from 'react';
import { createPortal } from 'react-dom';
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
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const [place, setPlace] = useState<CSSProperties>();
  const until = mutedUntil(subject);

  useEffect(() => {
    if (!open) return;
    const close = (e: globalThis.MouseEvent) => {
      const t = e.target as Node;
      if (!root.current?.contains(t) && !menu.current?.contains(t)) setOpen(false);
    };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [open]);

  // The menu is fixed to the window, in a portal, so a list that clips its overflow cannot cut it off. It opens
  // under the button, lined up with whichever of its edges keeps it on screen, or above it when
  // there is no room below.
  useLayoutEffect(() => {
    if (!open) return;
    const position = () => {
      const b = button.current?.getBoundingClientRect();
      // offset sizes, as the opening animation scales the menu.
      const m = menu.current && { width: menu.current.offsetWidth, height: menu.current.offsetHeight };
      if (!b || !m) return;
      const gap = 6;
      const edge = 8;
      const left = b.right - m.width >= edge ? b.right - m.width : b.left;
      const below = b.bottom + gap + m.height <= window.innerHeight - edge;
      setPlace({
        left: Math.max(edge, Math.min(left, window.innerWidth - m.width - edge)),
        top: below ? b.bottom + gap : Math.max(edge, b.top - gap - m.height),
      });
    };
    position();
    menu.current?.querySelector('button')?.focus({ preventScroll: true });
    window.addEventListener('resize', position);
    window.addEventListener('scroll', position, true);
    return () => {
      window.removeEventListener('resize', position);
      window.removeEventListener('scroll', position, true);
    };
  }, [open]);

  if (!summary) return null;

  if (until) return <MutedTag subject={subject} label={label} compact={compact} />;

  return (
    <span className={`mute ${open ? 'open' : ''}`} ref={root} onClick={stop}>
      <button
        ref={button}
        type="button"
        className={compact ? 'icon-btn mute-btn' : 'btn-quiet mute-btn'}
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={`Mute ${label}`}
        title={`Mute ${label}'s alerts for a while`}
        onClick={() => setOpen((o) => !o)}
      >
        <BellOffIcon />
        {!compact && <span>Mute</span>}
      </button>
      {open &&
        createPortal(
          <div
            ref={menu}
            className="mute-menu"
            style={place}
            role="menu"
            aria-label={`Mute ${label} for`}
            onKeyDown={(e) => {
              if (e.key === 'Escape') {
                e.stopPropagation();
                setOpen(false);
                button.current?.focus();
              }
            }}
          >
            <div className="mute-menu-head">Mute alerts for</div>
            {MUTE_FOR.map((m) => (
              <button
                key={m.minutes}
                type="button"
                role="menuitem"
                onClick={() => {
                  setOpen(false);
                  mute(subject, label, m.minutes);
                }}
              >
                {m.label}
              </button>
            ))}
          </div>,
          document.body,
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
