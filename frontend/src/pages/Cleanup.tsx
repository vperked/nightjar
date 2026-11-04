import { useState } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import type { CleanupItem, CleanupView } from '../api/types';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { MediaTabs, Section } from '../components/ui';
import { formatAgo, formatBytes, formatDuration, plural } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';

// The media cleanup: what nobody has watched since Plex added it, when it leaves, and what was
// removed. Marking something Keep spares it for good.

const day = (ts: number) => new Date(ts * 1000).toLocaleDateString(undefined, { weekday: 'short', day: 'numeric', month: 'short' });
const BY: Record<CleanupItem['manager'], string> = { radarr: 'Radarr', sonarr: 'Sonarr', plex: 'Plex' };

function name(it: CleanupItem) {
  return it.year ? `${it.title} (${it.year})` : it.title;
}

function When({ it, now }: { it: CleanupItem; now: number }) {
  switch (it.status) {
    case 'preview':
      return <span className="ink-3">when turned on</span>;
    case 'leaving':
      return <span>{it.leaving_at <= now ? 'next pass' : `${day(it.leaving_at)} · in ${formatDuration(it.leaving_at - now)}`}</span>;
    case 'waiting':
      return <span className="pct-fair">seeding first</span>;
    case 'failed':
      return <span className="pct-poor">could not remove</span>;
    case 'removed':
      return <span className="ink-3">removed {formatAgo(it.removed_at ?? null, now)}</span>;
  }
}

