import { useEffect, useMemo, useRef, useState } from 'react';
import { Link, useSearchParams } from 'react-router';
import { ApiError, api, UnauthorizedError, waitForRestart } from '../api/client';
import type { Finding, Unit, UnitAction, UnitsResponse } from '../api/types';
import { CauseLine, Diagnosis } from '../components/Diagnosis';
import { MuteButton, MutedTag } from '../components/MuteButton';
import { Pager, usePaged } from '../components/Pager';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { Seg, SubTabs } from '../components/ui';
import { isProblem, verdict } from '../components/unitState';
import { formatAgo } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';

type Filter = 'all' | 'problems' | 'running';
const short = (name: string) => name.replace(/\.service$/, '');

// Restarting one of these can cut off the way in (ssh, the network) or take much else down with it
// (every container), so the Restart button asks first, as it does for the monitor itself.
const CAREFUL = /^(ssh|sshd|docker|containerd|networking|NetworkManager|systemd-[^.]+|dbus|dbus-broker)\.service$/;

// selfRestartCut tells an answer the monitor's own restart cut short from a real refusal: no answer
// in time, the systemctl it ran killed with the rest of the monitor, or a proxy's bad gateway.
const selfRestartCut = (message: string) => message.startsWith('No answer') || /signal: (terminated|killed)/.test(message) || /: HTTP 50[234]$/.test(message);

// ServicesTabs switches between uptime checks and systemd services under the Services tab.
export function ServicesTabs({ current }: { current: 'checks' | 'system' }) {
  return (
    <SubTabs
      label="Service views"
      current={current === 'checks' ? '/services' : '/system'}
      items={[
        { to: '/services', label: 'Uptime checks' },
        { to: '/system', label: 'System services' },
      ]}
    />
  );
}

function Logs({ name }: { name: string }) {
  const [lines, setLines] = useState<string[]>();
  const [findings, setFindings] = useState<Finding[]>([]);
  const [error, setError] = useState('');
  const load = () =>
    api.unitLogs(name).then(
      (r) => {
        setLines(r.lines);
        setFindings(r.findings ?? []);
        setError('');
      },
      (e: Error) => setError(e.message),
    );
  useEffect(() => {
    load();
  }, [name]); // load is recreated each render but only depends on name
  return (
    <div className="unit-logs">
      <Diagnosis findings={findings} />
      <div className="unit-logs-head">
        <span className="label">Recent log</span>
        <button className="btn-quiet" onClick={load}>
          Refresh
        </button>
      </div>
      {error ? (
        <p className="form-error">{error}</p>
      ) : !lines ? (
        <p className="unit-log-empty">loading…</p>
      ) : lines.length === 0 ? (
        <p className="unit-log-empty">Nothing in the journal for this service.</p>
      ) : (
        <pre className="unit-log mono">{lines.join('\n')}</pre>
      )}
    </div>
  );
}

