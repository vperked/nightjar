import { useCallback, useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { api } from '../api/client';
import { useServices } from '../api/ServicesContext';
import type { CheckSpec, HistoryPoint } from '../api/types';
import { HeartbeatPanel, RepairsPanel, runtime } from '../components/CheckExtras';
import { Diagnosis } from '../components/Diagnosis';
import { MuteButton } from '../components/MuteButton';
import { useToast } from '../components/Toasts';
import { TrendChart } from '../components/TrendChart';
import { Pct, Section, Seg, State, Ticks } from '../components/ui';
import { formatAgo, formatDuration } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';
import { CheckForm } from './CheckForm';

const RANGES = [
  { value: 1, label: '1h' },
  { value: 24, label: '24h' },
  { value: 168, label: '7d' },
  { value: 720, label: '30d' },
];

const ms = (v: number) => `${Math.round(v)}ms`;

// shortTime reads like "Nov 2, 10:56 AM" in the browser's own language.
const shortTime = (ts: number) => new Date(ts * 1000).toLocaleString(undefined, { month: 'short', day: 'numeric', hour: 'numeric', minute: '2-digit' });
const clockTime = (ts: number) => new Date(ts * 1000).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' });

// span reads like "Nov 2, 10:27 – 10:49 PM", naming the day twice only when it changes.
function span(from: number, to: number): string {
  if (from === to) return shortTime(from);
  const sameDay = new Date(from * 1000).toDateString() === new Date(to * 1000).toDateString();
  return `${shortTime(from)} – ${sameDay ? clockTime(to) : shortTime(to)}`;
}

type Outage = { from: number; to: number; fails: number; detail: string };

// outages folds failed checks in a row with the same reason into one, so a long outage reads as
// one line rather than a page of identical ones. A passing check in between starts a new one.
function outages(points: HistoryPoint[]): Outage[] {
  const out: Outage[] = [];
  let open: Outage | undefined;
  for (const p of points) {
    if (p.up) {
      open = undefined;
      continue;
    }
    const detail = p.detail || 'down';
    if (open && open.detail === detail) {
      open.to = p.ts;
      open.fails += p.fails ?? 1;
    } else {
      open = { from: p.ts, to: p.ts, fails: p.fails ?? 1, detail };
      out.push(open);
    }
  }
  return out;
}

export function ServiceDetail() {
  const { name = '' } = useParams();
  const [hours, setHours] = useState(24);
  const services = useServices();
  const fetchHistory = useCallback(() => api.history(name, hours), [name, hours]);
  const history = usePoll(fetchHistory, 30000);
  const now = useNow(5000);

  const points = useMemo(() => history.data?.points ?? [], [history.data]);
  const series = useMemo(() => [{ label: 'Response time', color: 'var(--series-1)', points: points.map((p) => ({ t: p.ts, v: p.latency_ms })) }], [points]);
  const latencies = points.filter((p) => p.latency_ms !== null).map((p) => p.latency_ms as number);
  const sorted = [...latencies].sort((a, b) => a - b);
  const avg = latencies.length ? latencies.reduce((a, b) => a + b, 0) / latencies.length : null;
  const p95 = sorted.length ? sorted[Math.floor(sorted.length * 0.95)] : null;
  const failures = points.filter((p) => !p.up);
  const failCount = failures.reduce((n, p) => n + (p.fails ?? 1), 0);
  const runs = useMemo(() => outages(points), [points]);
  const svc = services.data?.services.find((s) => s.name === name);
  const navigate = useNavigate();
  const [editing, setEditing] = useState<CheckSpec>();
  const [confirmDelete, setConfirmDelete] = useState(false);
  const notify = useToast();

  // act runs a change to this check, then refreshes the shared services poll.
  const act = async (change: (spec: CheckSpec) => Promise<void>) => {
    try {
      const spec = (await api.checks()).checks.find((c) => c.name === name);
      if (!spec) throw new Error('This check no longer exists.');
      await change(spec);
      services.refresh();
    } catch (err) {
      notify({ tone: 'error', title: 'Could not change the check', body: (err as Error).message });
    }
  };
  const edit = () => act(async (spec) => setEditing(spec));
  const togglePause = () =>
    act(async (spec) => {
      await api.saveCheck({ ...spec, paused: !spec.paused }, name);
      notify({ tone: 'ok', title: spec.paused ? `${name} resumed` : `${name} paused`, body: spec.paused ? 'It runs on the next cycle.' : 'It will not run or alert until resumed.' });
    });
  const remove = () =>
    act(async () => {
      await api.deleteCheck(name);
      notify({ tone: 'ok', title: `${name} deleted`, body: 'Its history was removed too.' });
      navigate('/services');
    });

  return (
    <main>
      <div className="page-title">
        <div className="min0">
          <div className="crumb">
            <Link to="/services">Services</Link> /
          </div>
          <h1 className="truncate">{name}</h1>
          {svc && (
            <div className="title-meta">
              <State status={svc.status} />
              <span className="svc-target truncate">{svc.type === 'heartbeat' ? `heartbeat · ${svc.schedule}` : `${svc.type} · ${svc.target}`}</span>
            </div>
          )}
        </div>
        {svc && (
          <div className="title-actions">
            <button className="btn-quiet" onClick={edit}>
              Edit
            </button>
            <button className="btn-quiet" onClick={togglePause}>
              {svc.status === 'paused' ? 'Resume' : 'Pause'}
            </button>
            <MuteButton subject={`check:${name}`} label={name} />
            {confirmDelete ? (
              <span className="confirm">
                Delete it and its history?{' '}
                <button className="btn-quiet danger" onClick={remove}>
                  Delete
                </button>{' '}
                <button className="btn-quiet" onClick={() => setConfirmDelete(false)}>
                  Keep
                </button>
              </span>
            ) : (
              <button className="btn-quiet" onClick={() => setConfirmDelete(true)}>
                Delete
              </button>
            )}
          </div>
        )}
      </div>
      {editing && (
        <CheckForm
          initial={editing}
          onCancel={() => setEditing(undefined)}
          onSaved={(check) => {
            setEditing(undefined);
            services.refresh();
            if (check.name !== name) navigate(`/services/${encodeURIComponent(check.name)}`, { replace: true });
          }}
        />
      )}

      {services.data && !svc ? (
        <p className="note">No check with this name. It may have been deleted or renamed.</p>
      ) : (
        <>
          {svc && (
            <div className="stats-row">
              {svc.type === 'heartbeat' ? (
                <div>
                  <span className="label">Last run took</span>
                  <div className="readout-value">{runtime(svc) !== null ? formatDuration(runtime(svc) as number) : '—'}</div>
                  <div className="readout-sub">{svc.pings?.ok ? `finished ${formatAgo(svc.pings.ok, now)}` : 'no run reported yet'}</div>
                </div>
              ) : (
                <div>
                  <span className="label">Response</span>
                  <div className="readout-value">{svc.latency_ms !== null ? ms(svc.latency_ms) : '—'}</div>
                  <div className="readout-sub">{avg !== null ? `avg ${ms(avg)} · p95 ${ms(p95 ?? 0)}` : 'no data in range'}</div>
                </div>
              )}
              <div>
                <span className="label">Uptime 24h</span>
                <div className="readout-value">
                  <Pct value={svc.uptime_24h} />
                </div>
                <div className="readout-sub num">
                  7d {svc.uptime_7d?.toFixed(2) ?? '—'} · 30d {svc.uptime_30d?.toFixed(2) ?? '—'}
                </div>
              </div>
              <div>
                <span className="label">Last check</span>
                <div className="readout-value">{formatAgo(svc.last_check, now)}</div>
                <div className="readout-sub truncate" title={svc.detail}>
                  {svc.detail || '—'}
                </div>
              </div>
              <div>
                <span className="label">In this state</span>
                <div className="readout-value">{svc.last_change ? formatAgo(svc.last_change, now).replace(' ago', '') : '—'}</div>
                <div className="readout-sub">{svc.last_change ? `since ${shortTime(svc.last_change)}` : 'no change recorded'}</div>
              </div>
            </div>
          )}
          {svc?.cause && svc.status === 'down' && <Diagnosis findings={[svc.cause]} label="Why it is down" />}
          {svc && (
            <Section label="Last 60 checks" aside={<span className="small ink-3">oldest → newest</span>}>
              <Ticks beats={svc.recent} slots={60} tall />
            </Section>
          )}
          {svc?.type === 'heartbeat' && <HeartbeatPanel svc={svc} now={now} />}
          {svc && <RepairsPanel svc={svc} now={now} />}
          {svc?.type !== 'heartbeat' && (
            <Section label="Response time" aside={<Seg label="Time range" value={hours} onChange={setHours} options={RANGES} />}>
              <TrendChart series={series} height={220} format={ms} markers={failures.map((f) => f.ts)} emptyText="no checks recorded in this range" />
            </Section>
          )}
          {failures.length > 0 && (
            <Section label="Failed checks" aside={<span className="small ink-3 num">{failCount} in range</span>}>
              <ul className="outages">
                {runs
                  .slice(-25)
                  .reverse()
                  .map((o) => (
                    <li key={o.from}>
                      <span className="outage-when num">{span(o.from, o.to)}</span>
                      <span className="outage-why">{o.detail}</span>
                      <span className="outage-count num">{o.fails === 1 ? '1 check' : `${o.fails} checks`}</span>
                    </li>
                  ))}
              </ul>
            </Section>
          )}
        </>
      )}
    </main>
  );
}