export function CleanupPage() {
  const { data, error, refresh } = usePoll(api.cleanup, 60000);
  const [scanning, setScanning] = useState(false);
  const [view, setView] = useState<CleanupView>();
  const notify = useToast();
  const now = useNow(30000);
  const v = view ?? data;
  const scan = async () => {
    setScanning(true);
    try {
      setView(await api.cleanupScan());
      notify({ tone: 'ok', title: 'Libraries checked' });
    } catch (e) {
      notify({ tone: 'error', title: 'The check failed', body: (e as Error).message });
      refresh();
    } finally {
      setScanning(false);
    }
  };
  const keep = async (key: string, on: boolean, title: string) => {
    try {
      setView(await api.cleanupKeep(key, on));
      notify({ tone: 'ok', title: on ? `${title} is kept` : `${title} can be listed again` });
    } catch (e) {
      notify({ tone: 'error', title: 'Could not change it', body: (e as Error).message });
    }
  };
  const listed = v?.items.filter((i) => i.status !== 'removed') ?? [];
  const removed = v?.items.filter((i) => i.status === 'removed') ?? [];
  const leaving = listed.filter((i) => i.status === 'leaving' || i.status === 'preview');
  const size = (list: CleanupItem[]) => list.reduce((n, i) => n + i.size, 0);
  return (
    <main>
      <div className="page-title">
        <div className="min0">
          <h1>Cleanup</h1>
          <MediaTabs current="/plex/cleanup" />
          {v && (
            <div className="page-sub">
              Never watched {v.days} days after being added → listed for {v.notice_days} days → torrents seeded to {v.ratio.toFixed(1)} (or {v.ratio_days} more days) → removed through{' '}
              {v.managers.length ? `${v.managers.join(' or ')}, else Plex` : 'Plex'}.
            </div>
          )}
        </div>
        <span className="title-actions">
          <Link className="btn-quiet" to="/settings?find=cleanup">
            Settings
          </Link>
          <button className="btn" onClick={scan} disabled={scanning}>
            {scanning ? 'Checking…' : 'Check now'}
          </button>
        </span>
      </div>
      {error && !v && <p className="alert">Could not load the cleanup: {error.message}</p>}
      {!v && !error && <Skeleton kind="list" />}
      {v && (
        <>
          {!v.enabled && (
            <p className="cleanup-off">
              <strong>Off.</strong> This is a preview of what would be listed; nothing is removed. Turn it on under <Link to="/settings?find=cleanup">Settings → Media cleanup</Link>.
            </p>
          )}
          {v.error && <p className="alert">The last check stopped: {v.error}</p>}
          {(v.found ?? []).length > 0 && <p className="form-help cleanup-found">Found on this machine: {v.found!.join('; ')}. Set them under Settings → Media cleanup to use others.</p>}
          <div className="stats-row">
            <div>
              <span className="label">{v.enabled ? 'Leaving soon' : 'Would leave'}</span>
              <div className="readout-value">{leaving.length}</div>
              <div className="readout-sub">{leaving.length ? formatBytes(size(leaving)) : 'nothing unwatched'}</div>
            </div>
            <div>
              <span className="label">Seeding first</span>
              <div className="readout-value">{listed.filter((i) => i.status === 'waiting').length}</div>
              <div className="readout-sub">until ratio {v.ratio.toFixed(1)}</div>
            </div>
            <div>
              <span className="label">Kept</span>
              <div className="readout-value">{v.kept.length}</div>
              <div className="readout-sub">never removed</div>
            </div>
            <div>
              <span className="label">Freed</span>
              <div className="readout-value">{v.freed ? formatBytes(v.freed) : '—'}</div>
              <div className="readout-sub">{v.scanned ? `checked ${formatAgo(v.scanned, now)}` : 'not checked yet'}</div>
            </div>
          </div>
          <Section label={v.enabled ? 'Leaving soon' : 'Would leave'} aside={<span className="small ink-3 num">{plural(listed.length, 'title')}</span>}>
            {listed.length === 0 ? (
              <p className="note" style={{ padding: 0 }}>
                Everything older than {v.days} days has been watched by someone.
              </p>
            ) : (
              <table className="table cleanup-table">
                <thead>
                  <tr>
                    <th>Title</th>
                    <th className="hide-sm">Added</th>
                    <th className="r hide-sm">Size</th>
                    <th className="hide-sm">Torrent</th>
                    <th>Leaves</th>
                    <th className="shrink" />
                  </tr>
                </thead>
                <tbody>
                  {listed.map((it) => (
                    <tr key={it.key}>
                      <td className="name-cell">
                        <span className="svc-name truncate" style={{ display: 'block' }} title={name(it)}>
                          {name(it)}
                        </span>
                        <span className="sub">
                          {it.library} · removed by {BY[it.manager]}
                        </span>
                        {it.reason && <span className="sub cleanup-reason">{it.reason}</span>}
                      </td>
                      <td className="hide-sm ink-3 num">{formatAgo(it.added, now)}</td>
                      <td className="r num hide-sm">{it.size ? formatBytes(it.size) : '—'}</td>
                      <td className="hide-sm num">
                        {it.torrents.length ? (
                          it.torrents.map((t) => (
                            <span key={t.hash} className={`cleanup-ratio ${t.ratio >= v.ratio ? 'done' : ''}`} title={t.name}>
                              ratio {t.ratio.toFixed(2)}
                            </span>
                          ))
                        ) : (
                          <span className="ink-3">none</span>
                        )}
                      </td>
                      <td className="num">
                        <When it={it} now={now} />
                      </td>
                      <td className="shrink">
                        <button className="btn-quiet" onClick={() => keep(it.key, true, it.title)}>
                          Keep
                        </button>
                      </td>
                    </tr>
                  ))}
                </tbody>
              </table>
            )}
          </Section>
          <div className="columns">
            <div className="col">
              <Section label="Removed lately">
                {removed.length === 0 ? (
                  <p className="note" style={{ padding: 0 }}>
                    Nothing yet.
                  </p>
                ) : (
                  <ul className="report-list">
                    {removed.map((it) => (
                      <li key={it.key}>
                        <strong>{name(it)}</strong>
                        <span className="report-meta num">
                          {it.removed_at ? day(it.removed_at) : ''} · {it.size ? formatBytes(it.size) : ''} · by {BY[it.manager]}
                        </span>
                      </li>
                    ))}
                  </ul>
                )}
              </Section>
            </div>
            <div className="col">
              <Section label="Kept">
                {v.kept.length === 0 ? (
                  <p className="note" style={{ padding: 0 }}>
                    Mark something Keep and it is never listed again.
                  </p>
                ) : (
                  <ul className="report-list">
                    {v.kept.map((k) => (
                      <li key={k.key} className="cleanup-kept">
                        <span>{k.title}</span>
                        <button className="btn-ghost" onClick={() => keep(k.key, false, k.title)}>
                          Let it go
                        </button>
                      </li>
                    ))}
                  </ul>
                )}
              </Section>
            </div>
          </div>
        </>
      )}
    </main>
  );
}
