import { useCallback, useMemo, useState } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import type { Incident, IncidentCause, IncidentEvent } from '../api/types';
import { Skeleton } from '../components/Skeleton';
import { Legend, TrendChart } from '../components/TrendChart';
import { Select } from '../components/Select';
import { useToast } from '../components/Toasts';
import { Seg, SubTabs } from '../components/ui';
import { formatDuration, plural } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';

// Incidents are built from every alert Nightjar raised and the machine's own readings: each
// stretch of trouble, what happened in the minutes before and during it, and the likely cause.

const RANGES = [
  { value: 1, label: '24h' },
  { value: 7, label: '7d' },
  { value: 30, label: '30d' },
  { value: 90, label: '90d' },
];

// KIND names a cause in a few words, for the summary and the tag beside it.
export const KIND: Record<IncidentCause['kind'], string> = {
  memory: 'Out of memory',
  disk: 'Disk full',
  hardware: 'Hardware fault',
  restart: 'Machine restarted',
  crash: 'Service crashed',
  network: 'Network',
  change: 'After a change',
  overload: 'Overloaded',
  check: "The check's own error",
  first: 'First to fail',
};

const ROLE: Record<IncidentEvent['role'], string> = { before: 'before', problem: 'problem', recovery: 'recovered', action: 'Nightjar', note: 'meanwhile' };

const clock = (ts: number) => new Date(ts * 1000).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });
const day = (ts: number) => new Date(ts * 1000).toLocaleDateString(undefined, { weekday: 'short', month: 'short', day: 'numeric' });

// offset is an event's time from the incident's start: "−4m", "+0:40", "+1h 12m".
function offset(seconds: number): string {
  const s = Math.round(Math.abs(seconds));
  const sign = seconds < 0 ? '−' : '+';
  if (s < 3600) return `${sign}${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`;
  return `${sign}${formatDuration(s)}`;
}

function length(inc: Incident, now: number) {
  return (inc.ended ?? now) - inc.started;
}

// fixedByNightjar: a repair ran in it and it ended.
const fixedByNightjar = (inc: Incident) => inc.ended !== null && inc.events.some((e) => e.role === 'action' && e.severity !== 'warning');

