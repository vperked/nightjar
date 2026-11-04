import { Link } from 'react-router';
import { api } from '../api/client';
import type { RemoteHost } from '../api/types';
import { usePoll } from '../hooks/usePoll';
import { Section } from './ui';

export const HOST_STATUS: Record<RemoteHost['status'], [string, string]> = { online: ['good', 'Online'], offline: ['crit', 'Offline'], waiting: ['idle', 'Connecting'] };

// HostStatus is a server's state as a mark and a word.
export function HostStatus({ h }: { h: RemoteHost }) {
  const [tone, word] = HOST_STATUS[h.status];
  return <span className={`state ${tone}`}>{word}</span>;
}

// RemoteSummary is the overview's card of other servers, one line each; nothing without any.
export function RemoteSummary() {
  const { data } = usePoll(api.hosts, 10000);
  if (!data || data.hosts.length === 0) return null;
  return (
    <Section label="Other servers" aside={<Link to="/servers">all servers →</Link>}>
      <table className="table remote-table">
        <thead>
          <tr>
            <th>Server</th>
            <th className="shrink" />
            <th className="r shrink">CPU</th>
            <th className="r shrink">Temp</th>
            <th className="r shrink">Mem</th>
          </tr>
        </thead>
        <tbody>
          {data.hosts.map((h) => {
            const r = h.readings;
            return (
              <tr key={h.id} className={h.status === 'offline' ? 'stale' : ''}>
                <td className="min0">
                  <Link to={`/servers/${h.id}`} className="truncate">
                    {h.name}
                  </Link>
                </td>
                <td className="shrink">
                  <HostStatus h={h} />
                </td>
                <td className="r num small shrink" title="CPU">
                  {r ? `${Math.round(r.cpu.percent)}%` : '—'}
                </td>
                <td className={`r num small shrink ${h.problems.some((p) => p.startsWith('CPU at')) ? 'pct-poor' : ''}`} title="CPU temperature">
                  {r?.temperature != null ? `${Math.round(r.temperature)}°C` : '—'}
                </td>
                <td className="r num small shrink" title="Memory">
                  {r ? `${Math.round(r.memory.percent)}%` : '—'}
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </Section>
  );
}
