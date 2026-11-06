import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, ApiError } from '../api/client';
import type { FirewallDrop, FirewallPort, FirewallRule, FirewallStatus } from '../api/types';
import { formatAgo, formatBytes, plural } from '../format';
import { useNow } from '../hooks/useNow';
import { useToast } from './Toasts';
import { Section, Seg } from './ui';

// The firewall, managed from the dashboard: what happens to traffic no rule decides, the rules in
// order (the first that matches decides), what they caught and dropped, the open ports, and the
// machine's own rules. The same for this machine and for a server through its agent.

// TOOL says which firewall the machine runs, in words.
export const TOOL: Record<FirewallStatus['tool'], string> = { ufw: 'ufw', firewalld: 'firewalld', iptables: 'plain iptables' };

const errText = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : String(e));

const ACTION: Record<FirewallRule['action'], string> = { allow: 'Allow', block: 'Block', limit: 'Limit' };

// UNIT says a rate's unit in words: "6/min" is "6 a minute".
const UNIT: Record<string, string> = { sec: 'a second', min: 'a minute', hour: 'an hour' };
export const rateWords = (rate?: string) => {
  const [n, unit] = (rate ?? '').split('/');
  return `${n} ${UNIT[unit] ?? unit}`;
};

// portWords says a rule's ports: "tcp port 22", "ports 80, 443", "port 8000–8100", "every port".
const portWords = (r: Pick<FirewallRule, 'port' | 'proto'>) => {
  if (!r.port) return 'every port';
  const list = r.port.replaceAll(':', '–').replaceAll(',', ', ');
  return `${r.proto ? `${r.proto} ` : ''}${r.port.includes(',') ? 'ports' : 'port'} ${list}`;
};

// describeRule puts a rule in words: "Block 203.0.113.7 on every port", "Limit anyone on tcp port 22
// to 6 a minute each", "Block outgoing connections on port 25", "Allow outgoing to 192.0.2.25 on every port".
export function describeRule(r: Pick<FirewallRule, 'action' | 'source' | 'port' | 'proto' | 'rate' | 'direction'>): string {
  const who = r.direction === 'out' ? (r.source ? `outgoing to ${r.source}` : 'outgoing connections') : r.source || 'anyone';
  return `${ACTION[r.action]} ${who} on ${portWords(r)}${r.action === 'limit' ? ` to ${rateWords(r.rate)} each` : ''}`;
}

// within says whether an address is a network's, or the address itself: "45.142.120.11" is within
// "45.142.120.0/24". IPv6 networks only match their own address.
export function within(addr: string, net: string): boolean {
  if (addr === net) return true;
  const ip4 = (s: string) => {
    const p = s.split('.').map(Number);
    return p.length === 4 && p.every((n) => Number.isInteger(n) && n >= 0 && n <= 255) ? ((p[0] << 24) >>> 0) + (p[1] << 16) + (p[2] << 8) + p[3] : null;
  };
  const [base, bits] = net.split('/');
  const a = ip4(addr);
  const b = ip4(base);
  if (a === null || b === null || bits === undefined) return false;
  const mask = Number(bits) === 0 ? 0 : (~0 << (32 - Number(bits))) >>> 0;
  return (a & mask) >>> 0 === (b & mask) >>> 0;
}

// FirewallTarget is the machine a firewall belongs to: this one, or a server through its agent. Each
// call answers with the firewall after it.
export interface FirewallTarget {
  key: string;
  name: string;
  load: () => Promise<FirewallStatus | undefined>;
  add: (rule: Partial<FirewallRule>) => Promise<FirewallStatus | undefined>;
  update: (id: string, rule: Partial<FirewallRule>) => Promise<FirewallStatus | undefined>;
  remove: (id: string) => Promise<FirewallStatus | undefined>;
  order: (ids: string[]) => Promise<FirewallStatus | undefined>;
  setDefault: (def: '' | 'drop') => Promise<FirewallStatus | undefined>;
  keep: (keep: boolean) => Promise<FirewallStatus | undefined>;
}

export const localFirewall: FirewallTarget = {
  key: 'local',
  name: 'this machine',
  load: () => api.firewall().then((r) => (r.enabled ? r.firewall : undefined)),
  add: (rule) => api.addFirewallRule(rule).then((r) => r.firewall),
  update: (id, rule) => api.updateFirewallRule(id, rule).then((r) => r.firewall),
  remove: (id) => api.removeFirewallRule(id).then((r) => r.firewall),
  order: (ids) => api.orderFirewall(ids).then((r) => r.firewall),
  setDefault: (def) => api.setFirewallDefault(def).then((r) => r.firewall),
  keep: (keep) => api.keepFirewallDefault(keep).then((r) => r.firewall),
};

