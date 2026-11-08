import { Fragment, useEffect, useLayoutEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import type { LogEntry, LogLevel, LogsResponse } from '../api/types';
import { Diagnosis } from '../components/Diagnosis';
import { Select } from '../components/Select';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { Seg } from '../components/ui';
import { formatDuration } from '../format';
import { useArrivals } from '../hooks/useArrivals';

// The monitor's own log, live: what it checked, what it changed, and what went wrong.

type Show = 'all' | 'info' | 'warn' | 'error';
const MAX = 3000;

const LEVEL_WORD: Record<LogLevel, string> = { debug: 'debug', info: 'info', warn: 'warn', error: 'error' };

const clock = (ms: number) => {
  const d = new Date(ms);
  return d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', second: '2-digit', hour12: false });
};

// asText is one entry as a plain log line, for copying and downloading.
const asText = (e: LogEntry) =>
  [
    new Date(e.time).toISOString(),
    e.level.toUpperCase().padEnd(5),
    (e.component ?? '').padEnd(11),
    e.message,
    ...(e.attrs ?? []).map((a) => `${a.key}=${/\s/.test(a.value) ? JSON.stringify(a.value) : a.value}`),
  ].join(' ');

// hit marks every place text contains needle (any case), for a search the log was narrowed by.
function hit(text: string, needle: string) {
  if (!needle) return text;
  const lower = text.toLowerCase();
  const n = needle.toLowerCase();
  const out: ReactNode[] = [];
  let from = 0;
  for (let at = lower.indexOf(n); at >= 0; at = lower.indexOf(n, from)) {
    if (at > from) out.push(text.slice(from, at));
    out.push(
      <mark key={at} className="log-hit">
        {text.slice(at, at + n.length)}
      </mark>,
    );
    from = at + n.length;
  }
  if (!out.length) return text;
  if (from < text.length) out.push(text.slice(from));
  return out;
}

// GAP is the quiet spell between two lines that gets a divider saying how long it was.
const GAP = 10 * 60 * 1000;

function LogLine({
  e,
  showDate,
  arrived,
  needle,
  picked,
  focused,
  onPick,
}: {
  e: LogEntry;
  showDate: boolean;
  arrived: boolean;
  needle: string;
  picked: boolean;
  focused: boolean;
  onPick: () => void;
}) {
  // A click picks the line (for its actions), but not one that ends selecting text to copy.
  const click = () => {
    if (window.getSelection?.()?.toString()) return;
    onPick();
  };
  return (
    <div className={`log-line ${e.level}${arrived ? ' arrive' : ''}${picked ? ' picked' : ''}${focused ? ' focused' : ''}`} data-seq={e.seq} onClick={click} aria-expanded={picked}>
      <span className="log-time num" title={new Date(e.time).toLocaleString()}>
        {showDate && <span className="log-date">{new Date(e.time).toLocaleDateString(undefined, { month: 'short', day: 'numeric' })} </span>}
        {clock(e.time)}
      </span>
      <span className="log-level">{LEVEL_WORD[e.level]}</span>
      <span className="log-component truncate">{e.component && hit(e.component, needle)}</span>
      <span className="log-text">
        <span className="log-message">{hit(e.message, needle)}</span>
        {e.attrs?.map((a, i) => (
          <span key={i} className="log-attr">
            <span className="log-key">{hit(a.key, needle)}</span>
            {hit(a.value, needle)}
          </span>
        ))}
      </span>
    </div>
  );
}

export function LogsPage() {
  const [entries, setEntries] = useState<LogEntry[]>();
  const [meta, setMeta] = useState<Omit<LogsResponse, 'entries'>>();
  const [show, setShow] = useState<Show>('all');
  const [component, setComponent] = useState('');
  const [query, setQuery] = useState('');
  const [search, setSearch] = useState('');
  const [follow, setFollow] = useState(true);
  const [error, setError] = useState('');
  const latest = useRef(0);
  const box = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const [scrolledUp, setScrolledUp] = useState(false);
  // The newest line in view when you scrolled up: anything after it is new to you.
  const [readTo, setReadTo] = useState(0);
  // The line picked for its actions, and one to bring into view once the log has loaded.
  const [picked, setPicked] = useState<number>();
  const [focus, setFocus] = useState<number>();
  const notify = useToast();

  useEffect(() => {
    const id = setTimeout(() => setSearch(query.trim()), 250);
    return () => clearTimeout(id);
  }, [query]);

  const filters = useMemo(() => ({ level: show === 'all' ? '' : show, component, q: search }), [show, component, search]);

  // A filter change loads the matching tail afresh; after that, only newer entries are asked for.
  useEffect(() => {
    let live = true;
    let busy = false;
    latest.current = 0;
    setEntries(undefined);
    const load = async () => {
      if (busy) return;
      busy = true;
      const first = latest.current === 0;
      try {
        const r = await api.logs({ ...filters, after: latest.current || undefined });
        if (!live) return;
        const { entries: fresh, ...rest } = r;
        setMeta(rest);
        setError('');
        setEntries((old) => {
          if (first || !old) return fresh;
          const last = old.at(-1)?.seq ?? 0;
          return [...old, ...fresh.filter((e) => e.seq > last)].slice(-MAX);
        });
        latest.current = Math.max(latest.current, r.latest);
      } catch (e) {
        if (live) setError((e as Error).message);
      } finally {
        busy = false;
      }
    };
    load();
    const id = follow ? setInterval(() => document.visibilityState !== 'hidden' && load(), 2000) : 0;
    return () => {
      live = false;
      clearInterval(id);
    };
  }, [filters, follow]);

  // While following, the view stays pinned to the newest line, unless you scrolled up to read.
  useLayoutEffect(() => {
    const el = box.current;
    if (el && follow && atBottom.current && focus === undefined) el.scrollTop = el.scrollHeight;
  }, [entries, follow, focus]);
  // A line asked for in context is brought to the middle of the view once it has loaded.
  useLayoutEffect(() => {
    if (focus === undefined || !entries) return;
    const el = box.current;
    const line = el?.querySelector<HTMLElement>(`[data-seq="${focus}"]`);
    if (!el || !line) return;
    atBottom.current = false;
    setScrolledUp(true);
    setReadTo(entries.at(-1)?.seq ?? 0);
    // Only the log scrolls; the page stays where it is.
    const at = line.getBoundingClientRect().top - el.getBoundingClientRect().top;
    el.scrollTop += at - el.clientHeight / 2 + line.offsetHeight / 2;
    const id = setTimeout(() => setFocus(undefined), 2400);
    return () => clearTimeout(id);
  }, [focus, entries]);
  const onScroll = () => {
    const el = box.current;
    if (!el) return;
    const bottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
    if (!bottom && atBottom.current) setReadTo(entries?.at(-1)?.seq ?? 0);
    atBottom.current = bottom;
    setScrolledUp(!bottom);
  };
  const toBottom = () => {
    atBottom.current = true;
    setScrolledUp(false);
    box.current?.scrollTo({ top: box.current.scrollHeight, behavior: 'smooth' });
  };
  const unread = scrolledUp && follow ? (entries ?? []).filter((e) => e.seq > readTo).length : 0;

  const filtered = !!(search || show !== 'all' || component);
  const copy = async (e: LogEntry) => {
    try {
      await navigator.clipboard.writeText(asText(e));
      notify({ tone: 'ok', title: 'Line copied' });
    } catch {
      notify({ tone: 'error', title: 'Could not copy', body: 'The browser did not allow it. Select the line and copy it instead.' });
    }
  };
  // inContext drops every filter and brings the line to the middle, among those around it.
  const inContext = (e: LogEntry) => {
    setQuery('');
    setSearch('');
    setShow('all');
    setComponent('');
    setPicked(e.seq);
    setFocus(e.seq);
  };

  const counts = useMemo(() => {
    const c = { warn: 0, error: 0 };
    for (const e of entries ?? []) if (e.level === 'warn' || e.level === 'error') c[e.level]++;
    return c;
  }, [entries]);
  // Dates appear only when the view spans more than one day.
  // Lines written while the page is open slide in as they come.
  const arrived = useArrivals(
    (entries ?? []).map((e) => ({ key: String(e.seq), at: e.seq })),
    entries ? JSON.stringify(filters) : '',
  );
  const multiDay = !!entries?.length && new Date(entries[0].time).toDateString() !== new Date(entries[entries.length - 1].time).toDateString();

  const download = () => {
    const blob = new Blob([(entries ?? []).map(asText).join('\n') + '\n'], { type: 'text/plain' });
    const a = document.createElement('a');
    a.href = URL.createObjectURL(blob);
    a.download = `nightjar-${new Date().toISOString().slice(0, 19).replace(/:/g, '')}.log`;
    a.click();
    URL.revokeObjectURL(a.href);
  };

  return (
    <main>
      <div className="page-title">
        <div>
          <h1 className="title-with-badge">
            Monitor log
            {follow && (
              <span className="live-badge" title="New entries appear as they are written">
                <span className="live-dot" aria-hidden="true" />
                Live
              </span>
            )}
          </h1>
          <div className="page-sub">
            What the monitor itself is doing: the last {meta?.kept.toLocaleString() ?? '3,000'} entries.
            {meta && (
              <>
                {' '}
                Level <strong>{meta.level.toLowerCase()}</strong> (<Link to="/settings?find=LOG_LEVEL">change</Link>)
                {meta.file && (
                  <>
                    , also written to <span className="mono">{meta.file}</span>
                  </>
                )}
                .
              </>
            )}
          </div>
        </div>
        <div className="toolbar">
          <label className="check">
            <input type="checkbox" checked={follow} onChange={(e) => setFollow(e.target.checked)} /> Follow
          </label>
          <button className="btn-quiet" onClick={download} disabled={!entries?.length}>
            Download
          </button>
        </div>
      </div>

      {meta?.findings && meta.findings.length > 0 && <Diagnosis findings={meta.findings} label="Problems in this log" />}
      <div className="log-filters">
        <input className="search" type="search" placeholder="Search the log…" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Search the log" />
        <Seg<Show>
          label="Level"
          value={show}
          onChange={setShow}
          options={[
            { value: 'all', label: 'All' },
            { value: 'info', label: 'Info +' },
            { value: 'warn', label: <>Warnings{counts.warn + counts.error > 0 && show !== 'error' && <span className="seg-count warn">{counts.warn + counts.error}</span>}</> },
            { value: 'error', label: <>Errors{counts.error > 0 && <span className="seg-count crit">{counts.error}</span>}</> },
          ]}
        />
        <Select
          className="short"
          label="Component"
          value={component}
          onChange={setComponent}
          options={[{ value: '', label: 'Everything', text: 'everything' }, ...(meta?.components ?? []).map((c) => ({ value: c, label: c }))]}
        />
      </div>

      {error && <div className="alert">{error}</div>}
      {!entries ? (
        <Skeleton kind="list" />
      ) : (
        <div className="log-panel">
          <div className="log-scroll" ref={box} onScroll={onScroll} role="log" aria-live="off" aria-label="Monitor log">
            {entries.length === 0 ? (
              <div className="log-empty">{search || show !== 'all' || component ? 'Nothing in the log matches.' : 'Nothing logged yet.'}</div>
            ) : (
              entries.map((e, i) => {
                const gap = i > 0 ? e.time - entries[i - 1].time : 0;
                return (
                  <Fragment key={e.seq}>
                    {gap >= GAP && (
                      <div className="log-gap" role="separator">
                        <span>{formatDuration(gap / 1000)} later</span>
                      </div>
                    )}
                    <LogLine
                      e={e}
                      showDate={multiDay}
                      arrived={arrived.has(String(e.seq))}
                      needle={search}
                      picked={picked === e.seq}
                      focused={focus === e.seq}
                      onPick={() => setPicked(picked === e.seq ? undefined : e.seq)}
                    />
                    {picked === e.seq && (
                      <div className="log-actions">
                        <button type="button" className="btn-quiet" onClick={() => copy(e)}>
                          Copy line
                        </button>
                        {filtered && (
                          <button type="button" className="btn-quiet" onClick={() => inContext(e)}>
                            Show in context
                          </button>
                        )}
                        {e.component && e.component !== component && (
                          <button type="button" className="btn-quiet" onClick={() => setComponent(e.component!)}>
                            Only {e.component}
                          </button>
                        )}
                        <span className="log-actions-when num">{new Date(e.time).toLocaleString()}</span>
                      </div>
                    )}
                  </Fragment>
                );
              })
            )}
          </div>
          <div className="log-foot">
            <span className="num">
              {entries.length.toLocaleString()} {entries.length === 1 ? 'entry' : 'entries'}
            </span>
            <span className="log-foot-hint">Click a line to copy it{filtered ? ' or see it in context' : ''}.</span>
          </div>
          {follow && scrolledUp && (
            <button type="button" className={`log-newest ${unread ? 'has-new' : ''}`} onClick={toBottom}>
              {unread ? `${unread.toLocaleString()} new ${unread === 1 ? 'line' : 'lines'}` : 'Jump to newest'} ↓
            </button>
          )}
        </div>
      )}
    </main>
  );
}
