import { useCallback, useMemo, useState } from 'react';
import { Link, useNavigate, useParams } from 'react-router';
import { api } from '../api/client';
import { useServices } from '../api/ServicesContext';
import type { CheckSpec } from '../api/types';
import { MuteButton } from '../components/MuteButton';
import { useToast } from '../components/Toasts';
import { TrendChart } from '../components/TrendChart';
import { Pct, Section, Seg, State, Ticks } from '../components/ui';
import { formatAgo } from '../format';
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
            <Link to="/services">services</Link> /
          </div>
          <h1 className="truncate">{name}</h1>
          {svc && (
            <div className="svc-target truncate">
              {svc.type} · {svc.target}
            </div>
          )}
        </div>
        {svc && (
          <div className="title-actions">
            <State status={svc.status} />
            <button className="btn-quiet" onClick={edit}>
              edit
            </button>
            <button className="btn-quiet" onClick={togglePause}>
              {svc.status === 'paused' ? 'resume' : 'pause'}
            </button>
            <MuteButton subject={`check:${name}`} label={name} />
            {confirmDelete ? (
              <span className="confirm">
                delete with its history?{' '}
                <button className="btn-quiet danger" onClick={remove}>
                  delete
                </button>{' '}
                <button className="btn-quiet" onClick={() => setConfirmDelete(false)}>
                  keep
                </button>
              </span>
            ) : (
              <button className="btn-quiet" onClick={() => setConfirmDelete(true)}>
                delete
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
              <div>
                <span className="label">Response</span>
                <div className="readout-value">{svc.latency_ms !== null ? ms(svc.latency_ms) : '—'}</div>
                <div className="readout-sub">{avg !== null ? `avg ${ms(avg)} · p95 ${ms(p95 ?? 0)}` : 'no data in range'}</div>
              </div>
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
                <div className="readout-sub">{svc.last_change ? `since ${new Date(svc.last_change * 1000).toLocaleString()}` : 'no change recorded'}</div>
              </div>
            </div>
          )}
          {svc && (
            <Section label="Last 60 checks" aside={<span className="small ink-3">oldest → newest</span>}>
              <Ticks beats={svc.recent} slots={60} tall />
            </Section>
          )}
          <Section label="Response time" aside={<Seg label="Time range" value={hours} onChange={setHours} options={RANGES} />}>
            <TrendChart series={series} height={220} format={ms} markers={failures.map((f) => f.ts)} emptyText="no checks recorded in this range" />
          </Section>
          {failures.length > 0 && (
            <Section label="Failed checks" aside={<span className="small ink-3 num">{failCount} in range</span>}>
              <table className="table">
                <tbody>
                  {failures
                    .slice(-25)
                    .reverse()
                    .map((p) => (
                      <tr key={p.ts}>
                        <td className="num ink-3" style={{ whiteSpace: 'nowrap', width: 1 }}>
                          {new Date(p.ts * 1000).toLocaleString()}
                        </td>
                        <td>
                          {(p.fails ?? 1) > 1 && <span className="num ink-3">{p.fails}× </span>}
                          {p.detail || 'down'}
                        </td>
                      </tr>
                    ))}
                </tbody>
              </table>
            </Section>
          )}
        </>
      )}
    </main>
  );
}