export const hostFirewall = (h: { id: string; name: string }): FirewallTarget => ({
  key: h.id,
  name: h.name,
  load: () => api.hostFirewall(h.id).then((v) => v.firewall),
  add: (rule) => api.addHostRule(h.id, rule).then((v) => v.firewall),
  update: (id, rule) => api.updateHostRule(h.id, id, rule).then((v) => v.firewall),
  remove: (id) => api.removeHostRule(h.id, id).then((v) => v.firewall),
  order: (ids) => api.orderHostRules(h.id, ids).then((v) => v.firewall),
  setDefault: (def) => api.setHostDefault(h.id, def).then((v) => v.firewall),
  keep: (keep) => api.keepHostDefault(h.id, keep).then((v) => v.firewall),
});

interface Draft {
  // action is '' until one is picked: a new rule starts with no action, so nothing is chosen for you.
  action: FirewallRule['action'] | '';
  // who and ports say whether the rule is for anyone or every port, or for what is typed in source
  // and port.
  who: 'any' | 'some';
  source: string;
  ports: 'all' | 'some';
  port: string;
  proto: '' | 'tcp' | 'udp';
  // count and unit are a limit's rate for each address.
  count: string;
  unit: 'sec' | 'min' | 'hour';
  note: string;
}

const EMPTY: Draft = { action: '', who: 'any', source: '', ports: 'all', port: '', proto: '', count: '6', unit: 'min', note: '' };

// startDraft is a new rule's form filled with what is known: an address, a port, an action.
const startDraft = (d: Partial<Draft>): Draft => ({ ...EMPTY, ...d, who: d.source ? 'some' : 'any', ports: d.port ? 'some' : 'all' });

const draftOf = (r: FirewallRule): Draft => {
  const [count, unit] = (r.rate ?? '6/min').split('/');
  return {
    action: r.action,
    who: r.source ? 'some' : 'any',
    source: r.source ?? '',
    ports: r.port ? 'some' : 'all',
    port: (r.port ?? '').replaceAll(':', '-'),
    proto: r.proto ?? '',
    count,
    unit: (unit as Draft['unit']) || 'min',
    note: r.note ?? '',
  };
};

// ruleOf is the rule a draft makes, as the API takes it.
const ruleOf = (draft: Draft, dir: 'in' | 'out'): Partial<FirewallRule> => {
  const source = draft.who === 'some' ? draft.source.trim() : '';
  const port = draft.ports === 'some' ? draft.port.trim() : '';
  return {
    action: draft.action || undefined,
    direction: dir === 'out' ? 'out' : undefined,
    source,
    port,
    // The protocol goes only with a port: a protocol alone is refused.
    proto: (port && draft.proto) || undefined,
    rate: draft.action === 'limit' ? `${draft.count.trim()}/${draft.unit}` : undefined,
    note: draft.note.trim(),
  };
};

// missing says what still keeps a draft from being a rule, in words, or '' when it is ready.
const missing = (draft: Draft, dir: 'in' | 'out'): string => {
  if (!draft.action) return 'Pick what the rule does.';
  if (draft.who === 'some' && !draft.source.trim()) return `Type ${dir === 'out' ? 'where to' : 'who'}: an address such as 203.0.113.7, or a network such as 198.51.100.0/24.`;
  if (draft.ports === 'some' && !draft.port.trim()) return 'Type the port: 22, a list such as 80,443, or a range such as 8000-8100.';
  if (draft.action === 'limit' && draft.ports === 'all') return 'A limit is for a port: pick which one.';
  if (draft.action === 'limit' && !Number(draft.count)) return 'Say how many each address may make.';
  if (draft.who === 'any' && draft.ports === 'all')
    return draft.action === 'block'
      ? `That would block ${dir === 'out' ? 'everything this machine sends' : 'everything, you included'}: pick an address or a port.`
      : `That would let ${dir === 'out' ? 'everything out' : 'everything in'}: pick an address or a port.`;
  return '';
};

// Shortcuts for opening what a home server usually runs.
interface Preset {
  label: string;
  port: string;
  proto: Draft['proto'];
}
function presets(st: FirewallStatus): Preset[] {
  const ssh = (st.open ?? []).filter((o) => o.what === 'SSH').map((o) => o.port);
  const qb = st.listening.filter((p) => !p.local && /qbittorrent/i.test(p.program ?? '')).map((p) => p.port);
  return [
    ...(ssh.length ? [{ label: 'SSH', port: ssh.join(','), proto: 'tcp' as const }] : []),
    { label: 'Web', port: '80,443', proto: 'tcp' },
    { label: 'Plex', port: '32400', proto: 'tcp' },
    ...(qb.length ? [{ label: 'qBittorrent', port: [...new Set(qb)].join(','), proto: '' as const }] : []),
    { label: 'DNS', port: '53', proto: '' },
    { label: 'Samba', port: '139,445', proto: 'tcp' },
  ];
}

