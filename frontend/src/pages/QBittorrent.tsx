import { useEffect, useMemo, useState } from 'react';
import { Link, useLocation, useSearchParams } from 'react-router';
import { api } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import type { QBState, QBTorrent } from '../api/types';
import { Select } from '../components/Select';
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
import { useArrivals } from '../hooks/useArrivals';

type Filter = 'all' | Group;
// Errors covers torrents in error and with missing files, the other half of "Needs attention".
const FILTERS: Filter[] = ['all', 'downloading', 'stalled', 'seeding', 'paused', 'error'];
const FILTER_WORD: Partial<Record<Filter, string>> = { error: 'Errors' };
const mbs = (v: number) => (v < 10 ? String(Number(v.toFixed(1))) : v.toFixed(0)); // "5", not "5.0", beside "10"

// The list's order: by state (the page's own, what needs attention first) or by a column.
type SortKey = 'state' | 'name' | 'progress' | 'speed' | 'ratio';
interface Sort {
  key: SortKey;
  desc: boolean;
}
const DEFAULT_SORT: Sort = { key: 'state', desc: false };
const SORT_STORE = 'nightjar.qbittorrent.sort';
const compare: Record<SortKey, (a: QBTorrent, b: QBTorrent) => number> = {
  state: (a, b) => ORDER.indexOf(group(a.state)) - ORDER.indexOf(group(b.state)) || b.dlspeed + b.upspeed - (a.dlspeed + a.upspeed),
  name: (a, b) => a.name.localeCompare(b.name),
  progress: (a, b) => a.progress - b.progress,
  speed: (a, b) => a.dlspeed + a.upspeed - (b.dlspeed + b.upspeed),
  ratio: (a, b) => a.ratio - b.ratio,
};
// A figure's first click puts the biggest first; names go A to Z.
const startsDesc = (k: SortKey) => k === 'progress' || k === 'speed' || k === 'ratio';

// The order is remembered in this browser, so the list comes back sorted the way it was left.
function savedSort(): Sort {
  try {
    const s = JSON.parse(localStorage.getItem(SORT_STORE) ?? 'null') as Sort | null;
    if (s && s.key in compare) return { key: s.key, desc: !!s.desc };
  } catch {
    /* storage unavailable or unreadable */
  }
  return DEFAULT_SORT;
}

// Phones hide the columns, so their order is chosen from a menu instead of the headers.
const PHONE_SORTS: { value: string; label: string; sort: Sort }[] = [
  { value: 'state', label: 'By state', sort: DEFAULT_SORT },
  { value: 'ratio-desc', label: 'Ratio, highest first', sort: { key: 'ratio', desc: true } },
  { value: 'ratio-asc', label: 'Ratio, lowest first', sort: { key: 'ratio', desc: false } },
  { value: 'speed-desc', label: 'Fastest first', sort: { key: 'speed', desc: true } },
  { value: 'progress-asc', label: 'Least done first', sort: { key: 'progress', desc: false } },
  { value: 'name-asc', label: 'Name, A to Z', sort: { key: 'name', desc: false } },
  { value: 'name-desc', label: 'Name, Z to A', sort: { key: 'name', desc: true } },
];

function SortHeader({ k, label, sort, onSort, className = '' }: { k: SortKey; label: string; sort: Sort; onSort: (k: SortKey) => void; className?: string }) {
  const on = sort.key === k;
  return (
    <th className={className} aria-sort={on ? (sort.desc ? 'descending' : 'ascending') : undefined}>
      <button type="button" className={`th-sort${on ? ' on' : ''}`} onClick={() => onSort(k)}>
        {label}
        <span className="th-arrow" aria-hidden="true">
          {on ? (sort.desc ? '↓' : '↑') : ''}
        </span>
      </button>
    </th>
  );
}

