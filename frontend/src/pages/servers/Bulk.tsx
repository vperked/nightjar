import { useState } from 'react';
import { ApiError, api } from '../../api/client';
import { useNotifications } from '../../api/NotificationsContext';
import type { RemoteHost, SSHAction } from '../../api/types';
import { MUTE_FOR } from '../../components/notifications';
import { useToast } from '../../components/Toasts';
import { plural } from '../../format';

// BulkAction is something done to every selected server at once.
export type BulkAction = SSHAction | 'mute' | 'remove';

// BULK says what each action is called, which servers it can be done to, and why the rest are left out.
const BULK: Record<BulkAction, { name: string; done: string; can: (h: RemoteHost) => boolean; skip: string }> = {
  test: { name: 'Test SSH', done: 'Logged in', can: (h) => !!h.ssh, skip: 'no SSH login' },
  'restart-agent': { name: 'Restart agents', done: 'Agent restarted', can: (h) => h.kind === 'agent' && !!h.ssh, skip: 'no agent and SSH login' },
  'reinstall-agent': { name: 'Reinstall agents', done: 'Agent reinstalled', can: (h) => h.kind === 'agent' && !!h.ssh, skip: 'no agent and SSH login' },
  reboot: { name: 'Reboot', done: 'Rebooting', can: (h) => !!h.ssh, skip: 'no SSH login' },
  mute: { name: 'Mute', done: 'Muted', can: () => true, skip: '' },
  remove: { name: 'Remove', done: 'Removed', can: () => true, skip: '' },
};

// Outcome is how one server's part of a bulk action went.
export interface Outcome {
  id: string;
  name: string;
  state: 'running' | 'ok' | 'failed' | 'skipped';
  text?: string;
}

// Run is a bulk action under way or done, with each server's outcome.
export interface Run {
  action: BulkAction;
  title: string;
  outcomes: Outcome[];
}

// atOnce is how many servers an action works on at the same time.
const atOnce = 4;

// eachLimited runs fn for every item, at most `limit` at a time.
async function eachLimited<T>(items: T[], limit: number, fn: (item: T) => Promise<void>) {
  let next = 0;
  const worker = async () => {
    while (next < items.length) await fn(items[next++]);
  };
  await Promise.all(Array.from({ length: Math.min(limit, items.length) }, worker));
}

const firstLine = (s: string) => s.trim().split('\n')[0] ?? '';

// BulkBar floats at the bottom while servers are selected, with what can be done to all of them.
// Rebooting, removing and muting ask first.
export function BulkBar({ picked, busy, onRun, onClear }: { picked: RemoteHost[]; busy: boolean; onRun: (action: BulkAction, minutes?: number) => void; onClear: () => void }) {
  const [ask, setAsk] = useState<'reboot' | 'remove' | 'mute' | null>(null);
  const count = (a: BulkAction) => picked.filter(BULK[a].can).length;
  const label = (a: BulkAction) => {
    const n = count(a);
    return n === picked.length ? BULK[a].name : `${BULK[a].name} (${n})`;
  };
  const why = (a: BulkAction) => {
    const left = picked.filter((h) => !BULK[a].can(h));
    return left.length ? `Leaves out ${left.map((h) => h.name).join(', ')}: ${BULK[a].skip}` : undefined;
  };
  const run = (a: BulkAction, minutes?: number) => {
    setAsk(null);
    onRun(a, minutes);
  };
  const action = (a: 'test' | 'restart-agent' | 'reinstall-agent') => (
    <button type="button" className="btn-quiet" onClick={() => run(a)} disabled={busy || count(a) === 0} title={why(a)}>
      {label(a)}
    </button>
  );

  return (
    <div className="bulk-bar servers-bar" role="toolbar" aria-label="Selected servers">
      <span className="bulk-bar-count">
        <strong>{picked.length}</strong> selected
      </span>
      <span className="bulk-bar-rule" aria-hidden="true" />
      {ask === 'reboot' ? (
        <span className="bulk-bar-confirm">
          <span>Reboot {plural(count('reboot'), 'server')}?</span>
          <button type="button" className="btn-quiet danger" onClick={() => run('reboot')}>
            Reboot
          </button>
          <button type="button" className="btn-quiet" onClick={() => setAsk(null)}>
            Keep running
          </button>
        </span>
      ) : ask === 'remove' ? (
        <span className="bulk-bar-confirm">
          <span>Stop watching {plural(picked.length, 'server')}?</span>
          <button type="button" className="btn-quiet danger" onClick={() => run('remove')}>
            Remove
          </button>
          <button type="button" className="btn-quiet" onClick={() => setAsk(null)}>
            Keep
          </button>
        </span>
      ) : ask === 'mute' ? (
        <span className="bulk-bar-confirm">
          <span>Mute for</span>
          {MUTE_FOR.map((m) => (
            <button key={m.minutes} type="button" className="btn-quiet" onClick={() => run('mute', m.minutes)}>
              {m.label}
            </button>
          ))}
          <button type="button" className="btn-quiet" onClick={() => run('mute', 0)}>
            Unmute
          </button>
          <button type="button" className="btn-quiet" onClick={() => setAsk(null)}>
            Cancel
          </button>
        </span>
      ) : (
        <span className="bulk-bar-actions">
          {action('test')}
          {action('restart-agent')}
          {action('reinstall-agent')}
          <button type="button" className="btn-quiet" onClick={() => setAsk('mute')} disabled={busy}>
            Mute…
          </button>
          <button type="button" className="btn-quiet danger" onClick={() => setAsk('reboot')} disabled={busy || count('reboot') === 0} title={why('reboot')}>
            {label('reboot')}…
          </button>
          <button type="button" className="btn-quiet danger" onClick={() => setAsk('remove')} disabled={busy}>
            Remove…
          </button>
        </span>
      )}
      <span className="bulk-bar-rule" aria-hidden="true" />
      <button className="bulk-bar-close" aria-label="Clear selection" title="Clear selection" onClick={onClear}>
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
          <path d="M6 6l12 12M18 6L6 18" />
        </svg>
      </button>
    </div>
  );
}