// hitWords says what a rule caught: "dropped 1,204 · 82 KB", "let in 30".
function hitWords(r: FirewallRule, hits: FirewallStatus['hits']): string {
  const h = hits?.[r.id];
  if (!h || !h.packets) return 'nothing caught yet';
  const n = `${h.packets.toLocaleString()} · ${formatBytes(h.bytes)}`;
  return r.action === 'allow' ? `let in ${n}` : r.action === 'limit' ? `dropped ${n} over the limit` : `dropped ${n}`;
}

// withLists fills in the open ports when an answer has none: an agent from before they were read, or
// one that could read nothing from /proc/net, sends null.
const withLists = (s: FirewallStatus | undefined): FirewallStatus | undefined => s && { ...s, listening: s.listening ?? [] };

// FirewallManager is a machine's whole firewall. It refreshes itself every 15 seconds while shown,
// for what the rules caught.
export function FirewallManager({ target }: { target: FirewallTarget }) {
  const notify = useToast();
  const [st, setSt] = useState<FirewallStatus>();
  const [off, setOff] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState('');
  const [dir, setDir] = useState<'in' | 'out'>('in');
  const [draft, setDraft] = useState<Draft>(EMPTY);
  const [editing, setEditing] = useState<FirewallRule>();
  const [formError, setFormError] = useState('');
  // adding is whether the rule form is open: it opens from Add a rule, a port, a dropped address or Edit.
  const [adding, setAdding] = useState(false);
  const [asking, setAsking] = useState(false);
  const busyRef = useRef('');
  busyRef.current = busy;

  const refresh = useCallback(
    () =>
      target.load().then(
        (s) => {
          setOff(!s);
          setSt(withLists(s));
          setError('');
        },
        (e) => setError(errText(e)),
      ),
    [target],
  );
  useEffect(() => {
    setSt(undefined);
    setDraft(EMPTY);
    setEditing(undefined);
    setAdding(false);
    setAsking(false);
    refresh();
    const t = window.setInterval(() => {
      if (document.visibilityState === 'visible' && !busyRef.current) refresh();
    }, 15000);
    return () => window.clearInterval(t);
  }, [refresh]);

  // run makes a change, shows the firewall after it, and says how it went.
  const run = async (key: string, change: () => Promise<FirewallStatus | undefined>, ok?: { title: string; body?: string }) => {
    setBusy(key);
    try {
      const s = await change();
      if (s) setSt(withLists(s));
      if (ok) notify({ tone: 'ok', ...ok });
      return true;
    } catch (err) {
      notify({ tone: 'error', title: `Could not change the firewall · ${target.name}`, body: errText(err) });
      return false;
    } finally {
      setBusy('');
    }
  };

  const form = useRef<HTMLFormElement>(null);
  const toForm = () => form.current?.scrollIntoView?.({ behavior: 'smooth', block: 'start' });
  // ruleFor opens the form on a new rule: empty from Add a rule, a port's from the open ports, or a
  // block of an address that kept being dropped.
  const ruleFor = (d: Partial<Draft>, direction: 'in' | 'out' = 'in') => {
    setEditing(undefined);
    setFormError('');
    setDir(direction);
    setDraft(startDraft(d));
    setAdding(true);
    setTimeout(toForm);
  };
  const edit = (r: FirewallRule) => {
    setEditing(r);
    setFormError('');
    setDraft(draftOf(r));
    setAdding(true);
    setTimeout(toForm);
  };
  const cancelEdit = () => {
    setEditing(undefined);
    setDraft(EMPTY);
    setFormError('');
    setAdding(false);
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (missing(draft, dir)) return;
    setBusy('form');
    setFormError('');
    const rule: Partial<FirewallRule> = { ...ruleOf(draft, dir), paused: editing?.paused };
    try {
      const s = editing ? await target.update(editing.id, rule) : await target.add(rule);
      if (s) setSt(withLists(s));
      notify({ tone: 'ok', title: `${editing ? 'Rule changed' : 'Rule added'} · ${target.name}`, body: describeRule(rule as FirewallRule) });
      setDraft(EMPTY);
      setEditing(undefined);
      setAdding(false);
    } catch (err) {
      setFormError(errText(err));
    } finally {
      setBusy('');
    }
  };

  const rules = st?.rules ?? [];
  const shown = rules.filter((r) => (dir === 'out') === (r.direction === 'out'));
  // move swaps a rule with the one before or after it in the same direction.
  const move = (r: FirewallRule, by: -1 | 1) => {
    const i = shown.indexOf(r);
    const other = shown[i + by];
    if (!other) return;
    const ids = rules.map((x) => x.id);
    const a = ids.indexOf(r.id);
    const b = ids.indexOf(other.id);
    [ids[a], ids[b]] = [ids[b], ids[a]];
    run(r.id, () => target.order(ids));
  };

  if (error)
    return (
      <Section label="Firewall">
        <p className="form-error">{error}</p>
      </Section>
    );
  if (off)
    return (
      <Section label="Firewall">
        <p className="note">This dashboard does not manage its firewall.</p>
      </Section>
    );
  if (!st)
    return (
      <Section label="Firewall">
        <p className="note">Reading the firewall…</p>
      </Section>
    );

  const full = !!st.full;
  const canChange = !st.problem;
  const tool = TOOL[st.tool];
  const aside = `${tool}${st.policy ? ` · on its own ${st.policy === 'DROP' ? 'drops' : 'lets in'} what no rule decides` : ''}`;
  const closing = st.listening.filter((p) => !p.local && !p.allowed);
  const inCount = rules.filter((r) => r.direction !== 'out').length;
  const outCount = rules.length - inCount;

  return (
    <>
      {full && (
        <Section label="Firewall" aside={<span className="small ink-3">{aside}</span>}>
          <div className="fw">
            {st.problem && <p className="note">{st.problem}</p>}
            <DefaultChoice st={st} target={target} busy={busy} run={run} asking={asking} setAsking={setAsking} closing={closing} disabled={!canChange} />
          </div>
        </Section>
      )}

      <Section label={full ? 'Rules' : 'Firewall'} aside={<span className="small ink-3">{full ? 'checked in order; the first that matches decides' : aside}</span>}>
        <div className="fw">
          {!full && (
            <p className="note fw-old">
              This agent runs an older version, so only its rules show here. It updates itself to this dashboard’s version shortly, and then the whole firewall can be managed here.
            </p>
          )}
          {!full && st.problem && <p className="note">{st.problem}</p>}
          {(full || canChange) && (
            <div className="fw-dir">
              {full && (
                <Seg
                  label="Incoming or outgoing"
                  value={dir}
                  onChange={(d) => {
                    setDir(d);
                    cancelEdit();
                  }}
                  options={[
                    { value: 'in', label: `Incoming${inCount ? ` ${inCount}` : ''}` },
                    { value: 'out', label: `Outgoing${outCount ? ` ${outCount}` : ''}` },
                  ]}
                />
              )}
              {full && st.since ? <span className="small ink-3">counted since {formatAgo(st.since)}</span> : null}
              {canChange && !adding && (
                <button type="button" className="btn fw-new" onClick={() => ruleFor({}, dir)}>
                  + Add a rule
                </button>
              )}
            </div>
          )}

          {canChange && adding && (
            <RuleForm
              formRef={form}
              st={st}
              full={full}
              tool={tool}
              dir={dir}
              setDir={setDir}
              draft={draft}
              setDraft={setDraft}
              editing={editing}
              busy={busy === 'form'}
              error={formError}
              onSubmit={submit}
              onCancel={cancelEdit}
            />
          )}
          {shown.length === 0 ? (
            <p className="note">
              {dir === 'out'
                ? 'No rules for what this machine sends. An outgoing rule looks at new connections only, so replies always go out.'
                : `No rules set from here. Rules you add go before ${tool}’s own and stay in place across reboots.`}
            </p>
          ) : (
            <ul className="fw-rule-list">
              {shown.map((r, i) => (
                <li key={r.id} className={`fw-rule${r.paused ? ' paused' : ''}${editing?.id === r.id ? ' editing' : ''}`}>
                  {full && (
                    <span className="fw-move">
                      <button type="button" className="icon-btn" aria-label={`Move up: ${describeRule(r)}`} disabled={!!busy || i === 0 || !canChange} onClick={() => move(r, -1)}>
                        ↑
                      </button>
                      <button type="button" className="icon-btn" aria-label={`Move down: ${describeRule(r)}`} disabled={!!busy || i === shown.length - 1 || !canChange} onClick={() => move(r, 1)}>
                        ↓
                      </button>
                    </span>
                  )}
                  <span className={`fw-action ${r.action}`}>{ACTION[r.action]}</span>
                  <span className="fw-rule-text min0">
                    <span className="fw-rule-who mono">
                      {r.direction === 'out' && <span className="fw-to">to </span>}
                      {r.source || 'Anyone'}
                    </span>
                    <span className="fw-rule-what">
                      {portWords(r)}
                      {r.action === 'limit' && ` · at most ${rateWords(r.rate)} each`}
                    </span>
                    {r.note && <span className="fw-rule-note">{r.note}</span>}
                    {full && <span className="fw-rule-hits">{r.paused ? 'paused: not in place' : hitWords(r, st.hits)}</span>}
                  </span>
                  <span className="fw-rule-acts">
                    {full && (
                      <>
                        <button
                          type="button"
                          className="btn-quiet"
                          disabled={!!busy || !canChange}
                          onClick={() =>
                            run(r.id, () => target.update(r.id, { ...r, paused: !r.paused }), { title: `${r.paused ? 'Rule back in place' : 'Rule paused'} · ${target.name}`, body: describeRule(r) })
                          }
                        >
                          {r.paused ? 'Resume' : 'Pause'}
                        </button>
                        <button type="button" className="btn-quiet" disabled={!!busy || !canChange} onClick={() => edit(r)}>
                          Edit
                        </button>
                      </>
                    )}
                    <button
                      type="button"
                      className="btn-quiet"
                      disabled={!!busy || !canChange}
                      onClick={() => run(r.id, () => target.remove(r.id), { title: `Rule removed · ${target.name}`, body: describeRule(r) })}
                    >
                      {busy === r.id ? '…' : 'Remove'}
                    </button>
                  </span>
                </li>
              ))}
            </ul>
          )}

          {dir === 'in' && st.listening.length > 0 && <OpenPorts st={st} canChange={canChange} ruleFor={(p) => ruleFor({ port: String(p.port), proto: p.proto })} />}
        </div>
      </Section>

      {full && <Dropped st={st} rules={rules} canChange={canChange} block={(source) => ruleFor({ action: 'block', source, note: 'kept being dropped' })} />}

      {full && st.native && (
        <Section label={`${tool}’s own rules`} aside={<span className="small ink-3">read only</span>}>
          <details className="fw-native">
            <summary>
              Show what {tool} has, besides the rules here ({plural(st.native.split('\n').length, 'line')})
            </summary>
            <pre className="mono">{st.native}</pre>
          </details>
        </Section>
      )}
    </>
  );
}