function Body({ q, refresh }: { q: QBState; refresh: () => void }) {
  const now = useNow(5000);
  const actions = useActions(refresh);
  // A viewer gets no add form and no selection, since every selection action changes something.
  const canEdit = useCanEdit();
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
  const [sort, setSortState] = useState<Sort>(savedSort);
  const setSort = (s: Sort) => {
    setSortState(s);
    try {
      localStorage.setItem(SORT_STORE, JSON.stringify(s));
    } catch {
      /* storage unavailable: the order lasts until the page is left */
    }
  };
  // Clicking the column already sorted by turns its order round; another column starts its own way.
  const sortBy = (k: SortKey) => setSort(sort.key === k ? { key: k, desc: !sort.desc } : { key: k, desc: startsDesc(k) });
  const phoneSort = PHONE_SORTS.find((o) => o.sort.key === sort.key && o.sort.desc === sort.desc)?.value ?? 'state';
  const counts = useMemo(() => {
    const c: Record<Filter, number> = { all: q.torrents.length, downloading: 0, stalled: 0, seeding: 0, paused: 0, error: 0, other: 0 };
    for (const t of q.torrents) c[group(t.state)]++;
    return c;
  }, [q.torrents]);
  const shown = useMemo(() => {
    const needle = query.toLowerCase();
    return q.torrents
      .filter((t) => (filter === 'all' || group(t.state) === filter) && (t.name + ' ' + t.category).toLowerCase().includes(needle))
      .sort((a, b) => (sort.desc ? -1 : 1) * compare[sort.key](a, b) || a.name.localeCompare(b.name));
  }, [q.torrents, filter, query, sort]);
  // A torrent added while the page is open (here or elsewhere) slides in.
  const arrived = useArrivals(
    shown.map((t) => ({ key: t.hash, at: t.added_on })),
    `${filter}\u0000${query}`,
  );
  const speeds = useMemo(
    () => [
      { label: 'Download', color: 'var(--series-1)', points: q.history.map((s) => ({ t: s.ts, v: s.dl / 1048576 })) },
      { label: 'Upload', color: 'var(--series-2)', points: q.history.map((s) => ({ t: s.ts, v: s.up / 1048576 })) },
    ],
    [q.history],
  );
  // Selections only hold torrents that still exist and are shown. One hidden by a filter or search is
  // let go, so it does not come back selected when the filter is cleared.
  const picked = shown.filter((t) => selected.has(t.hash));
  useEffect(() => {
    setSelected((s) => {
      if (!s.size) return s;
      const visible = new Set(shown.map((t) => t.hash));
      const kept = new Set([...s].filter((h) => visible.has(h)));
      return kept.size === s.size ? s : kept;
    });
  }, [shown]);
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
          <div className="svc-target truncate">
            {q.version ? `${q.version} · ` : ''}
            {q.url}
          </div>
          <MediaTabs current="/qbittorrent" />
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
                    {FILTER_WORD[f] ?? f[0].toUpperCase() + f.slice(1)}
                    <span className="n">{counts[f]}</span>
                  </>
                ),
              }))}
            />
            <input className="search" placeholder="Search torrents…" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Search torrents" />
            <Select
              className="show-sm qb-sort"
              label="Sort torrents"
              value={phoneSort}
              options={PHONE_SORTS.map((o) => ({ value: o.value, label: o.label }))}
              onChange={(v) => setSort(PHONE_SORTS.find((o) => o.value === v)?.sort ?? DEFAULT_SORT)}
            />
            {canEdit && q.connected && !adding && (
              <button className="btn" onClick={() => setAdding(true)}>
                Add torrent
              </button>
            )}
          </div>
        }
      >
        {canEdit && adding && (
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
                {canEdit && (
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
                )}
                <SortHeader k="name" label="Name" sort={sort} onSort={sortBy} />
                <SortHeader k="state" label="State" sort={sort} onSort={sortBy} className="shrink hide-sm" />
                <SortHeader k="progress" label="Progress" sort={sort} onSort={sortBy} className="hide-sm" />
                <SortHeader k="speed" label="Speed" sort={sort} onSort={sortBy} className="r hide-sm" />
                <SortHeader k="ratio" label="Ratio" sort={sort} onSort={sortBy} className="r hide-sm" />
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
                  arrived={arrived.has(t.hash)}
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