function Cause({ cause, also }: { cause: IncidentCause | null; also: IncidentCause[] }) {
  if (!cause) return <p className="inc-cause none">Nothing else happened on the machine around it.</p>;
  return (
    <div className="inc-cause">
      <span className={`inc-confidence ${cause.confidence}`}>{cause.confidence === 'likely' ? 'Likely cause' : 'Possible cause'}</span>
      <span className="inc-kind">{KIND[cause.kind]}</span>
      {cause.learned && <span className="inc-kind learned">from your feedback</span>}
      <p>{cause.text}.</p>
      {also.length > 0 && (
        <ul className="inc-also">
          {also.map((c) => (
            <li key={c.kind}>
              <span className="inc-kind">{KIND[c.kind]}</span> {c.text}.
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

function Timeline({ inc }: { inc: Incident }) {
  return (
    <ol className="inc-timeline">
      {inc.events.map((e) => (
        <li key={e.id} className={`role-${e.role} sev-${e.severity} ${inc.cause?.event === e.id ? 'is-cause' : ''}`}>
          <span className="inc-at num" title={new Date(e.ts * 1000).toLocaleString()}>
            {clock(e.ts)}
            <span>{offset(e.ts - inc.started)}</span>
          </span>
          <span className="inc-mark" aria-hidden="true" />
          <span className="min0">
            <span className="inc-role">{ROLE[e.role]}</span>
            <strong>{e.title}</strong>
            {inc.cause?.event === e.id && <span className="inc-flag">the likely cause</span>}
            {e.body && <span className="inc-body">{e.body}</span>}
          </span>
        </li>
      ))}
    </ol>
  );
}

// Readings shows the machine's processor and memory around the incident, marking where it began.
function Readings({ inc }: { inc: Incident }) {
  const series = useMemo(
    () => [
      { label: 'Memory', color: 'var(--series-1)', points: inc.series.map((p) => ({ t: p.t, v: p.mem })) },
      { label: 'CPU', color: 'var(--series-2)', points: inc.series.map((p) => ({ t: p.t, v: p.cpu })) },
    ],
    [inc.series],
  );
  if (inc.series.length < 2) return null;
  return (
    <div className="inc-readings">
      <div className="inc-readings-head">
        <span className="label">The machine around it</span>
        <Legend series={series} />
      </div>
      <TrendChart series={series} height={130} max={100} format={(v) => `${Math.round(v)}%`} markers={inc.events.filter((e) => e.role === 'problem').map((e) => e.ts)} />
    </div>
  );
}

// IncidentsTabs joins incidents, planned maintenance and the weekly report under one tab.
export function IncidentsTabs({ current }: { current: '/incidents' | '/incidents/maintenance' | '/incidents/report' }) {
  return (
    <SubTabs
      label="Incidents"
      current={current}
      items={[
        { to: '/incidents', label: 'Incidents' },
        { to: '/incidents/maintenance', label: 'Maintenance' },
        { to: '/incidents/report', label: 'Weekly report' },
      ]}
    />
  );
}

const KINDS = Object.keys(KIND) as IncidentCause['kind'][];

// Feedback asks whether the cause was right, and what it really was if not. What the owner says
// weighs the causes of later incidents of the same checks and services.
function Feedback({ inc, onSaved }: { inc: Incident; onSaved: () => void }) {
  const [verdict, setVerdict] = useState<'right' | 'wrong' | null>(null);
  const [actual, setActual] = useState<IncidentCause['kind'] | ''>('');
  const [note, setNote] = useState(inc.feedback?.note ?? '');
  const [saving, setSaving] = useState(false);
  const notify = useToast();
  const fb = inc.feedback;
  const save = async (v: 'right' | 'wrong', a: IncidentCause['kind'] | '' = '', n = note) => {
    setSaving(true);
    try {
      await api.feedback(inc.id, { verdict: v, actual: a, note: n });
      notify({ tone: 'ok', title: 'Thanks', body: 'Nightjar will weigh this for the next incidents like it.' });
      setVerdict(null);
      onSaved();
    } catch (e) {
      notify({ tone: 'error', title: 'Could not save the feedback', body: (e as Error).message });
    } finally {
      setSaving(false);
    }
  };
  if (!inc.cause) return null;
  if (fb && verdict === null)
    return (
      <div className="inc-feedback done">
        <span className="inc-feedback-q">
          You said: {fb.verdict === 'right' ? 'that was the cause' : fb.actual ? `it was really ${KIND[fb.actual as IncidentCause['kind']].toLowerCase()}` : 'that was not the cause'}
        </span>
        {fb.note && <span className="inc-feedback-note">“{fb.note}”</span>}
        <button type="button" className="btn-ghost" onClick={() => setVerdict(fb.verdict)}>
          Change
        </button>
      </div>
    );
  return (
    <div className="inc-feedback">
      <span className="inc-feedback-q">Was that the cause?</span>
      <button type="button" className={`chip-btn ${verdict === 'right' ? 'on' : ''}`} disabled={saving} onClick={() => (fb ? setVerdict('right') : save('right'))}>
        Yes
      </button>
      <button type="button" className={`chip-btn ${verdict === 'wrong' ? 'on' : ''}`} disabled={saving} onClick={() => setVerdict('wrong')}>
        No
      </button>
      {verdict && (
        <div className="inc-feedback-more">
          {verdict === 'wrong' && (
            <Select
              label="What was it?"
              value={actual}
              onChange={(v) => setActual(v as IncidentCause['kind'] | '')}
              options={[{ value: '', label: "I don't know" }, ...KINDS.filter((k) => k !== 'first' && k !== inc.cause?.kind).map((k) => ({ value: k, label: KIND[k] }))]}
            />
          )}
          <input className="field" aria-label="A note" placeholder="A note for later (optional)" value={note} onChange={(e) => setNote(e.target.value)} />
          <button type="button" className="btn" disabled={saving} onClick={() => save(verdict, actual)}>
            Save
          </button>
        </div>
      )}
    </div>
  );
}

// NoticeEditor puts an incident on the public status page, in words drafted for visitors.
function NoticeEditor({ inc, onSaved }: { inc: Incident; onSaved: () => void }) {
  const start = inc.notice ?? inc.draft;
  const [open, setOpen] = useState(false);
  const [title, setTitle] = useState(start?.title ?? '');
  const [text, setText] = useState(start?.text ?? '');
  const notify = useToast();
  if (!start) return null;
  const published = inc.notice?.published ?? false;
  const save = async (publish: boolean | null) => {
    try {
      await api.notice(inc.id, publish === null ? null : { title, text, published: publish });
      notify({
        tone: 'ok',
        title: publish === null ? 'Taken off the status page' : publish ? 'On the status page' : 'Notice saved',
        body: publish ? 'Visitors see it under Recent incidents.' : undefined,
      });
      setOpen(false);
      onSaved();
    } catch (e) {
      notify({ tone: 'error', title: 'Could not save the notice', body: (e as Error).message });
    }
  };
  return (
    <div className="inc-notice">
      {!open ? (
        <>
          {published && <span className="inc-state closed">On the status page</span>}
          <button type="button" className="btn-ghost" onClick={() => setOpen(true)}>
            {published ? 'Edit the notice' : inc.notice ? 'Finish the notice' : 'Tell visitors on the status page'}
          </button>
        </>
      ) : (
        <div className="inc-notice-form">
          <span className="label">What visitors read</span>
          <input className="field" aria-label="Notice title" value={title} onChange={(e) => setTitle(e.target.value)} maxLength={120} />
          <textarea className="field" aria-label="Notice text" rows={3} value={text} onChange={(e) => setText(e.target.value)} maxLength={1000} />
          <span className="form-help">Drafted from the incident with the names the status page uses, and nothing internal. Edit it as you like.</span>
          <div className="inc-notice-actions">
            {inc.notice && (
              <button type="button" className="btn-quiet danger" onClick={() => save(null)}>
                Remove
              </button>
            )}
            <button type="button" className="btn-quiet" onClick={() => setOpen(false)}>
              Cancel
            </button>
            {!published && (
              <button type="button" className="btn-quiet" onClick={() => save(false)}>
                Save without publishing
              </button>
            )}
            <button type="button" className="btn" onClick={() => save(true)} disabled={!title.trim()}>
              {published ? 'Update' : 'Publish'}
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

function IncidentCard({ inc, now, open: startOpen, onChanged }: { inc: Incident; now: number; open: boolean; onChanged: () => void }) {
  const [open, setOpen] = useState(startOpen);
  const ongoing = inc.ended === null;
  const peaks = [
    inc.peaks.mem !== null && `memory ${Math.round(inc.peaks.mem)}%`,
    inc.peaks.cpu !== null && `CPU ${Math.round(inc.peaks.cpu)}%`,
    inc.peaks.disk !== null && `disk ${Math.round(inc.peaks.disk)}%`,
  ].filter(Boolean);
  return (
    <article className={`section incident sev-${inc.severity} ${ongoing ? 'ongoing' : ''}`}>
      <header className="inc-head">
        <span className={`inc-state ${ongoing ? 'open' : 'closed'}`}>{ongoing ? 'Ongoing' : fixedByNightjar(inc) ? 'Fixed by Nightjar' : 'Resolved'}</span>
        <h2>{inc.title}</h2>
        <span className="inc-when num">
          {day(inc.started)} · {clock(inc.started)} · {ongoing ? `for ${formatDuration(length(inc, now))} so far` : `lasted ${formatDuration(length(inc, now))}`}
        </span>
      </header>
      <Cause cause={inc.cause} also={inc.also} />
      <div className="inc-affected">
        {inc.affected.map((a) => (
          <Link
            key={a.subject || a.label}
            className={`inc-hit kind-${a.kind} ${a.up ? 'up' : 'down'}`}
            to={a.kind === 'check' ? `/services/${encodeURIComponent(a.label)}` : a.kind === 'service' ? '/system' : '/notifications'}
          >
            {a.label}
            {/* A problem with nothing to recover from (a memory warning) has no end of its own. */}
            <span>{a.up ? `back after ${formatDuration(a.up - a.down)}` : a.subject ? 'still down' : clock(a.down)}</span>
          </Link>
        ))}
        {peaks.length > 0 && <span className="inc-peaks">peaks: {peaks.join(' · ')}</span>}
      </div>
      <button type="button" className="inc-toggle" aria-expanded={open} onClick={() => setOpen(!open)}>
        {open ? 'Hide' : 'Show'} the timeline · {plural(inc.events.length, 'event')}
      </button>
      {open && (
        <div className="inc-detail">
          <Timeline inc={inc} />
          <Readings inc={inc} />
        </div>
      )}
      <footer className="inc-foot">
        <Feedback inc={inc} onSaved={onChanged} />
        <NoticeEditor inc={inc} onSaved={onChanged} />
      </footer>
    </article>
  );
}

export function IncidentsPage() {
  const [days, setDays] = useState(7);
  const fetch = useCallback(() => api.incidents(days), [days]);
  const { data, error, refresh } = usePoll(fetch, 30000);
  const now = useNow(10000);
  const list = data?.incidents ?? [];
  const open = list.filter((i) => i.ended === null).length;
  const down = list.reduce((n, i) => n + length(i, now), 0);
  const kinds = list.reduce<Record<string, number>>((m, i) => (i.cause && i.cause.kind !== 'first' ? { ...m, [i.cause.kind]: (m[i.cause.kind] ?? 0) + 1 } : m), {});
  const usual = Object.entries(kinds).sort((a, b) => b[1] - a[1])[0];
  const fixed = list.filter(fixedByNightjar).length;
  return (
    <main>
      <div className="page-title">
        <div className="min0">
          <h1>Incidents</h1>
          <IncidentsTabs current="/incidents" />
          <div className="page-sub">What went wrong, what happened on the machine around it, and why.</div>
        </div>
        <Seg label="Time range" value={days} onChange={setDays} options={RANGES} />
      </div>
      {error && !data && <p className="alert">Could not load the incidents: {error.message}</p>}
      {!data && !error && <Skeleton kind="list" />}
      {data && (
        <>
          <div className="stats-row">
            <div>
              <span className="label">Incidents</span>
              <div className="readout-value">{list.length}</div>
              <div className="readout-sub">{open ? `${open} still open` : 'none open'}</div>
            </div>
            <div>
              <span className="label">Time in trouble</span>
              <div className="readout-value">{list.length ? formatDuration(down) : '—'}</div>
              <div className="readout-sub">across the {RANGES.find((r) => r.value === days)?.label}</div>
            </div>
            <div>
              <span className="label">Usual cause</span>
              <div className="readout-value inc-usual">{usual ? KIND[usual[0] as IncidentCause['kind']] : '—'}</div>
              <div className="readout-sub">{usual ? `behind ${plural(usual[1], 'incident')}` : 'nothing stands out'}</div>
            </div>
            <div>
              <span className="label">Fixed by Nightjar</span>
              <div className="readout-value">{fixed}</div>
              <div className="readout-sub">
                <Link to="/docs/uptime-checks#repairs">set up repairs →</Link>
              </div>
            </div>
          </div>
          {list.length === 0 ? (
            <div className="empty">
              <strong>No incidents in this range</strong>
              Everything Nightjar watches stayed up. When something breaks, it shows here with what led up to it.
            </div>
          ) : (
            list.map((inc, i) => <IncidentCard key={inc.id} inc={inc} now={now} open={i === 0} onChanged={refresh} />)
          )}
        </>
      )}
    </main>
  );
}
