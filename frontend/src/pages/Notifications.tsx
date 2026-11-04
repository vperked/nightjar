import { useCallback, useEffect, useMemo, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router';
import { api } from '../api/client';
import { useNotifications } from '../api/NotificationsContext';
import type { AlertLevel, AlertRules, Notification, NotificationRules, QuietHours, Severity } from '../api/types';
import { SeverityIcon } from '../components/Bell';
import { MuteButton } from '../components/MuteButton';
import { AREA_SHORT, AREAS, delivery, linkFor, MUTE_FOR, plain, subjectLabel, timeLeft } from '../components/notifications';
import { Select } from '../components/Select';
import { TimePicker } from '../components/TimePicker';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { Seg, SubTabs } from '../components/ui';
import { formatAgo, plural } from '../format';
import { useNow } from '../hooks/useNow';

// The notification center: every alert the monitor raised (inbox), and where alerts go (rules,
// quiet hours and mutes).

function NotificationsTabs({ current }: { current: string }) {
  return (
    <SubTabs
      label="Notifications"
      current={current}
      items={[
        { to: '/notifications', label: 'Inbox' },
        { to: '/notifications/rules', label: 'Rules' },
      ]}
    />
  );
}

export function NotificationsPage() {
  const { pathname } = useLocation();
  return pathname === '/notifications/rules' ? <RulesView /> : <InboxView />;
}

// ---------- inbox ----------

type Show = 'all' | 'problems' | 'ok' | 'info';
const SHOW: Record<Show, Severity[] | undefined> = { all: undefined, problems: ['critical', 'warning'], ok: ['ok'], info: ['info'] };

// day heads a group of notifications: Today, Yesterday, or the date.
function day(ts: number): string {
  const d = new Date(ts * 1000);
  const today = new Date();
  const diff = Math.round((new Date(today.toDateString()).getTime() - new Date(d.toDateString()).getTime()) / 86400000);
  if (diff === 0) return 'Today';
  if (diff === 1) return 'Yesterday';
  return d.toLocaleDateString(undefined, { weekday: 'long', day: 'numeric', month: 'long' });
}

const clockTime = (ts: number) => new Date(ts * 1000).toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit' });

const DELIVERY_TONE: Record<Notification['status'], string> = { sent: 'sent', digest: 'sent', held: 'held', muted: 'muted', failed: 'failed', kept: 'kept', throttled: 'muted', maintenance: 'muted' };

function InboxRow({ n, now, open, onToggle, onRead }: { n: Notification; now: number; open: boolean; onToggle: () => void; onRead: (read: boolean) => void }) {
  const link = linkFor(n);
  const muteSubject = n.subject ?? `area:${n.area}`;
  const muteLabel = n.subject ? subjectLabel(n.subject) : (AREAS[n.area] ?? n.area);
  return (
    <li className={`inbox-row ${n.read ? '' : 'unread'} ${open ? 'open' : ''}`}>
      <button type="button" className="inbox-main" aria-expanded={open} onClick={onToggle}>
        <SeverityIcon severity={n.severity} />
        <span className="min0">
          <span className="inbox-title">{n.title}</span>
          {n.body && <span className={`inbox-body ${open ? '' : 'clamp'}`}>{plain(n.body)}</span>}
          <span className="inbox-meta">
            <span className="area-chip">{AREA_SHORT[n.area] ?? n.area}</span>
            <span className={`delivery ${DELIVERY_TONE[n.status]}`}>{delivery(n)}</span>
          </span>
        </span>
        <span className="inbox-time num" title={new Date(n.ts * 1000).toLocaleString()}>
          {now - n.ts < 3600 ? formatAgo(n.ts, now) : clockTime(n.ts)}
        </span>
      </button>
      {open && (
        <div className="inbox-detail">
          {n.fields.length > 0 && (
            <dl className="inbox-fields">
              {n.fields
                .filter((f) => f.value)
                .map((f, i) => (
                  <div key={i} className={f.value.includes('\n') ? 'wide' : ''}>
                    <dt>{f.name}</dt>
                    <dd className={f.value.startsWith('```') ? 'mono pre' : ''}>{plain(f.value).trim()}</dd>
                  </div>
                ))}
            </dl>
          )}
          <div className="inbox-actions">
            {link !== '/' && (
              <Link className="btn" to={link} onClick={() => !n.read && onRead(true)}>
                Open
              </Link>
            )}
            <MuteButton subject={muteSubject} label={muteLabel} />
            <button type="button" className="btn-quiet" onClick={() => onRead(!n.read)}>
              Mark {n.read ? 'unread' : 'read'}
            </button>
          </div>
        </div>
      )}
    </li>
  );
}

function InboxView() {
  const { summary, refresh } = useNotifications();
  const [query, setQuery] = useState('');
  const [search, setSearch] = useState('');
  const [show, setShow] = useState<Show>('all');
  const [area, setArea] = useState('');
  const [unread, setUnread] = useState(false);
  const [items, setItems] = useState<Notification[]>();
  const [total, setTotal] = useState(0);
  const [kept, setKept] = useState(true);
  const [error, setError] = useState('');
  const [openId, setOpenId] = useState<number>();
  const [loadingMore, setLoadingMore] = useState(false);
  const now = useNow(30000);
  const latest = summary?.counts.latest;

  // Typing settles for a moment before searching.
  useEffect(() => {
    const id = setTimeout(() => setSearch(query.trim()), 250);
    return () => clearTimeout(id);
  }, [query]);

  const filters = useMemo(() => ({ q: search, severity: SHOW[show], area, unread }), [search, show, area, unread]);

  // The newest page reloads when the filters change and when the bell sees something new; older
  // pages already loaded stay below it.
  useEffect(() => {
    let live = true;
    api.notifications(filters).then(
      (r) => {
        if (!live) return;
        setTotal(r.total);
        setKept(r.kept);
        setError('');
        setItems((old) => {
          const fresh = new Map(r.items.map((n) => [n.id, n]));
          const oldest = r.items.at(-1)?.id ?? 0;
          const older = (old ?? []).filter((n) => n.id < oldest && !fresh.has(n.id));
          return [...r.items, ...older];
        });
      },
      (e: Error) => live && setError(e.message),
    );
    return () => {
      live = false;
    };
  }, [filters, latest]);
  // A filter change starts from the top.
  useEffect(() => setItems(undefined), [filters]);

  const more = async () => {
    const last = items?.at(-1);
    if (!last) return;
    setLoadingMore(true);
    try {
      const r = await api.notifications({ ...filters, before: last.id });
      setItems((old) => [...(old ?? []), ...r.items]);
    } finally {
      setLoadingMore(false);
    }
  };

  const mark = async (ids: number[] | undefined, read: boolean) => {
    await api.markRead(ids, !read).catch(() => undefined);
    setItems((list) => list?.map((n) => (!ids || ids.includes(n.id) ? { ...n, read } : n)));
    refresh();
  };

  const groups = useMemo(() => {
    const out: { day: string; items: Notification[] }[] = [];
    for (const n of items ?? []) {
      const d = day(n.ts);
      if (out.at(-1)?.day !== d) out.push({ day: d, items: [] });
      out.at(-1)!.items.push(n);
    }
    return out;
  }, [items]);

  const filtered = !!(search || show !== 'all' || area || unread);
  const unreadCount = summary?.counts.unread ?? 0;
  return (
    <main>
      <div className="page-title">
        <div>
          <h1>Notifications</h1>
          <div className="page-sub">Every alert the monitor raised, and where it went.</div>
          <NotificationsTabs current="/notifications" />
        </div>
        <div className="toolbar">
          {unreadCount > 0 && (
            <button className="btn-quiet" onClick={() => mark(undefined, true)}>
              Mark all {unreadCount} read
            </button>
          )}
        </div>
      </div>

      {summary?.quiet && (
        <div className="quiet-banner" role="status">
          <span className="quiet-moon" aria-hidden="true">
            ☾
          </span>
          <span>
            <strong>Quiet hours are on.</strong> {summary.held ? `${plural(summary.held, 'alert')} held` : 'Alerts are held'} for one summary when they end.
          </span>
          <Link className="btn-quiet" to="/notifications/rules">
            Change
          </Link>
        </div>
      )}

      <div className="inbox-filters">
        <input className="search" type="search" placeholder="Search alerts…" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Search notifications" />
        <Seg<Show>
          label="Severity"
          value={show}
          onChange={setShow}
          options={[
            { value: 'all', label: 'All' },
            { value: 'problems', label: 'Problems' },
            { value: 'ok', label: 'Recovered' },
            { value: 'info', label: 'Info' },
          ]}
        />
        <Select
          className="short"
          label="Area"
          value={area}
          onChange={setArea}
          options={[{ value: '', label: 'All areas', text: 'all' }, ...Object.entries(AREAS).map(([value, label]) => ({ value, label }))]}
        />
        <label className="check">
          <input type="checkbox" checked={unread} onChange={(e) => setUnread(e.target.checked)} /> Unread only
        </label>
      </div>

      {error ? (
        <div className="alert">{error}</div>
      ) : !items ? (
        <Skeleton kind="list" />
      ) : items.length === 0 ? (
        <div className="inbox-empty">
          <div className="inbox-empty-mark" aria-hidden="true">
            ✓
          </div>
          <strong>{filtered ? 'Nothing matches' : 'All quiet'}</strong>
          <p>
            {filtered ? 'Try fewer filters.' : kept ? 'Alerts the monitor raises appear here, whether or not they were sent anywhere.' : 'The database is unavailable, so alerts are not being kept.'}
          </p>
        </div>
      ) : (
        <>
          {groups.map((g) => (
            <section key={g.day} className="inbox-day">
              <h2 className="inbox-day-head">{g.day}</h2>
              <ul className="inbox-list">
                {g.items.map((n) => (
                  <InboxRow
                    key={n.id}
                    n={n}
                    now={now}
                    open={openId === n.id}
                    onToggle={() => {
                      setOpenId(openId === n.id ? undefined : n.id);
                      if (!n.read && openId !== n.id) mark([n.id], true);
                    }}
                    onRead={(read) => mark([n.id], read)}
                  />
                ))}
              </ul>
            </section>
          ))}
          <div className="inbox-more">
            <span className="small ink-3 num">{items.length < total ? `${items.length} of ${total}` : plural(total, 'notification')} · kept for 90 days</span>
            {items.length < total && (
              <button className="btn-quiet" onClick={more} disabled={loadingMore}>
                {loadingMore ? 'Loading…' : 'Show older'}
              </button>
            )}
          </div>
        </>
      )}
    </main>
  );
}

// ---------- rules ----------

const LEVELS: { value: AlertLevel; label: string; text: string; hint: string }[] = [
  { value: 'all', label: 'Everything', text: 'everything', hint: 'Info, warnings and critical' },
  { value: 'warning', label: 'Warnings +', text: 'warnings', hint: 'Warnings and critical' },
  { value: 'critical', label: 'Critical only', text: 'critical', hint: 'Only what needs you now' },
  { value: 'off', label: 'Off', text: 'off', hint: 'Nothing goes here' },
];

function RulesView() {
  const [data, setData] = useState<NotificationRules>();
  const [rules, setRules] = useState<AlertRules>({});
  const [quiet, setQuiet] = useState<QuietHours>();
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const [muteArea, setMuteArea] = useState('torrents');
  const [muteFor, setMuteFor] = useState('60');
  const { summary, mute } = useNotifications();
  const notify = useToast();
  const navigate = useNavigate();
  const now = useNow(30000);

  const load = useCallback(() => {
    api.notificationRules().then(
      (r) => {
        setData(r);
        setRules(r.rules);
        setQuiet(r.quiet);
      },
      (e: Error) => setError(e.message),
    );
  }, []);
  useEffect(load, [load]);

  if (error) return <main className="alert">{error}</main>;
  if (!data || !quiet) return <Skeleton kind="form" />;

  const dirty = JSON.stringify(rules) !== JSON.stringify(data.rules) || JSON.stringify(quiet) !== JSON.stringify(data.quiet);
  const changed = (area: string, ch: string) => rules[area]?.[ch] !== data.defaults[area]?.[ch];
  const anyChanged = data.areas.some((a) => data.channels.some((c) => changed(a.id, c)));
  const setLevel = (area: string, ch: string, l: AlertLevel) => setRules({ ...rules, [area]: { ...rules[area], [ch]: l } });
  const mutes = summary?.mutes ?? data.mutes;

  const save = async () => {
    setSaving(true);
    try {
      const saved = await api.saveNotificationRules(rules, quiet);
      setData(saved);
      setRules(saved.rules);
      setQuiet(saved.quiet);
      notify({ tone: 'ok', title: 'Notification rules saved', body: 'They apply to the next alert.' });
    } catch (e) {
      notify({ tone: 'error', title: 'Could not save the rules', body: (e as Error).message });
    } finally {
      setSaving(false);
    }
  };

  return (
    <main className={dirty ? 'has-savebar' : ''}>
      <div className="page-title">
        <div>
          <h1>Notifications</h1>
          <div className="page-sub">Which channels hear about what, and when to keep quiet.</div>
          <NotificationsTabs current="/notifications/rules" />
        </div>
      </div>

      <section className="panel">
        <div className="section-head">
          <span className="label">Where alerts go</span>
          {anyChanged && (
            <button type="button" className="btn-quiet" onClick={() => setRules(data.defaults)}>
              Reset to defaults
            </button>
          )}
        </div>
        {data.channels.length === 0 ? (
          <div className="rules-empty">
            <p>
              <strong>No alert channels yet.</strong> Alerts are still kept in the inbox. Add Discord, ntfy, Telegram, Slack, email or a webhook to have them sent.
            </p>
            <button className="btn" onClick={() => navigate('/settings?find=WEBHOOK')}>
              Set up a channel
            </button>
          </div>
        ) : (
          <>
            <p className="form-help rules-help">Recoveries go wherever their alert went, so a channel that heard about an outage also hears that it is over.</p>
            <div className="rules" role="table" aria-label="Alert routing" style={{ ['--channels' as string]: data.channels.length }}>
              <div className="rules-row rules-headrow" role="row">
                <span role="columnheader">Alerts about</span>
                {data.channels.map((c) => (
                  <span key={c} role="columnheader" className="rules-channel">
                    {c}
                  </span>
                ))}
              </div>
              {data.areas.map((a) => (
                <div className="rules-row" role="row" key={a.id}>
                  <span role="rowheader" className="rules-area">
                    {a.label}
                  </span>
                  {data.channels.map((c) => {
                    const level = rules[a.id]?.[c];
                    return (
                      <span role="cell" key={c} className={`rules-cell ${level ?? 'none'} ${level && changed(a.id, c) ? 'changed' : ''}`}>
                        <span className="rules-cell-ch">{c}</span>
                        {level ? (
                          <Select label={`${a.label} on ${c}`} value={level} onChange={(v) => setLevel(a.id, c, v as AlertLevel)} options={LEVELS} />
                        ) : (
                          <span className="ink-3 small" title={`${c} has no webhook for these. Set one in Settings.`}>
                            no webhook
                          </span>
                        )}
                      </span>
                    );
                  })}
                </div>
              ))}
            </div>
          </>
        )}
      </section>

      <section className="panel">
        <div className="section-head">
          <span className="label">Quiet hours</span>
          {data.quiet_active && <span className="state warn">on now{data.held ? ` · ${data.held} held` : ''}</span>}
        </div>
        <p className="form-help">Alerts raised during quiet hours wait, then arrive as one summary when they end. Every alert still shows in the inbox straight away.</p>
        <div className="quiet-form">
          <label className="check">
            <input type="checkbox" checked={quiet.enabled} onChange={(e) => setQuiet({ ...quiet, enabled: e.target.checked })} /> Keep quiet
          </label>
          <span className="quiet-time">
            from
            <TimePicker value={quiet.start} disabled={!quiet.enabled} onChange={(v) => setQuiet({ ...quiet, start: v })} label="Quiet hours start" />
          </span>
          <span className="quiet-time">
            until
            <TimePicker value={quiet.end} disabled={!quiet.enabled} onChange={(v) => setQuiet({ ...quiet, end: v })} label="Quiet hours end" />
          </span>
          <label className="check">
            <input type="checkbox" checked={quiet.critical} disabled={!quiet.enabled} onChange={(e) => setQuiet({ ...quiet, critical: e.target.checked })} /> Send critical alerts anyway
          </label>
        </div>
      </section>

      <section className="panel">
        <div className="section-head">
          <span className="label">Muted</span>
          <span className="small ink-3">{mutes.length ? plural(mutes.length, 'mute') : 'nothing muted'}</span>
        </div>
        <p className="form-help">Mute a check, service or drive from its page, or a whole area here. Muted alerts still show in the inbox but go nowhere.</p>
        {mutes.length > 0 && (
          <ul className="mutes">
            {mutes.map((m) => (
              <li key={m.subject}>
                <span className="min0">
                  <span className="truncate">{m.label}</span>
                  <span className="sub">{m.subject.startsWith('area:') ? 'area' : m.subject.slice(0, m.subject.indexOf(':'))}</span>
                </span>
                <span className="num ink-3 small">{timeLeft(m.until, now)}</span>
                <button type="button" className="btn-quiet" onClick={() => mute(m.subject, m.label, 0)}>
                  Unmute
                </button>
              </li>
            ))}
          </ul>
        )}
        <div className="mute-area">
          <Select label="Area to mute" value={muteArea} onChange={setMuteArea} options={data.areas.map((a) => ({ value: a.id, label: a.label }))} />
          <Select className="short" label="Mute for" value={muteFor} onChange={setMuteFor} options={MUTE_FOR.map((m) => ({ value: String(m.minutes), label: `for ${m.label}`, text: m.label }))} />
          <button type="button" className="btn-quiet" onClick={() => mute(`area:${muteArea}`, AREAS[muteArea] ?? muteArea, Number(muteFor))}>
            Mute
          </button>
        </div>
      </section>

      {dirty && (
        <div className="savebar" role="region" aria-label="Unsaved changes">
          <span>Unsaved changes to the notification rules</span>
          <span className="savebar-actions">
            <button
              className="btn-quiet"
              onClick={() => {
                setRules(data.rules);
                setQuiet(data.quiet);
              }}
            >
              discard
            </button>
            <button className="btn" onClick={save} disabled={saving}>
              {saving ? 'saving…' : 'Save'}
            </button>
          </span>
        </div>
      )}
    </main>
  );
}