// useBulk runs an action on several servers, keeping each one's outcome as it comes in.
export function useBulk(refresh: () => void) {
  const notify = useToast();
  const { refresh: refreshMutes } = useNotifications();
  const [run, setRun] = useState<Run | null>(null);
  const busy = run?.outcomes.some((o) => o.state === 'running') ?? false;

  const start = async (action: BulkAction, hosts: RemoteHost[], minutes = 0) => {
    const b = BULK[action];
    const doing = hosts.filter(b.can);
    const title = action === 'mute' ? (minutes ? `Mute for ${MUTE_FOR.find((m) => m.minutes === minutes)?.label ?? `${minutes} minutes`}` : 'Unmute') : b.name;
    const outcomes: Outcome[] = hosts.map((h) => ({ id: h.id, name: h.name, state: b.can(h) ? 'running' : 'skipped', text: b.can(h) ? undefined : b.skip }));
    setRun({ action, title, outcomes });
    // final keeps the outcomes here as well, for the summary once all are in.
    const final = new Map(outcomes.map((o) => [o.id, o]));
    const settle = (id: string, o: Partial<Outcome>) => {
      final.set(id, { ...final.get(id)!, ...o });
      setRun((r) => r && { ...r, outcomes: r.outcomes.map((x) => (x.id === id ? { ...x, ...o } : x)) });
    };

    await eachLimited(doing, atOnce, async (h) => {
      try {
        if (action === 'mute') {
          await api.mute(`host:${h.id}`, h.name, minutes);
          settle(h.id, { state: 'ok', text: minutes ? 'muted' : 'unmuted' });
        } else if (action === 'remove') {
          await api.deleteHost(h.id);
          settle(h.id, { state: 'ok', text: 'removed' });
        } else {
          const { output } = await api.runHostSSH(h.id, action);
          settle(h.id, { state: 'ok', text: firstLine(output) || b.done });
        }
      } catch (err) {
        settle(h.id, { state: 'failed', text: err instanceof ApiError ? err.message : String(err) });
      }
    });

    const all = [...final.values()];
    const failed = all.filter((o) => o.state === 'failed').length;
    const worked = all.filter((o) => o.state === 'ok').length;
    notify(
      failed
        ? { tone: 'error', title: `${title}: ${failed} of ${worked + failed} did not work`, body: 'Each server’s answer is listed above the servers.' }
        : { tone: 'ok', title: `${title}: ${plural(worked, 'server')}`, body: all.some((o) => o.state === 'skipped') ? 'Servers it could not be done to were left out.' : undefined },
    );
    if (action === 'mute') refreshMutes();
    refresh();
  };

  return { run, busy, start, dismiss: () => setRun(null) };
}

// BulkResults shows each server's outcome of the last bulk action, live while it runs.
export function BulkResults({ run, onDismiss }: { run: Run; onDismiss: () => void }) {
  const running = run.outcomes.filter((o) => o.state === 'running').length;
  const failed = run.outcomes.filter((o) => o.state === 'failed').length;
  return (
    <section className="section bulk-results" aria-label={`${run.title} results`}>
      <div className="section-head">
        <span className="label">{run.title}</span>
        <span className="small ink-3">
          {running ? `${running} of ${run.outcomes.filter((o) => o.state !== 'skipped').length} still going…` : failed ? `${failed} did not work` : 'done'}
          {!running && (
            <button type="button" className="btn-quiet bulk-results-close" onClick={onDismiss}>
              Dismiss
            </button>
          )}
        </span>
      </div>
      <ul className="bulk-results-list" role="status">
        {run.outcomes.map((o) => (
          <li key={o.id} className={`bulk-outcome ${o.state}`}>
            <span className="bulk-outcome-mark" aria-hidden="true" />
            <strong className="truncate">{o.name}</strong>
            <span className="bulk-outcome-word">
              {o.state === 'running' ? 'working…' : o.state === 'ok' ? BULK[run.action].done.toLowerCase() : o.state === 'failed' ? 'did not work' : 'left out'}
            </span>
            {o.text && o.state !== 'running' && <span className="bulk-outcome-text">{o.text}</span>}
          </li>
        ))}
      </ul>
    </section>
  );
}