function UnitRow({ u, self, open, focus, onToggle, onChanged, now }: { u: Unit; self?: string; open: boolean; focus?: boolean; onToggle: () => void; onChanged: () => void; now: number }) {
  const row = useRef<HTMLLIElement>(null);
  // Arriving from a link to this service brings it into view.
  useEffect(() => {
    if (focus) row.current?.scrollIntoView?.({ block: 'center' });
  }, [focus]);
  const notify = useToast();
  const [busy, setBusy] = useState<UnitAction>();
  const [confirmStop, setConfirmStop] = useState(false);
  const [confirmRestart, setConfirmRestart] = useState(false);
  const [tone, word] = verdict(u);
  const isSelf = u.name === self;
  const act = async (action: UnitAction) => {
    setBusy(action);
    setConfirmStop(false);
    setConfirmRestart(false);
    // Restarting the monitor ends the request with it: no answer, or none in time, means it is
    // restarting, so wait for it to come back as the settings do. So does an answer the restart
    // itself cut short: the systemctl it ran killed along with the monitor, or a proxy in front
    // saying the monitor went away. Any other answer is a refusal to show.
    if (isSelf && action === 'restart') {
      try {
        await api.controlUnit(u.name, action);
      } catch (err) {
        if (err instanceof UnauthorizedError || (err instanceof ApiError && !selfRestartCut(err.message))) {
          notify({ tone: 'error', title: `Could not restart ${short(u.name)}`, body: err.message });
          setBusy(undefined);
          return;
        }
      }
      notify({ tone: 'info', title: `Restarting ${short(u.name)}`, body: 'The page reloads when it is back.' });
      await waitForRestart();
      return;
    }
    try {
      const { unit } = await api.controlUnit(u.name, action);
      const [, state] = verdict(unit);
      notify({
        tone: unit.active === 'failed' ? 'error' : 'ok',
        title: `${short(u.name)}: ${action === 'restart' ? 'restarted' : action === 'start' ? 'started' : 'stopped'}`,
        body: `Now ${state.toLowerCase()}.`,
      });
      onChanged();
    } catch (err) {
      notify({ tone: 'error', title: `Could not ${action} ${short(u.name)}`, body: err instanceof ApiError ? err.message : String(err) });
    } finally {
      setBusy(undefined);
    }
  };
  const running = u.active === 'active' || u.active === 'activating' || u.active === 'reloading';
  const why = u.active === 'failed' && u.result && u.result !== 'success' ? `${u.result}${u.exit_status ? ` (exit ${u.exit_status})` : ''}` : '';
  return (
    <li ref={row} className={`upd unit ${open ? 'open' : ''}`}>
      <div className="upd-row">
        <button className="upd-toggle unit-toggle" aria-expanded={open} onClick={onToggle}>
          <span className={`state ${tone}`}>{word}</span>
          <span className="upd-name min0">
            <strong className="truncate">{short(u.name)}</strong>
            {u.cause ? (
              <span className="unit-desc truncate">
                <CauseLine cause={u.cause} />
              </span>
            ) : (
              <span className="unit-desc truncate">{[u.description, why].filter(Boolean).join(' · ')}</span>
            )}
          </span>
          <MutedTag subject={`unit:${u.name}`} label={short(u.name)} compact />
          <span className="upd-change num">{u.flapping ? `${u.flapping} restarts in 10 min` : u.since ? `${running ? 'up' : 'since'} ${formatAgo(u.since, now).replace(' ago', '')}` : ''}</span>
          <svg className="chev" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
            <path d="m6 9 6 6 6-6" />
          </svg>
        </button>
        <div className="upd-controls">
          {/* Down services get the strong button; running ones a quiet one; finished one-shot jobs none. */}
          {u.sub === 'exited' ? (
            <span className="small-btn-space" />
          ) : confirmRestart ? (
            <span className="confirm">
              <span className="small ink-3">{isSelf ? 'Restart the monitor? The dashboard is away for a moment' : `Restart ${short(u.name)}? What runs on it restarts or drops too`}</span>
              <button className="btn-quiet danger" disabled={!!busy} onClick={() => act('restart')}>
                Restart
              </button>
              <button className="btn-quiet" onClick={() => setConfirmRestart(false)}>
                Keep running
              </button>
            </span>
          ) : (
            <button
              className={tone === 'crit' || !running ? 'btn small-btn' : 'btn-ghost small-btn'}
              disabled={!!busy}
              onClick={() => (running && (isSelf || CAREFUL.test(u.name)) ? setConfirmRestart(true) : act(running ? 'restart' : 'start'))}
            >
              {busy === 'restart' ? 'Restarting…' : busy === 'start' ? 'Starting…' : running ? 'Restart' : 'Start'}
            </button>
          )}
        </div>
      </div>
      {open && (
        <div className="upd-details">
          <dl className="unit-facts">
            <div>
              <dt>Unit</dt>
              <dd className="mono truncate" title={u.name}>
                {u.name}
              </dd>
            </div>
            <div>
              <dt>State</dt>
              {/* "failed (failed)" says it twice: the sub-state only shows when it adds something. */}
              <dd className="truncate">{u.sub && u.sub !== u.active ? `${u.active} · ${u.sub}` : u.active}</dd>
            </div>
            <div>
              <dt>Starts at boot</dt>
              <dd>{u.enabled === 'enabled' ? 'yes' : u.enabled || 'no'}</dd>
            </div>
            <div>
              <dt>Restarts</dt>
              <dd className="num">{u.restarts}</dd>
            </div>
          </dl>
          <Logs name={u.name} />
          <div className="upd-actions unit-foot">
            {/* An ignored service never alerts, so there is nothing to mute. */}
            {!u.ignored && <MuteButton subject={`unit:${u.name}`} label={short(u.name)} />}
            {running && u.name !== self && (
              <>
                {confirmStop ? (
                  <span className="confirm">
                    stop {short(u.name)}? it stays stopped until started{' '}
                    <button className="btn-quiet danger" disabled={!!busy} onClick={() => act('stop')}>
                      Stop
                    </button>
                    <button className="btn-quiet" onClick={() => setConfirmStop(false)}>
                      Keep running
                    </button>
                  </span>
                ) : (
                  <button className="btn-quiet" disabled={!!busy} onClick={() => setConfirmStop(true)}>
                    Stop…
                  </button>
                )}
              </>
            )}
            {u.ignored && (
              <span className="unit-note">
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.9" strokeLinecap="round" aria-hidden="true">
                  <circle cx="12" cy="12" r="9" />
                  <path d="M12 11v5M12 8h.01" />
                </svg>
                <span>
                  Ignored in <Link to="/settings#system-services">Settings → System services</Link>, so it never alerts.
                </span>
              </span>
            )}
          </div>
        </div>
      )}
    </li>
  );
}

