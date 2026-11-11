import { useEffect, useMemo, useState } from 'react';
import { Link, useLocation, useNavigate } from 'react-router';
import { api } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import { useServices } from '../api/ServicesContext';
import type { CheckSpec, Service } from '../api/types';
import { Editable } from '../components/Editable';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { Pct, Seg, State, Ticks } from '../components/ui';
import { formatAgo, formatResponse } from '../format';
import { useNow } from '../hooks/useNow';
import { CheckForm } from './CheckForm';
import { ServicesTabs } from './SystemServices';

type Filter = 'all' | 'up' | 'down';

export function ServiceRow({ s, now, onEdit, server }: { s: Service; now: number; onEdit?: () => void; server?: string }) {
  const navigate = useNavigate();
  const href = `/services/${encodeURIComponent(s.name)}`;
  return (
    // A click on the name's own link is left to the link, so it goes once into the history (and a
    // Ctrl-click opens a tab without moving this one).
    <tr className="link" onClick={(e) => !(e.target as Element).closest('a') && navigate(href)}>
      <td className="shrink hide-sm">
        <State status={s.status} />
      </td>
      <td className="name-cell">
        <Link to={href} className="svc-name truncate" style={{ display: 'block' }}>
          {s.name}
        </Link>
        <span className="svc-target truncate" title={s.detail}>
          {s.type === 'heartbeat' ? `heartbeat · ${s.schedule}` : `${s.type} · ${s.target}`}
          {server && ` · on ${server}`}
        </span>
        {/* On a phone the state sits with the last checks, to leave the name the room. */}
        <div className="show-sm svc-sm">
          <State status={s.status} />
          <Ticks beats={s.recent} slots={30} />
        </div>
      </td>
      <td className="ticks-cell hide-sm">
        <Ticks beats={s.recent} slots={48} />
      </td>
      <td className="r num hide-sm">{formatResponse(s)}</td>
      <td className="r num ink-3 hide-sm">{formatAgo(s.last_check, now)}</td>
      <td className="r">
        {/* A paused check has no figure; it shows the same dash as every other empty cell. */}
        {s.uptime_24h === null ? <span className="ink-3">—</span> : <Pct value={s.uptime_24h} />}
      </td>
      {onEdit && (
        <td className="shrink r">
          <button
            className="icon-btn row-edit"
            aria-label={`Edit ${s.name}`}
            title="Edit"
            onClick={(e) => {
              e.stopPropagation();
              onEdit();
            }}
          >
            <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
              <path d="M4 20h4L19 9l-4-4L4 16v4zM13.5 6.5l4 4" />
            </svg>
          </button>
        </td>
      )}
    </tr>
  );
}

export function ServicesPage() {
  const { data, loading, refresh } = useServices();
  const location = useLocation();
  const navigate = useNavigate();
  const canEdit = useCanEdit();
  // A viewer cannot add or edit checks, so #add opens nothing for them.
  const [addOpen, setAdding] = useState(location.hash === '#add');
  const adding = canEdit && addOpen;
  // A server's page opens the form for a check of something on it (?server=id#add).
  const presetServer = new URLSearchParams(location.search).get('server') ?? undefined;
  const servers = useServerNames();
  const [editing, setEditing] = useState<CheckSpec>();
  // #add opens the form when it is followed while already here too (the Finder's "Add an uptime check").
  useEffect(() => {
    if (location.hash !== '#add') return;
    setEditing(undefined);
    setAdding(true);
  }, [location.hash, location.key]);
  const notify = useToast();
  const [filter, setFilter] = useState<Filter>('all');
  const [query, setQuery] = useState('');
  const now = useNow(5000);
  const list = useMemo(() => data?.services ?? [], [data]);
  const counts = { all: list.length, up: list.filter((s) => s.status === 'up').length, down: list.filter((s) => s.status === 'down').length };
  // edit opens the form on the full definition, which the status list does not carry.
  const edit = async (name: string) => {
    try {
      const spec = (await api.checks()).checks.find((c) => c.name === name);
      if (!spec) throw new Error('This check no longer exists.');
      setAdding(false);
      setEditing(spec);
      document.querySelector('main')?.scrollIntoView?.({ behavior: 'smooth', block: 'start' });
    } catch (err) {
      notify({ tone: 'error', title: 'Could not open the check', body: (err as Error).message });
    }
  };
  const shown = useMemo(() => list.filter((s) => (filter === 'all' || s.status === filter) && (s.name + ' ' + s.target).toLowerCase().includes(query.toLowerCase())), [list, filter, query]);

  return (
    <main>
      <div className="page-title">
        <div>
          <h1>Services</h1>
          <ServicesTabs current="checks" />
        </div>
        <div className="toolbar">
          <Seg<Filter>
            label="Filter by state"
            value={filter}
            onChange={setFilter}
            options={(['all', 'up', 'down'] as Filter[]).map((f) => ({
              value: f,
              label: (
                <>
                  {f[0].toUpperCase() + f.slice(1)}
                  <span className="n">{counts[f]}</span>
                </>
              ),
            }))}
          />
          <input className="search" placeholder="Filter checks…" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Filter services" />
          {canEdit && !adding && list.length > 0 && (
            <button
              className="btn"
              onClick={() => {
                setEditing(undefined);
                setAdding(true);
              }}
            >
              Add check
            </button>
          )}
        </div>
      </div>
      {canEdit && editing && (
        <CheckForm
          key={editing.name}
          initial={editing}
          onCancel={() => setEditing(undefined)}
          onSaved={() => {
            setEditing(undefined);
            refresh();
          }}
        />
      )}
      {adding && (
        <CheckForm
          server={presetServer}
          onCancel={() => setAdding(false)}
          onSaved={(check) => {
            refresh();
            navigate(`/services/${encodeURIComponent(check.name)}`);
          }}
        />
      )}
      {loading && !data ? (
        <Skeleton kind="list" />
      ) : list.length === 0 ? (
        !adding && (
          <div className="empty">
            <strong>No uptime checks yet</strong>
            Watch a website, an open port, a host or a TLS certificate.
            <Editable>
              <div>
                <button className="btn" onClick={() => setAdding(true)}>
                  Add your first check
                </button>
              </div>
            </Editable>
          </div>
        )
      ) : shown.length === 0 ? (
        <p className="note">No services match.</p>
      ) : (
        <div className="section">
          <table className="table">
            <thead>
              <tr>
                <th className="shrink hide-sm">State</th>
                <th>Service</th>
                <th className="hide-sm">Last 48 checks</th>
                <th className="r hide-sm">Resp.</th>
                <th className="r hide-sm">Checked</th>
                <th className="r">24h</th>
                {canEdit && (
                  <th className="shrink">
                    <span className="sr-only">Edit</span>
                  </th>
                )}
              </tr>
            </thead>
            <tbody>
              {shown.map((s) => (
                <ServiceRow key={s.name} s={s} now={now} onEdit={canEdit ? () => edit(s.name) : undefined} server={s.server ? servers[s.server] : undefined} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </main>
  );
}

// useServerNames names the watched servers by id, for the checks that belong to one.
function useServerNames(): Record<string, string> {
  const [names, setNames] = useState<Record<string, string>>({});
  useEffect(() => {
    api.hosts().then(
      (r) => setNames(Object.fromEntries((r.hosts ?? []).map((h) => [h.id, h.name]))),
      () => setNames({}),
    );
  }, []);
  return names;
}