// RuleForm adds or changes one rule, a question at a time: which way the traffic goes, what to do
// with it, from whom, on which ports. It says the rule in words as it is filled in, and what is still
// missing, so the button is never greyed out without a reason.
function RuleForm({
  formRef,
  st,
  full,
  tool,
  dir,
  setDir,
  draft,
  setDraft,
  editing,
  busy,
  error,
  onSubmit,
  onCancel,
}: {
  formRef: React.RefObject<HTMLFormElement>;
  st: FirewallStatus;
  full: boolean;
  tool: string;
  dir: 'in' | 'out';
  setDir: (d: 'in' | 'out') => void;
  draft: Draft;
  setDraft: (d: Draft) => void;
  editing?: FirewallRule;
  busy: boolean;
  error: string;
  onSubmit: (e: React.FormEvent) => void;
  onCancel: () => void;
}) {
  const set = (d: Partial<Draft>) => setDraft({ ...draft, ...d });
  const out = dir === 'out';
  const gap = missing(draft, dir);
  const who = out ? 'To' : 'From';
  const actions: { value: FirewallRule['action']; title: string; body: string }[] = out
    ? [
        { value: 'block', title: 'Block', body: 'Stop this machine connecting there.' },
        { value: 'allow', title: 'Allow', body: 'Let it connect: an exception to a block further down.' },
      ]
    : [
        { value: 'allow', title: 'Allow', body: `Let it in, even where ${tool} would drop it.` },
        { value: 'block', title: 'Block', body: 'Drop it. A block that would cut off this dashboard or your SSH login is refused.' },
        { value: 'limit', title: 'Limit', body: 'Let each address make only so many new connections, and drop the rest. Stops password guessing.' },
      ];
  const quick = full && !out && !editing ? presets(st) : [];

  return (
    <form ref={formRef} className="fw-form" onSubmit={onSubmit} aria-label={editing ? 'Change the rule' : 'Add a rule'}>
      <div className="fw-form-head">
        <strong>{editing ? 'Change the rule' : 'New rule'}</strong>
        {editing && <span className="fw-editing small ink-3">Changing: {describeRule(editing)}</span>}
      </div>

      {quick.length > 0 && (
        <div className="fw-quick" role="group" aria-label="Quick rules">
          <span className="small ink-3">Quick:</span>
          {quick.map((p) => (
            <button key={p.label} type="button" className="chip" onClick={() => setDraft(startDraft({ action: 'allow', port: p.port, proto: p.proto, note: p.label }))}>
              Open {p.label}
            </button>
          ))}
        </div>
      )}

      {full && !editing && (
        <div className="fw-step">
          <span className="fw-step-label">Traffic</span>
          <Seg
            label="Traffic"
            value={dir}
            onChange={(d) => {
              setDir(d);
              if (d === 'out' && draft.action === 'limit') set({ action: '' });
            }}
            options={[
              { value: 'in', label: 'Coming in' },
              { value: 'out', label: 'Going out' },
            ]}
          />
        </div>
      )}

      <div className="fw-step">
        <span className="fw-step-label">Do</span>
        <div className={`fw-actions n${actions.length}`} role="radiogroup" aria-label="What the rule does">
          {actions.map((a) => (
            <button
              key={a.value}
              type="button"
              role="radio"
              aria-checked={draft.action === a.value}
              className={`fw-choice fw-act-${a.value}${draft.action === a.value ? ' on' : ''}`}
              onClick={() => set({ action: a.value, ...(a.value === 'limit' && draft.ports === 'all' ? { ports: 'some' as const } : {}) })}
            >
              <strong>{a.title}</strong>
              <span>{a.body}</span>
            </button>
          ))}
        </div>
      </div>

      <fieldset className="fw-step">
        <legend className="fw-step-label">{who}</legend>
        <div className="fw-pick">
          <label className="fw-radio">
            <input type="radio" name="fw-who" checked={draft.who === 'any'} onChange={() => set({ who: 'any' })} />
            {out ? 'Anywhere' : 'Anyone'}
          </label>
          <label className="fw-radio">
            <input type="radio" name="fw-who" checked={draft.who === 'some'} onChange={() => set({ who: 'some' })} />
            Only
          </label>
          <input
            className="field mono"
            name="who"
            value={draft.source}
            onChange={(e) => set({ source: e.target.value, ...(e.target.value.trim() ? { who: 'some' as const } : {}) })}
            onFocus={() => set({ who: 'some' })}
            placeholder="203.0.113.7 or 198.51.100.0/24"
            aria-label={who}
            spellCheck={false}
          />
        </div>
      </fieldset>

      <fieldset className="fw-step">
        <legend className="fw-step-label">Port</legend>
        <div className="fw-pick">
          <label className="fw-radio">
            <input type="radio" name="fw-ports" checked={draft.ports === 'all'} disabled={draft.action === 'limit'} onChange={() => set({ ports: 'all' })} />
            Every port
          </label>
          <label className="fw-radio">
            <input type="radio" name="fw-ports" checked={draft.ports === 'some'} onChange={() => set({ ports: 'some' })} />
            Only
          </label>
          <input
            className="field mono fw-port-input"
            value={draft.port}
            onChange={(e) => set({ port: e.target.value, ...(e.target.value.trim() ? { ports: 'some' as const } : {}) })}
            onFocus={() => set({ ports: 'some' })}
            placeholder="22, 80,443 or 8000-8100"
            aria-label="Port"
            spellCheck={false}
          />
          <select
            className="field fw-proto"
            value={draft.ports === 'some' && draft.port.trim() ? draft.proto : ''}
            onChange={(e) => set({ proto: e.target.value as Draft['proto'] })}
            aria-label="Protocol"
            disabled={draft.ports !== 'some' || !draft.port.trim()}
          >
            <option value="">tcp and udp</option>
            <option value="tcp">tcp only</option>
            <option value="udp">udp only</option>
          </select>
        </div>
      </fieldset>

      {draft.action === 'limit' && (
        <div className="fw-step">
          <span className="fw-step-label">At most</span>
          <div className="fw-pick">
            <input className="field mono fw-count" value={draft.count} onChange={(e) => set({ count: e.target.value.replace(/\D/g, '') })} aria-label="How many" inputMode="numeric" />
            <select className="field" value={draft.unit} onChange={(e) => set({ unit: e.target.value as Draft['unit'] })} aria-label="Per">
              <option value="sec">a second</option>
              <option value="min">a minute</option>
              <option value="hour">an hour</option>
            </select>
            <span className="small ink-3">new connections (tcp) or packets (udp) from each address</span>
          </div>
        </div>
      )}

      <label className="fw-step">
        <span className="fw-step-label">Note</span>
        <input className="field" value={draft.note} onChange={(e) => set({ note: e.target.value })} placeholder="optional: why, for later" aria-label="Note" maxLength={200} />
      </label>

      <div className="fw-form-foot">
        <p className={`fw-preview${gap ? ' unfinished' : ''}`} aria-live="polite" id="fw-preview">
          {gap || (
            <>
              <span className="ink-3">This rule will </span>
              {describeRule(ruleOf(draft, dir) as FirewallRule)
                .replace(/^\w/, (c) => c.toLowerCase())}
              .
            </>
          )}
        </p>
        <div className="fw-submit">
          <button type="button" className="btn-quiet" onClick={onCancel}>
            Cancel
          </button>
          <button className="btn" type="submit" disabled={busy || !!gap} aria-describedby="fw-preview">
            {busy ? 'Saving…' : editing ? 'Save' : 'Add rule'}
          </button>
        </div>
      </div>
      {error && <p className="form-error fw-error">{error}</p>}
      {out && <p className="form-help fw-help">An outgoing rule looks at new connections only, so replies to what reaches this machine always go out.</p>}
    </form>
  );
}