const FILTERS: Filter[] = ['all', 'problems', 'running'];

export function SystemServicesPage() {
  const poll = usePoll(api.units, 15000);
  const now = useNow(15000);
  const [filter, setFilter] = useState<Filter>('all');
  const [query, setQuery] = useState('');
  // ?open=<unit> (from the overview's attention card) opens that service.
  const [params] = useSearchParams();
  const [open, setOpen] = useState<string | undefined>(() => params.get('open') ?? undefined);
  const data: UnitsResponse | undefined = poll.data;
  const list = useMemo(() => data?.units ?? [], [data]);
  const counts = { all: list.length, problems: list.filter(isProblem).length, running: list.filter((u) => u.active === 'active' && u.sub === 'running').length };
  const shown = list.filter(
    (u) => (filter === 'all' || (filter === 'problems' ? isProblem(u) : u.active === 'active' && u.sub === 'running')) && `${u.name} ${u.description}`.toLowerCase().includes(query.toLowerCase()),
  );
  const paged = usePaged(shown, 25, `${filter}:${query}`);
  // A service opened by link (?open=) is on whichever page holds it: show that page once it loads.
  const linked = params.get('open');
  const linkedAt = linked ? shown.findIndex((u) => u.name === linked) : -1;
  const { setPage } = paged;
  useEffect(() => {
    if (linkedAt >= 0) setPage(Math.floor(linkedAt / 25));
  }, [linked, linkedAt >= 0]); // eslint-disable-line react-hooks/exhaustive-deps
  return (
    <main>
      <div className="page-title">
        <div>
          <h1>Services</h1>
          <ServicesTabs current="system" />
        </div>
        <div className="toolbar">
          <Seg<Filter>
            label="Filter services"
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
          <input className="search" placeholder="Filter services…" value={query} onChange={(e) => setQuery(e.target.value)} aria-label="Search services" />
        </div>
      </div>
      {data && !data.enabled && (
        <p className="notice">
          Watching is off, so failures do not alert. Turn on <strong>Watch systemd services</strong> in <Link to="/settings#system-services">Settings → System services</Link>.
        </p>
      )}
      {poll.error && !poll.unauthorized && <p className="alert">Can't reach the monitor ({poll.error.message}). Retrying.</p>}
      {!data ? (
        <Skeleton kind="list" rows={8} />
      ) : list.length === 0 ? (
        <div className="empty">
          <strong>No services yet</strong>
          The first look at systemd happens within half a minute of the monitor starting.
        </div>
      ) : shown.length === 0 ? (
        <p className="note">{filter === 'problems' ? 'Every service is healthy.' : 'No services match.'}</p>
      ) : (
        <>
          <ul className="upd-list">
            {paged.items.map((u) => (
              <UnitRow
                key={u.name}
                u={u}
                focus={open === u.name && params.get('open') === u.name}
                self={data.self}
                now={now}
                open={open === u.name}
                onToggle={() => setOpen((o) => (o === u.name ? undefined : u.name))}
                onChanged={poll.refresh}
              />
            ))}
          </ul>
          <Pager paged={paged} label="Pages of services" noun="services" />
        </>
      )}
    </main>
  );
}
