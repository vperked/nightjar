import { useEffect, useLayoutEffect, useRef, useState, type CSSProperties, type MouseEvent, type Ref } from 'react';
import { createPortal } from 'react-dom';
import { useCanEdit } from '../api/ConfigContext';
import { useNotifications } from '../api/NotificationsContext';
import { useMenu } from '../hooks/useMenu';
import { useNow } from '../hooks/useNow';
import { MUTE_FOR, timeLeft } from './notifications';

const BellOffIcon = () => (
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
  const canEdit = useCanEdit();
  const keys = useMenu(menu, button, open, setOpen);
  // Muting swaps the button for the muted tag, and unmuting swaps it back. Once the server answers,
  // focus follows to the new control, as long as it had not gone somewhere else in the meantime.
  const swapped = useRef(false);
  const takeFocus = swapped.current && (!document.activeElement || document.activeElement === document.body || !!root.current?.contains(document.activeElement));
  useEffect(() => {
    swapped.current = false;
  }, [!!until]); // eslint-disable-line react-hooks/exhaustive-deps

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
      setPlace({ left: Math.max(edge, Math.min(left, window.innerWidth - m.width - edge)), top: below ? b.bottom + gap : Math.max(edge, b.top - gap - m.height) });
    };
    position();
    window.addEventListener('resize', position);
    window.addEventListener('scroll', position, true);
    return () => {
      window.removeEventListener('resize', position);
      window.removeEventListener('scroll', position, true);
    };
  }, [open]);

  if (!summary) return null;

  if (until) return <MutedTag subject={subject} label={label} compact={compact} rootRef={root} autoFocus={takeFocus} onUnmute={() => (swapped.current = true)} />;
  // A viewer sees what is muted, but muting is the admin's.
  if (!canEdit) return null;

  return (
    <span className={`mute ${open ? 'open' : ''}`} ref={root} onClick={stop} onBlur={keys.onBlur}>
      <button
        ref={button}
        autoFocus={takeFocus}
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
          <div ref={menu} className="mute-menu" style={place} role="menu" aria-label={`Mute ${label} for`} onKeyDown={keys.onKeyDown}>
            <div className="mute-menu-head">Mute alerts for</div>
            {MUTE_FOR.map((m) => (
              <button
                key={m.minutes}
                type="button"
                role="menuitem"
                tabIndex={-1}
                onClick={() => {
                  keys.done();
                  swapped.current = true;
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
// subject is not muted, so lists can carry it on every row. readOnly shows it without the unmute,
// for a place that is itself a button (a row that opens), where a button inside would be invalid
// and out of reach; the row then needs its unmute somewhere else.
export function MutedTag({
  subject,
  label,
  compact = false,
  readOnly = false,
  autoFocus = false,
  onUnmute,
  rootRef,
}: {
  subject: string;
  label: string;
  compact?: boolean;
  readOnly?: boolean;
  autoFocus?: boolean;
  onUnmute?: () => void;
  rootRef?: Ref<HTMLSpanElement>;
}) {
  const { mutedUntil, mute } = useNotifications();
  const now = useNow(30000);
  const canEdit = useCanEdit();
  const until = mutedUntil(subject);
  if (!until) return null;
  const text = <span className="num">{compact ? timeLeft(until, now).replace(' left', '') : `muted · ${timeLeft(until, now)}`}</span>;
  if (!canEdit || readOnly)
    return (
      <span className="mute">
        <span className="mute-on" title={`Alerts for ${label} are muted.`}>
          <BellOffIcon />
          {/* The compact tag is only a time; a screen reader needs to hear what it is the time of. */}
          {compact && <span className="sr-only">muted, </span>}
          {text}
        </span>
      </span>
    );
  return (
    <span className="mute" onClick={stop} ref={rootRef}>
      <button
        type="button"
        className="mute-on"
        autoFocus={autoFocus}
        onClick={() => {
          onUnmute?.();
          mute(subject, label, 0);
        }}
        title={`Alerts for ${label} are muted. Click to unmute.`}
        aria-label={`Unmute ${label}`}
      >
        <BellOffIcon />
        {text}
      </button>
    </span>
  );
}
