import { useState } from 'react';
import { ApiError, api } from '../../api/client';
import { useNotifications } from '../../api/NotificationsContext';
import type { FirewallRule, RemoteHost, SSHAction } from '../../api/types';
import { describeRule, SECURE, type SecureKind } from '../../components/Firewall';
import { MUTE_FOR } from '../../components/notifications';
import { useToast } from '../../components/Toasts';
import { plural } from '../../format';

// BulkAction is something done to every selected server at once.
export type BulkAction = SSHAction | 'mute' | 'remove' | 'firewall';

// FirewallBatch is what Firewall… adds to each server: rules said outright, or "only Nightjar reaches
// the agent", whose address and port each server's own agent says.
export type FirewallBatch = { kind: 'rules'; title: string; rules: Partial<FirewallRule>[] } | { kind: 'agent'; title: string };

// BULK says what each action is called, which servers it can be done to, and why the rest are left out.
const BULK: Record<BulkAction, { name: string; done: string; can: (h: RemoteHost) => boolean; skip: string }> = {
  test: { name: 'Test SSH', done: 'Logged in', can: (h) => !!h.ssh, skip: 'no SSH login' },
  'restart-agent': { name: 'Restart agents', done: 'Agent restarted', can: (h) => h.kind === 'agent' && !!h.ssh, skip: 'no agent and SSH login' },
  'reinstall-agent': { name: 'Reinstall agents', done: 'Agent reinstalled', can: (h) => h.kind === 'agent' && !!h.ssh, skip: 'no agent and SSH login' },
  reboot: { name: 'Reboot', done: 'Rebooting', can: (h) => !!h.ssh, skip: 'no SSH login' },
  mute: { name: 'Mute', done: 'Muted', can: () => true, skip: '' },
  remove: { name: 'Remove', done: 'Removed', can: () => true, skip: '' },
  firewall: { name: 'Firewall', done: 'Rules added', can: (h) => h.kind === 'agent', skip: 'no agent, so no firewall to manage' },
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
export function BulkBar({
  picked,
  busy,
  onRun,
  onFirewall,
  onClear,
}: {
  picked: RemoteHost[];
  busy: boolean;
  onRun: (action: BulkAction, minutes?: number) => void;
  // onFirewall opens the panel that picks the rules to add to every selected server.
  onFirewall: () => void;
  onClear: () => void;
}) {
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
          <button type="button" className="btn-quiet" onClick={onFirewall} disabled={busy || count('firewall') === 0} title={why('firewall')}>
            {label('firewall')}…
          </button>
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

// addRules adds a batch to one server's firewall, a rule at a time, and says how it went. A rule already
// there counts as done; a refusal stops it there, saying how far it got.
async function addRules(id: string, batch: FirewallBatch): Promise<string> {
  let rules: Partial<FirewallRule>[];
  if (batch.kind === 'agent') {
    // Each server sees Nightjar from its own side, so its agent says the address and its port.
    const v = await api.hostFirewall(id);
    const port = v.firewall.open?.find((o) => /agent/i.test(o.what))?.port;
    if (!v.dashboard || !port) throw new Error('its agent does not say which address Nightjar asks from; reinstall it from the Servers page');
    rules = [
      { action: 'allow', source: v.dashboard, port: String(port), proto: 'tcp', note: 'Nightjar only' },
      { action: 'block', port: String(port), proto: 'tcp', note: 'everyone but Nightjar' },
    ];
  } else {
    rules = batch.rules;
  }
  let added = 0;
  for (const rule of rules) {
    try {
      await api.addHostRule(id, rule);
      added++;
    } catch (err) {
      const text = err instanceof ApiError ? err.message : String(err);
      if (/already a rule like that/i.test(text)) continue;
      throw new Error(added ? `added ${plural(added, 'rule')}, then: ${text}` : text);
    }
  }
  return added ? `${plural(added, 'rule')} added` : 'all there already';
}

// useBulk runs an action on several servers, keeping each one's outcome as it comes in.
export function useBulk(refresh: () => void) {
  const notify = useToast();
  const { refresh: refreshMutes } = useNotifications();
  const [run, setRun] = useState<Run | null>(null);
  const busy = run?.outcomes.some((o) => o.state === 'running') ?? false;

  const start = async (action: BulkAction, hosts: RemoteHost[], minutes = 0, batch?: FirewallBatch) => {
    const b = BULK[action];
    const doing = hosts.filter(b.can);
    const title =
      action === 'mute' ? (minutes ? `Mute for ${MUTE_FOR.find((m) => m.minutes === minutes)?.label ?? `${minutes} minutes`}` : 'Unmute') : action === 'firewall' && batch ? batch.title : b.name;
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
        } else if (action === 'firewall') {
          if (!batch) throw new Error('no rules chosen');
          settle(h.id, { state: 'ok', text: await addRules(h.id, batch) });
        } else {
          const { output } = await api.runHostSSH(h.id, action);
          settle(h.id, { state: 'ok', text: firstLine(output) || b.done });
        }
      } catch (err) {
        settle(h.id, { state: 'failed', text: err instanceof Error ? err.message : String(err) });
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

// FirewallChoice is what Firewall… adds: a Secure preset, only Nightjar on the agent's port, or one rule.
type FirewallChoice = SecureKind | 'agent' | 'rule';

// BulkFirewall picks the rules to add to every selected server's firewall, then hands them to the bulk
// run, which adds them server by server and lists how each went.
export function BulkFirewall({ picked, busy, onRun, onClose }: { picked: RemoteHost[]; busy: boolean; onRun: (batch: FirewallBatch) => void; onClose: () => void }) {
  const servers = picked.filter(BULK.firewall.can);
  const [choice, setChoice] = useState<FirewallChoice>('web');
  const [action, setAction] = useState<'allow' | 'block' | 'limit'>('allow');
  const [source, setSource] = useState('');
  const [port, setPort] = useState('');
  const [proto, setProto] = useState<'' | 'tcp' | 'udp'>('tcp');
  const [count, setCount] = useState('30');
  const rule: Partial<FirewallRule> = {
    action,
    source: source.trim(),
    port: port.trim(),
    // The protocol goes only with a port, as the rule form sends it.
    proto: (port.trim() && proto) || undefined,
    rate: action === 'limit' ? `${count}/min` : undefined,
    note: 'added to several servers',
  };
  const missing =
    choice !== 'rule'
      ? ''
      : !source.trim() && !port.trim()
        ? 'Give an address, a port, or both: a rule for everyone on every port is refused.'
        : action === 'limit' && !port.trim()
          ? 'A limit is for a port: give which one.'
          : action === 'limit' && !Number(count)
            ? 'Say how many new connections a minute each address may make.'
            : '';
  const choices: { value: FirewallChoice; title: string; body: string }[] = [
    { value: 'web', title: SECURE.web.title, body: 'The site open (80, 443, HTTP/3); the databases behind it shut.' },
    { value: 'email', title: SECURE.email.title, body: 'Mail in on 25; sending and IMAP over TLS, limited; plain-text IMAP and POP3 shut.' },
    { value: 'agent', title: 'Only Nightjar reaches the agent', body: 'Each server lets in only Nightjar on its agent’s port, by the address it sees Nightjar ask from.' },
    { value: 'rule', title: 'One rule', body: 'Allow, block or limit an address or a port on all of them.' },
  ];
  const submit = () => {
    if (missing) return;
    if (choice === 'agent') onRun({ kind: 'agent', title: 'Only Nightjar reaches the agent' });
    else if (choice === 'rule') onRun({ kind: 'rules', title: describeRule(rule as FirewallRule), rules: [rule] });
    else onRun({ kind: 'rules', title: SECURE[choice].title, rules: SECURE[choice].rules.map((r) => r.rule) });
  };
  return (
    <section className="section bulk-firewall" aria-label="Add firewall rules to the selected servers">
      <div className="section-head">
        <span className="label">Add firewall rules to {plural(servers.length, 'server')}</span>
        {servers.length < picked.length && <span className="small ink-3">{plural(picked.length - servers.length, 'server')} without an agent left out</span>}
      </div>
      <div className="bulk-fw-choices" role="radiogroup" aria-label="What to add">
        {choices.map((c) => (
          <button key={c.value} type="button" role="radio" aria-checked={choice === c.value} className={`fw-choice${choice === c.value ? ' on' : ''}`} onClick={() => setChoice(c.value)}>
            <strong>{c.title}</strong>
            <span>{c.body}</span>
          </button>
        ))}
      </div>
      {(choice === 'web' || choice === 'email') && (
        <ul className="bulk-fw-list">
          {SECURE[choice].rules.map((r) => (
            <li key={r.why}>{r.why}</li>
          ))}
        </ul>
      )}
      {choice === 'agent' && (
        <p className="small ink-3">
          Two rules on each: allow Nightjar on the agent’s port, then block everyone else there. A server whose agent is too old to say Nightjar’s address is listed as not done.
        </p>
      )}
      {choice === 'rule' && (
        <div className="bulk-fw-rule">
          <select className="field" value={action} onChange={(e) => setAction(e.target.value as typeof action)} aria-label="Do">
            <option value="allow">Allow</option>
            <option value="block">Block</option>
            <option value="limit">Limit</option>
          </select>
          <input className="field mono" value={source} onChange={(e) => setSource(e.target.value)} placeholder="from anyone, or 203.0.113.7" aria-label="From" spellCheck={false} />
          <input className="field mono" value={port} onChange={(e) => setPort(e.target.value)} placeholder="every port, or 22 or 80,443" aria-label="Port" spellCheck={false} />
          <select className="field" value={proto} onChange={(e) => setProto(e.target.value as typeof proto)} aria-label="Protocol" disabled={!port.trim()}>
            <option value="tcp">tcp only</option>
            <option value="udp">udp only</option>
            <option value="">tcp and udp</option>
          </select>
          {action === 'limit' && (
            <label className="bulk-fw-rate small ink-3">
              <input className="field mono" value={count} onChange={(e) => setCount(e.target.value.replace(/\D/g, ''))} inputMode="numeric" aria-label="New connections a minute" /> a minute each
            </label>
          )}
        </div>
      )}
      <div className="bulk-fw-foot">
        <p className="small ink-3" aria-live="polite">
          {missing || (choice === 'rule' ? `${describeRule(rule as FirewallRule)}, on each.` : 'Rules a server has already are left as they are.')}
        </p>
        <span className="fw-ask-acts">
          <button type="button" className="btn-quiet" onClick={onClose} disabled={busy}>
            Cancel
          </button>
          <button type="button" className="btn" onClick={submit} disabled={busy || !!missing || servers.length === 0}>
            Add to {plural(servers.length, 'server')}
          </button>
        </span>
      </div>
    </section>
  );
}
