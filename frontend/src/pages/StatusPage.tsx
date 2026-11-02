import { useEffect } from 'react';
import { api } from '../api/client';
import type { PublicService, PublicStatus } from '../api/types';
import { DemoBanner, Pct, State, Ticks } from '../components/ui';
import { plural } from '../format';
import { usePoll } from '../hooks/usePoll';

const VERDICT = {
  operational: 'All systems operational',
  degraded: 'Partial outage',
  outage: 'Major outage',
} as const;

function Row({ s, data }: { s: PublicService; data: PublicStatus }) {
  const uptime = data.show_uptime !== false;
  const history = data.show_history !== false && s.recent.length > 0;
  return (
    <div className="status-row">
      <div className="status-row-head">
        <span className="svc-name">{s.name}</span>
        <span style={{ display: 'flex', gap: 16, alignItems: 'baseline' }}>
          {uptime && (
            <span className="small ink-3">
              <Pct value={s.uptime_30d} /> 30d
            </span>
          )}
          <State status={s.status} />
        </span>
      </div>
      {history && (
        <>
          <Ticks beats={s.recent} slots={60} tall />
          <div className="status-scale">
            <span>older</span>
            <span>now</span>
          </div>
        </>
      )}
    </div>
  );
}

// StatusView draws a status page from its data; the designer previews with it too.
export function StatusView({ data }: { data: PublicStatus }) {
  const groups = data.groups ?? [{ name: '', services: data.services }];
  return (
    <main className="status-page">
      {data.announcement && (
        <div className={`status-announcement ${data.announcement.tone}`} role="note">
          {data.announcement.text}
        </div>
      )}
      <h1>{data.title}</h1>
      {data.description && <p className="status-description">{data.description}</p>}
      <div className={`verdict ${data.overall}`} role="status">
        <strong>{VERDICT[data.overall]}</strong>
        <span className="small ink-3 num">updated {new Date(data.updated * 1000).toLocaleTimeString()}</span>
      </div>
      {groups.map((g, i) =>
        g.services.length ? (
          <section key={g.name + i} className="status-group">
            {g.name && <h2>{g.name}</h2>}
            <div className="status-list">
              {g.services.map((s) => (
                <Row key={s.name} s={s} data={data} />
              ))}
            </div>
          </section>
        ) : null,
      )}
      {data.services.length === 0 && <p className="note">No services are listed yet.</p>}
      <div className="footer">
        <span>{data.footer || 'server monitor'}</span>
        <span>{plural(data.services.length, 'service')}</span>
      </div>
    </main>
  );
}

// StatusPage is public: it shows only what its design lists, never targets or error details.
export function StatusPage() {
  const status = usePoll(api.status, 30000);
  useEffect(() => {
    if (status.data) document.title = status.data.title;
  }, [status.data]);

  if (status.error && !status.data) {
    return (
      <main className="status-page">
        <h1>Status</h1>
        <p className="note">The status page is not available.</p>
      </main>
    );
  }
  if (!status.data) return <main className="status-page loading">loading…</main>;
  return (
    <>
      <DemoBanner />
      <StatusView data={status.data} />
    </>
  );
}
