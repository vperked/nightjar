import { useEffect, useRef, useState } from 'react';
import { Link, useNavigate } from 'react-router';
import { api } from '../api/client';
import { useNotifications } from '../api/NotificationsContext';
import type { Notification, Severity } from '../api/types';
import { formatAgo, plural } from '../format';
import { useNow } from '../hooks/useNow';
import { AREA_SHORT, delivery, linkFor, plain } from './notifications';

// The header's bell: how many alerts are unread, and a panel with the latest few.

const GLYPH: Record<Severity, string> = {
  critical: 'M8 8l8 8M16 8l-8 8',
  warning: 'M12 7v6M12 16.5v.5',
  ok: 'M7.5 12.5l3 3 6-6.5',
  info: 'M12 11v5.5M12 7.5v.5',
};

export const SeverityIcon = ({ severity }: { severity: Severity }) => (
  <span className={`sev-icon ${severity}`} aria-label={severity === 'ok' ? 'recovered' : severity} role="img">
    <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
      <path d={GLYPH[severity]} />
    </svg>
  </span>
);

const BellIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d="M6 9a6 6 0 1 1 12 0c0 6.5 1.5 8 1.5 8h-15S6 15.5 6 9M10.3 21a1.9 1.9 0 0 0 3.4 0" />
  </svg>
);

// NotificationLine is one notification in the bell's panel.
function NotificationLine({ n, now, onOpen }: { n: Notification; now: number; onOpen: () => void }) {
  return (
    <li>
      <button type="button" className={`bell-item ${n.read ? '' : 'unread'}`} onClick={onOpen}>
        <SeverityIcon severity={n.severity} />
        <span className="min0">
          <span className="bell-title truncate">{n.title}</span>
          {n.body && <span className="bell-body">{plain(n.body)}</span>}
          <span className="bell-meta">
            {AREA_SHORT[n.area] ?? n.area} · {formatAgo(n.ts, now)}
            {n.status !== 'sent' && <> · {delivery(n)}</>}
          </span>
        </span>
      </button>
    </li>
  );
}

export function Bell() {
  const { summary, refresh } = useNotifications();
  const [open, setOpen] = useState(false);
  const [items, setItems] = useState<Notification[]>();
  const [ringing, setRinging] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const seen = useRef<number>();
  const navigate = useNavigate();
  const now = useNow(30000);
  const counts = summary?.counts;

  // The bell rings once when a new warning or critical arrives.
  useEffect(() => {
    if (!counts) return;
    const before = seen.current;
    seen.current = counts.latest;
    if (before !== undefined && counts.latest > before && counts.alerts > 0) {
      setRinging(true);
      const id = setTimeout(() => setRinging(false), 1200);
      return () => clearTimeout(id);
    }
  }, [counts]);

  // The panel loads when it opens, and again when something new arrives while it is open.
  useEffect(() => {
    if (!open) return;
    api.notifications().then(
      (r) => setItems(r.items.slice(0, 8)),
      () => setItems([]),
    );
  }, [open, counts?.latest]);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false);
    document.addEventListener('mousedown', close);
    document.addEventListener('keydown', esc);
    return () => {
      document.removeEventListener('mousedown', close);
      document.removeEventListener('keydown', esc);
    };
  }, [open]);

  const go = (to: string) => {
    setOpen(false);
    navigate(to);
  };
  const openItem = (n: Notification) => {
    if (!n.read) api.markRead([n.id]).then(refresh, () => undefined);
    go(linkFor(n));
  };
  const readAll = async () => {
    await api.markRead().catch(() => undefined);
    setItems((list) => list?.map((n) => ({ ...n, read: true })));
    refresh();
  };

  const unread = counts?.unread ?? 0;
  const alerts = counts?.alerts ?? 0;
  const label = alerts ? `Notifications, ${plural(alerts, 'unread alert')}` : unread ? `Notifications, ${unread} unread` : 'Notifications';
  return (
    <div className={`bell ${open ? 'open' : ''}`} ref={root}>
      <button type="button" className={`icon-btn bell-btn ${ringing ? 'ringing' : ''}`} aria-label={label} title={label} aria-haspopup="dialog" aria-expanded={open} onClick={() => setOpen((o) => !o)}>
        <BellIcon />
        {alerts > 0 ? (
          <span className={`bell-badge ${counts?.worst}`} aria-hidden="true">
            {alerts > 99 ? '99+' : alerts}
          </span>
        ) : (
          unread > 0 && <span className="bell-dot" aria-hidden="true" />
        )}
      </button>
      {open && (
        <div className="bell-panel" role="dialog" aria-label="Latest notifications">
          <div className="bell-head">
            <strong>Notifications</strong>
            {unread > 0 && (
              <button type="button" className="btn-quiet" onClick={readAll}>
                mark all read
              </button>
            )}
          </div>
          {summary?.quiet && (
            <div className="bell-quiet">
              <span className="quiet-moon" aria-hidden="true">
                ☾
              </span>
              Quiet hours: {summary.held ? `${plural(summary.held, 'alert')} held for the morning summary` : 'alerts are held for a summary'}
            </div>
          )}
          {!items ? (
            <div className="bell-empty">Loading…</div>
          ) : items.length === 0 ? (
            <div className="bell-empty">
              <strong>All quiet.</strong>
              <span>Alerts the monitor raises show up here, whether or not they went anywhere.</span>
            </div>
          ) : (
            <ul className="bell-list">
              {items.map((n) => (
                <NotificationLine key={n.id} n={n} now={now} onOpen={() => openItem(n)} />
              ))}
            </ul>
          )}
          <div className="bell-foot">
            <Link to="/notifications" onClick={() => setOpen(false)}>
              All notifications
            </Link>
            <Link to="/notifications/rules" onClick={() => setOpen(false)}>
              Rules and quiet hours
            </Link>
          </div>
        </div>
      )}
    </div>
  );
}