// DefaultChoice says what happens to incoming traffic no rule decides, and changes it: dropping it
// first asks which open ports to keep open, then goes back by itself unless kept within a minute.
function DefaultChoice({
  st,
  target,
  busy,
  run,
  asking,
  setAsking,
  closing,
  disabled,
}: {
  st: FirewallStatus;
  target: FirewallTarget;
  busy: string;
  run: (key: string, change: () => Promise<FirewallStatus | undefined>, ok?: { title: string; body?: string }) => Promise<boolean>;
  asking: boolean;
  setAsking: (v: boolean) => void;
  closing: FirewallPort[];
  disabled: boolean;
}) {
  const now = useNow(1000);
  const tool = TOOL[st.tool];
  const def = st.default ?? '';
  const [keepOpen, setKeepOpen] = useState<Set<string>>(new Set());
  const key = (p: FirewallPort) => `${p.port}/${p.proto}`;
  useEffect(() => {
    if (asking) setKeepOpen(new Set(closing.map(key)));
    // Ticked again each time it asks.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [asking]);
  const open = st.open ?? [];
  const openWords = useMemo(() => {
    const by = new Map<string, number[]>();
    for (const o of open) by.set(o.what, [...(by.get(o.what) ?? []), o.port]);
    return [...by].map(([what, ports]) => `${what} ${ports.join(', ')}`).join(' · ');
  }, [open]);

  const choose = async (next: '' | 'drop') => {
    if (next === def || disabled) return;
    if (next === 'drop' && closing.length > 0 && !asking) {
      setAsking(true);
      return;
    }
    setAsking(false);
    if (next === 'drop') {
      for (const p of closing.filter((p) => keepOpen.has(key(p)))) {
        const ok = await run('default', () => target.add({ action: 'allow', port: String(p.port), proto: p.proto, note: p.program ? `open for ${p.program}` : 'kept open' }));
        if (!ok) return;
      }
    }
    await run('default', () => target.setDefault(next));
  };

  const left = st.pending ? Math.max(0, Math.round(st.pending.until - now)) : 0;
  return (
    <div className="fw-default">
      <span className="label">Incoming traffic no rule decides</span>
      <div className="fw-default-choices" role="radiogroup" aria-label="Incoming traffic no rule decides">
        <button type="button" role="radio" aria-checked={def === ''} className={`fw-choice${def === '' ? ' on' : ''}`} disabled={disabled || !!busy} onClick={() => choose('')}>
          <strong>Leave it to {tool}</strong>
          <span>{st.policy === 'DROP' ? `${tool} drops it, unless its own rules let it in.` : st.policy === 'ACCEPT' ? `${tool} lets it in, unless its own rules drop it.` : `${tool} decides.`}</span>
        </button>
        <button type="button" role="radio" aria-checked={def === 'drop'} className={`fw-choice${def === 'drop' ? ' on' : ''}`} disabled={disabled || !!busy} onClick={() => choose('drop')}>
          <strong>Drop it</strong>
          <span>Only what a rule allows gets in, besides what is always open.</span>
        </button>
      </div>
      <p className="form-help fw-always">
        Always open: {openWords || 'nothing'}
        {openWords ? ', ' : ''}ping, and replies to what this machine sent.
      </p>

      {asking && (
        <div className="fw-ask" role="group" aria-label="Ports to keep open">
          <p>
            <strong>{plural(closing.length, 'open port')} no rule lets in.</strong> Dropping what no rule allows would close {closing.length === 1 ? 'it' : 'them'}. Tick those to keep open: Nightjar
            adds an allow rule for each.
          </p>
          <ul>
            {closing.map((p) => (
              <li key={key(p)}>
                <label>
                  <input
                    type="checkbox"
                    checked={keepOpen.has(key(p))}
                    onChange={(e) => {
                      const next = new Set(keepOpen);
                      if (e.target.checked) next.add(key(p));
                      else next.delete(key(p));
                      setKeepOpen(next);
                    }}
                  />
                  <span className="mono">
                    {p.port}/{p.proto}
                  </span>
                  <span className="ink-2">{p.program || '—'}</span>
                </label>
              </li>
            ))}
          </ul>
          <div className="fw-ask-acts">
            <button type="button" className="btn-quiet" onClick={() => setAsking(false)}>
              Cancel
            </button>
            <button type="button" className="btn" disabled={!!busy} onClick={() => choose('drop')}>
              {busy === 'default' ? 'Changing…' : keepOpen.size ? `Keep ${keepOpen.size} open, drop the rest` : 'Drop the rest'}
            </button>
          </div>
        </div>
      )}

      {st.pending && (
        <div className="fw-pending" role="alert">
          <p>
            <strong>{st.pending.default === 'drop' ? 'Now dropping what no rule allows.' : `Now leaving it to ${tool}.`}</strong> If this page still answers, keep it. It goes back by itself in{' '}
            <span className="num">{left}s</span>, so a change that cut you off undoes itself.
          </p>
          <div className="fw-ask-acts">
            <button type="button" className="btn-quiet" disabled={!!busy} onClick={() => run('keep', () => target.keep(false), { title: `Put back · ${target.name}` })}>
              Undo
            </button>
            <button
              type="button"
              className="btn"
              disabled={!!busy}
              onClick={() => run('keep', () => target.keep(true), { title: `Kept · ${target.name}`, body: st.pending?.default === 'drop' ? 'Only what a rule allows gets in.' : undefined })}
            >
              Keep it
            </button>
          </div>
        </div>
      )}
    </div>
  );
}

// OpenPorts lists the ports something listens on, whether the network can reach each, and a shortcut
// to a rule for it.
function OpenPorts({ st, canChange, ruleFor }: { st: FirewallStatus; canChange: boolean; ruleFor: (p: FirewallPort) => void }) {
  const dropping = st.default === 'drop';
  return (
    <div className="fw-ports">
      <span className="label">Open ports</span>
      <ul>
        {st.listening.map((p) => (
          <li key={`${p.proto}${p.port}`} className={p.local ? 'local' : dropping && !p.allowed ? 'closed' : ''}>
            <span className="mono num">
              {p.port}/{p.proto}
            </span>
            <span className="ink-2 min0 truncate">{p.program || '—'}</span>
            {p.local ? (
              <span className="small ink-3">this machine only</span>
            ) : (
              <span className="fw-port-end">
                {st.full && <span className={`small ${p.allowed ? 'ink-3' : dropping ? 'fw-closed' : 'ink-3'}`}>{p.allowed ? 'open to anyone' : dropping ? 'closed by the default' : ''}</span>}
                {canChange && (
                  <button type="button" className="btn-quiet fw-port-rule" onClick={() => ruleFor(p)} aria-label={`Add a rule for ${p.port}/${p.proto}`}>
                    Add rule
                  </button>
                )}
              </span>
            )}
          </li>
        ))}
      </ul>
    </div>
  );
}

// Dropped shows the latest dropped packets the kernel logged, and who was dropped most, with a
// shortcut to block them.
function Dropped({ st, rules, canChange, block }: { st: FirewallStatus; rules: FirewallRule[]; canChange: boolean; block: (source: string) => void }) {
  const now = useNow(30000);
  const [all, setAll] = useState(false);
  const drops = st.drops ?? [];
  const byId = useMemo(() => new Map(rules.map((r) => [r.id, r])), [rules]);
  const top = useMemo(() => {
    const n = new Map<string, number>();
    for (const d of drops) if (d.direction !== 'out') n.set(d.source, (n.get(d.source) ?? 0) + 1);
    return [...n]
      .filter(([, c]) => c > 1)
      .sort((a, b) => b[1] - a[1])
      .slice(0, 5);
  }, [drops]);
  const blocked = (source: string) => rules.some((r) => r.action === 'block' && !r.paused && r.direction !== 'out' && !r.port && !!r.source && within(source, r.source));
  const why = (d: FirewallDrop) => {
    if (d.rule === 'default') return 'no rule allowed it';
    const r = byId.get(d.rule);
    return r ? describeRule(r) : 'a rule since removed';
  };
  const shown = all ? drops : drops.slice(0, 12);
  const defaultHits = st.hits?.default?.packets ?? 0;
  return (
    <Section label="Dropped lately" aside={<span className="small ink-3">a sample, at most 10 a minute for each rule</span>}>
      <div className="fw">
        {st.drops_note && <p className="note">{st.drops_note}</p>}
        {defaultHits > 0 && (
          <p className="small ink-2 fw-default-hits">
            The default dropped {defaultHits.toLocaleString()} packets ({formatBytes(st.hits!.default.bytes)}){st.since ? ` since ${formatAgo(st.since, now)}` : ''}.
          </p>
        )}
        {drops.length === 0 ? (
          !st.drops_note && <p className="note">Nothing dropped lately. Blocks and the default write what they drop here.</p>
        ) : (
          <>
            {top.length > 0 && (
              <div className="fw-top">
                <span className="label">Dropped most</span>
                <ul>
                  {top.map(([source, count]) => (
                    <li key={source}>
                      <span className="mono min0 truncate">{source}</span>
                      <span className="small ink-3 num">{count}×</span>
                      {canChange && !blocked(source) ? (
                        <button type="button" className="btn-quiet" onClick={() => block(source)} aria-label={`Block ${source}`}>
                          Block
                        </button>
                      ) : (
                        <span className="small ink-3">{blocked(source) ? 'blocked' : ''}</span>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <ul className="fw-drops">
              {shown.map((d, i) => (
                <li key={`${d.at}-${i}`}>
                  <span className="small ink-3 num fw-drop-at">{formatAgo(d.at, now)}</span>
                  <span className="fw-drop-what min0">
                    <span className="mono">{d.direction === 'out' ? `→ ${d.dest}` : d.source}</span>
                    <span className="ink-2">
                      {' '}
                      {d.proto ?? ''}
                      {d.port ? ` ${d.port}` : ''}
                    </span>
                    <span className="fw-drop-why">{why(d)}</span>
                  </span>
                </li>
              ))}
            </ul>
            {drops.length > 12 && (
              <button type="button" className="btn-quiet fw-more" onClick={() => setAll(!all)}>
                {all ? 'Show fewer' : `Show all ${drops.length}`}
              </button>
            )}
          </>
        )}
      </div>
    </Section>
  );
}
