import { useEffect, useMemo, useState } from 'react';
import { Link, useLocation, useSearchParams } from 'react-router';
import { api } from '../api/client';
import type { QBState } from '../api/types';
import { Num } from '../components/Num';
import { Skeleton } from '../components/Skeleton';
import { Legend, TrendChart } from '../components/TrendChart';
import { MediaTabs, Section, Seg, State } from '../components/ui';
import { formatAgo, formatBytes, formatRate } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';
import { AddTorrents } from './qbittorrent/AddTorrents';
import { ORDER, group, type Group } from './qbittorrent/states';
import { Actions, TorrentRow, useActions } from './qbittorrent/Torrents';

type Filter = 'all' | Group;
const FILTERS: Filter[] = ['all', 'downloading', 'stalled', 'seeding', 'paused'];
const mbs = (v: number) => (v === 0 ? '0' : v.toFixed(v < 10 ? 1 : 0));

function Body({ q, refresh }: { q: QBState; refresh: () => void }) {
  const now = useNow(5000);
  const actions = useActions(refresh);
  const [selected, setSelected] = useState<Set<string>>(new Set());
  const [open, setOpen] = useState<string>();
  const [params] = useSearchParams();
  const location = useLocation();
  const add = params.get('add') === '1';
  const [adding, setAdding] = useState(add);
  // ?add=1 opens the form when it is followed while already here too (the Finder's "Add a torrent").
  useEffect(() => {
    if (add) setAdding(true);
  }, [add, location.key]);
  const [filter, setFilter] = useState<Filter>('all');
  const [query, setQuery] = useState('');
  const counts = useMemo(() => {
    const c: Record<Filter, number> = { all: q.torrents.length, downloading: 0, stalled: 0, seeding: 0, paused: 0, error: 0, other: 0 };
    for (const t of q.torrents) c[group(t.state)]++;
    return c;
  }, [q.torrents]);
  const shown = useMemo(() => {
    const needle = query.toLowerCase();
    return q.torrents
      .filter((t) => (filter === 'all' || group(t.state) === filter) && (t.name + ' ' + t.category).toLowerCase().includes(needle))
      .sort((a, b) => ORDER.indexOf(group(a.state)) - ORDER.indexOf(group(b.state)) || b.dlspeed + b.upspeed - (a.dlspeed + a.upspeed) || a.name.localeCompare(b.name));
  }, [q.torrents, filter, query]);
  const speeds = useMemo(
    () => [
      { label: 'Download', color: 'var(--series-1)', points: q.history.map((s) => ({ t: s.ts, v: s.dl / 1048576 })) },
      { label: 'Upload', color: 'var(--series-2)', points: q.history.map((s) => ({ t: s.ts, v: s.up / 1048576 })) },
    ],
    [q.history],
  );
  // Selections only hold torrents that still exist and are shown.
  const picked = shown.filter((t) => selected.has(t.hash));
  const allPicked = shown.length > 0 && picked.length === shown.length;
  const select = (hash: string, on: boolean) =>
    setSelected((s) => {
      const next = new Set(s);
      if (on) next.add(hash);
      else next.delete(hash);
      return next;
    });
  const tr = q.transfer;
  const limit = (n: number) => (n > 0 ? `limit ${formatRate(n)}` : '');

  return (
    <>
      <div className="page-title">
        <div className="min0">
          <h1>qBittorrent</h1>
          <MediaTabs current="/qbittorrent" />
          <div className="svc-target truncate">
            {q.version ? `${q.version} · ` : ''}
            {q.url}
          </div>
        </div>
        <State status={q.updated === 0 ? 'unknown' : q.connected ? 'up' : 'down'} />
      </div>

      {!q.connected && q.updated > 0 && (
        <div className="alert" role="alert">
          qBittorrent is unreachable: {q.error}. {q.torrents.length ? 'Showing the last list it sent.' : ''}
        </div>
      )}

      <div className="stats-row">
        <div>
          <span className="label">Download</span>
          <div className="readout-value">{tr ? <Num value={tr.dl_info_speed} format={formatRate} /> : '—'}</div>
          <div className="readout-sub num">{tr ? [`${formatBytes(tr.dl_info_data)} this session`, limit(tr.dl_rate_limit)].filter(Boolean).join(' · ') : ''}</div>
        </div>
        <div>
          <span className="label">Upload</span>
          <div className="readout-value">{tr ? <Num value={tr.up_info_speed} format={formatRate} /> : '—'}</div>
          <div className="readout-sub num">{tr ? [`${formatBytes(tr.up_info_data)} this session`, limit(tr.up_rate_limit)].filter(Boolean).join(' · ') : ''}</div>
        </div>
        <div>
          <span className="label">Torrents</span>
          <div className="readout-value">{q.torrents.length}</div>
          <div className="readout-sub num">
            {counts.downloading} downloading · {counts.seeding} seeding
          </div>
        </div>
        <div>
          <span className="label">Needs attention</span>
          <div className={`readout-value ${counts.stalled + counts.error ? 'warn-ink' : ''}`}>{counts.stalled + counts.error}</div>
          <div className="readout-sub num">
            {counts.stalled} stalled · {counts.error} with errors
          </div>
        </div>
      </div>

      <Section label="Speed" aside={<Legend series={speeds} />}>
        <TrendChart series={speeds} height={150} format={mbs} tip={(v) => formatRate(v * 1048576)} emptyText="collecting data…" />
        <div className="chart-note small ink-3">
          MB/s · {tr ? `${tr.connection_status}, ${tr.dht_nodes} DHT nodes · ` : ''}updated {formatAgo(q.updated || null, now)}
        </div>
      </Section>

      <Section
        label="Torrents"
        aside={
          <div className="toolbar">
            <Seg<Filter>
              label="Filter torrents"
              value={filter}
              onChange={setFilter}
              options={FILTERS.map((f) => ({
                value: f,
                label: (
                  <>
                    {f[0].toUpperCase() + f.slice(1)}
                    <span className="n">{counts[f]}</span>
                  </>
                ),
              }))}
            />
            <input className="search" placeholder="Search torrents…" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Search torrents" />
            {q.connected && !adding && (
              <button className="btn" onClick={() => setAdding(true)}>
                Add torrent
              </button>
            )}
          </div>
        }
      >
        {adding && (
          <AddTorrents
            onClose={() => setAdding(false)}
            onAdded={() => {
              setAdding(false);
              refresh();
            }}
          />
        )}
        {picked.length > 0 && (
          <div className="bulk-bar" role="toolbar" aria-label="Selected torrents">
            <span className="bulk-bar-count">
              <strong>{picked.length}</strong> selected
            </span>
            <span className="bulk-bar-rule" aria-hidden="true" />
            <Actions key={picked.map((t) => t.hash).join()} torrents={picked} run={actions.run} busy={actions.busy} onDone={() => setSelected(new Set())} />
            <span className="bulk-bar-rule" aria-hidden="true" />
            <button className="bulk-bar-close" aria-label="Clear selection" title="Clear selection" onClick={() => setSelected(new Set())}>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
                <path d="M6 6l12 12M18 6L6 18" />
              </svg>
            </button>
          </div>
        )}
        {q.torrents.length === 0 ? (
          <p className="note" style={{ padding: '4px 0 0' }}>
            {q.updated ? 'No torrents.' : 'Waiting for the first list from qBittorrent…'}
          </p>
        ) : shown.length === 0 ? (
          <p className="note" style={{ padding: '4px 0 0' }}>
            No torrents match.
          </p>
        ) : (
          <table className="table qb-table">
            <thead>
              <tr>
                <th className="shrink qb-check">
                  <input
                    type="checkbox"
                    className="select-box"
                    aria-label="Select all shown"
                    checked={allPicked}
                    ref={(el) => {
                      if (el) el.indeterminate = picked.length > 0 && !allPicked;
                    }}
                    onChange={(e) => setSelected(e.target.checked ? new Set(shown.map((t) => t.hash)) : new Set())}
                  />
                </th>
                <th>Name</th>
                <th className="shrink hide-sm">State</th>
                <th className="hide-sm">Progress</th>
                <th className="r hide-sm">Speed</th>
                <th className="r hide-sm">Ratio</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((t) => (
                <TorrentRow
                  key={t.hash}
                  t={t}
                  selected={selected.has(t.hash)}
                  onSelect={(on) => select(t.hash, on)}
                  open={open === t.hash}
                  onToggle={() => setOpen((o) => (o === t.hash ? undefined : t.hash))}
                  actions={actions}
                />
              ))}
            </tbody>
          </table>
        )}
      </Section>
    </>
  );
}

export function QBittorrentPage() {
  const qb = usePoll(api.qbittorrent, 5000);
  return (
    <main>
      {qb.error && !qb.unauthorized && (
        <div className="alert" role="alert">
          Can't reach the monitor ({qb.error.message}). Retrying.
        </div>
      )}
      {!qb.data ? (
        <Skeleton kind="dashboard" />
      ) : qb.data.enabled && qb.data.qbittorrent ? (
        <Body q={qb.data.qbittorrent} refresh={qb.refresh} />
      ) : (
        <p className="note">
          qBittorrent monitoring is off. Turn on <strong>Watch qBittorrent</strong> in <Link to="/settings#qbittorrent">Settings → qBittorrent</Link>.
        </p>
      )}
    </main>
  );
}
