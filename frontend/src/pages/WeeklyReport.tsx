import { useState } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import { Editable } from '../components/Editable';
import { PlexStatsBody } from '../components/PlexStats';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { Section } from '../components/ui';
import { formatDuration, plural } from '../format';
import { usePoll } from '../hooks/usePoll';
import { IncidentsTabs } from './Incidents';

// The weekly report: the last seven days as the Monday message to Discord sums them up.

const day = (ts: number) => new Date(ts * 1000).toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' });

export function WeeklyReportPage() {
  const { data, error } = usePoll(api.report, 60000);
  const [sending, setSending] = useState(false);
  const notify = useToast();
  const send = async () => {
    setSending(true);
    try {
      await api.sendReport();
      notify({ tone: 'ok', title: 'Report sent', body: 'It is in Discord, and wherever system alerts go.' });
    } catch (e) {
      notify({ tone: 'error', title: 'Could not send the report', body: (e as Error).message });
    } finally {
      setSending(false);
    }
  };
  const r = data?.report;
  return (
    <main>
      <div className="page-title">
        <div className="min0">
          <h1>Weekly report</h1>
          <div className="page-sub">
            {!data
              ? ' '
              : data.enabled
                ? `Sent every ${data.at}${data.next ? `; next on ${day(data.next)}` : ''}. This is the last seven days.`
                : 'Off. Turn it on under Settings → Dashboard. This is the last seven days.'}
          </div>
          <IncidentsTabs current="/incidents/report" />
        </div>
        <span className="title-actions">
          <Link className="btn-quiet" to="/settings?find=weekly">
            Settings
          </Link>
          <Editable>
            <button className="btn" onClick={send} disabled={sending || !r}>
              {sending ? 'Sending…' : 'Send it now'}
            </button>
          </Editable>
        </span>
      </div>
      {error && !data && <p className="alert">Could not load the report: {error.message}</p>}
      {!r && !error && <Skeleton kind="list" />}
      {r && (
        <>
          <div className="stats-row">
            <div>
              <span className="label">Uptime</span>
              <div className="readout-value">{r.overall !== null ? `${r.overall.toFixed(2)}%` : '—'}</div>
              <div className="readout-sub">across {plural(r.checks.length, 'check')}</div>
            </div>
            <div>
              <span className="label">Incidents</span>
              <div className="readout-value">{r.incidents}</div>
              <div className="readout-sub">{r.incidents ? `${formatDuration(r.down)} in trouble` : 'nothing went wrong'}</div>
            </div>
            <div>
              <span className="label">Nightjar repaired</span>
              <div className="readout-value">{r.fixed}</div>
              <div className="readout-sub">{r.repairs ? `of ${plural(r.repairs, 'attempt')}` : 'no repairs needed'}</div>
            </div>
            <div>
              <span className="label">Alerts</span>
              <div className="readout-value">{r.alerts}</div>
              <div className="readout-sub">{r.noisiest[0] ? `most from ${r.noisiest[0].name}` : 'warnings and criticals'}</div>
            </div>
          </div>
          {r.plex && (
            <Section label="Plex this week">
              <PlexStatsBody st={r.plex} />
            </Section>
          )}
          <div className="columns">
            <div className="col">
              <Section label="Longest incidents">
                {r.top.length === 0 ? (
                  <p className="note" style={{ padding: 0 }}>
                    None this week.
                  </p>
                ) : (
                  <ul className="report-list">
                    {r.top.map((i) => (
                      <li key={i.id}>
                        <strong>{i.title}</strong>
                        <span className="report-meta num">
                          {day(i.started)} · {formatDuration(i.length)}
                          {i.open ? ' · still open' : ''}
                        </span>
                        {i.cause && <span className="report-cause">{i.cause}.</span>}
                      </li>
                    ))}
                  </ul>
                )}
                <Link className="report-more" to="/incidents">
                  All incidents →
                </Link>
              </Section>
              <Section label="Coming up">
                {r.forecasts.length === 0 ? (
                  <p className="note" style={{ padding: 0 }}>
                    Nothing to warn about: no disk filling fast, no certificate expiring soon, no heartbeat gone quiet.
                  </p>
                ) : (
                  <ul className="report-list">
                    {r.forecasts.map((f) => (
                      <li key={f}>{f}.</li>
                    ))}
                  </ul>
                )}
              </Section>
            </div>
            <div className="col">
              <Section label="Lowest uptime">
                <table className="table">
                  <tbody>
                    {r.checks.slice(0, 8).map((c) => (
                      <tr key={c.name}>
                        <td>{c.name}</td>
                        <td className="r num">{c.uptime !== null ? `${c.uptime.toFixed(2)}%` : '—'}</td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </Section>
              {r.causes.length > 0 && (
                <Section label="Causes">
                  <ul className="report-list">
                    {r.causes.map((c) => (
                      <li key={c.name}>
                        {c.name} <span className="report-meta num">×{c.n}</span>
                      </li>
                    ))}
                  </ul>
                </Section>
              )}
            </div>
          </div>
        </>
      )}
    </main>
  );
}
