import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { api, errorText } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import type { AgentFirewall, FirewallDrop, FirewallLock, FirewallPort, FirewallRule, FirewallStatus, SSHAccess } from '../api/types';
import { BlockSSH, SSHPending, SSHState, useSSHAccess } from './BlockSSH';
import { formatAgo, formatBytes, plural } from '../format';
import { useNow } from '../hooks/useNow';
import { Pager, usePaged } from './Pager';
import { useToast } from './Toasts';
import { Section, Seg } from './ui';

// The firewall, managed from the dashboard: what happens to traffic no rule decides, the rules in
// order (the first that matches decides), what they caught and dropped, the open ports, and the
// machine's own rules. The same for this machine and for a server through its agent.

// TOOL says which firewall the machine runs, in words.
export const TOOL: Record<FirewallStatus['tool'], string> = { ufw: 'ufw', firewalld: 'firewalld', iptables: 'plain iptables' };

const ACTION: Record<FirewallRule['action'], string> = { allow: 'Allow', block: 'Block', limit: 'Limit' };

// ActionIcon marks what a rule does at a glance: a check lets in, a bar across blocks, a gauge limits.
const ACTION_PATH: Record<FirewallRule['action'], string> = {
  allow: 'M5 12.5l4.5 4.5L19 7.5',
  block: 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM5.6 5.6l12.8 12.8',
  limit: 'M4 15a8 8 0 0 1 16 0M12 15l4-5',
};
const ActionIcon = ({ action }: { action: FirewallRule['action'] }) => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d={ACTION_PATH[action]} />
  </svg>
);

// caught is how many packets a rule (or the default, by "default") has caught.
const caught = (hits: FirewallStatus['hits'], id: string) => hits?.[id]?.packets ?? 0;

// UNIT says a rate's unit in words: "6/min" is "6 a minute".
const UNIT: Record<string, string> = { sec: 'a second', min: 'a minute', hour: 'an hour' };
const rateWords = (rate?: string) => {
  const [n, unit] = (rate ?? '').split('/');
  return `${n} ${UNIT[unit] ?? unit}`;
};

// portWords says a rule's ports: "tcp port 22", "ports 80, 443", "port 8000–8100", "every port".
const portWords = (r: Pick<FirewallRule, 'port' | 'proto'>) => {
  if (!r.port) return 'every port';
  const list = r.port.replaceAll(':', '–').replaceAll(',', ', ');
  return `${r.proto ? `${r.proto} ` : ''}${r.port.includes(',') ? 'ports' : 'port'} ${list}`;
};

// sourceWords names who a rule is for: "Nightjar" for the address Nightjar reaches a server from.
const sourceWords = (source: string, dashboard?: string) => (source && source === dashboard ? 'Nightjar' : source || 'anyone');

// serviceName names what holds a port in a word: its systemd service ("postgresql"), else its program.
export const serviceName = (s: { program?: string; exe?: string; unit?: string }) => (s.unit ? s.unit.replace(/\.service$/, '') : s.program || s.exe?.split('/').pop() || 'another program');

// serviceTitle is all that is known of what holds a port, for a tooltip.
const serviceTitle = (s: { program?: string; exe?: string; unit?: string }) => [s.program, s.exe, s.unit].filter(Boolean).join(' · ');

// holderName names the program holding a locked port instead of its service: the program as it runs,
// since in the service's own unit it is still not the service.
const holderName = (h: NonNullable<FirewallLock['holder']>) => h.program || h.exe?.split('/').pop() || 'another program';

// lockFor is the lock on a port, if there is one.
const lockFor = (st: FirewallStatus, p: Pick<FirewallPort, 'port' | 'proto'>) => st.locks?.find((l) => l.port === p.port && l.proto === p.proto);

// LockIcon marks a port locked to its service.
const LockIcon = () => (
  <svg viewBox="0 0 24 24" width="13" height="13" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <rect x="5" y="11" width="14" height="10" rx="2" />
    <path d="M8 11V7a4 4 0 0 1 8 0v4" />
  </svg>
);

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
  // Locks pin a port to the service on it now (lock), trust the program holding it now (relock), or
  // let it go (unlock). A target without them (as in tests) offers no locking.
  lock?: (port: number, proto: string) => Promise<FirewallStatus | undefined>;
  relock?: (id: string) => Promise<FirewallStatus | undefined>;
  unlock?: (id: string) => Promise<FirewallStatus | undefined>;
  // ssh is how the same machine lets people log in over SSH, for Block SSH's key path. A target
  // without it (as in tests) offers only the address path.
  ssh?: SSHTarget;
}

// SSHTarget is a machine's SSH logins: add a key to an account, turn passwords off (they come back by
// themselves unless kept) or on again. Each call answers with the logins after it.
export interface SSHTarget {
  load: () => Promise<SSHAccess>;
  addKey: (user: string, key: string) => Promise<SSHAccess>;
  keysOnly: (on: boolean, user?: string) => Promise<SSHAccess>;
  keep: (keep: boolean) => Promise<SSHAccess>;
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
  lock: (port, proto) => api.lockPort(port, proto).then((r) => r.firewall),
  relock: (id) => api.relockPort(id).then((r) => r.firewall),
  unlock: (id) => api.unlockPort(id).then((r) => r.firewall),
  ssh: { load: api.sshd, addKey: api.addSSHDKey, keysOnly: api.setKeysOnly, keep: api.keepKeysOnly },
};

// withDashboard puts the address the agent saw Nightjar ask from onto its firewall, for the form's
// "Nightjar" choice.
const withDashboard = (v: AgentFirewall): FirewallStatus => (v.dashboard ? { ...v.firewall, dashboard: v.dashboard } : v.firewall);

export const hostFirewall = (h: { id: string; name: string }): FirewallTarget => ({
  key: h.id,
  name: h.name,
  load: () => api.hostFirewall(h.id).then(withDashboard),
  add: (rule) => api.addHostRule(h.id, rule).then(withDashboard),
  update: (id, rule) => api.updateHostRule(h.id, id, rule).then(withDashboard),
  remove: (id) => api.removeHostRule(h.id, id).then(withDashboard),
  order: (ids) => api.orderHostRules(h.id, ids).then(withDashboard),
  setDefault: (def) => api.setHostDefault(h.id, def).then(withDashboard),
  keep: (keep) => api.keepHostDefault(h.id, keep).then(withDashboard),
  lock: (port, proto) => api.lockHostPort(h.id, port, proto).then(withDashboard),
  relock: (id) => api.relockHostPort(h.id, id).then(withDashboard),
  unlock: (id) => api.unlockHostPort(h.id, id).then(withDashboard),
  ssh: {
    load: () => api.hostSSHD(h.id),
    addKey: (user, key) => api.addHostSSHDKey(h.id, user, key),
    keysOnly: (on, user) => api.setHostKeysOnly(h.id, on, user),
    keep: (keep) => api.keepHostKeysOnly(h.id, keep),
  },
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
  // alone also blocks everyone else on the port, for a new allow from one address: the allow is then
  // the only way in ("only Nightjar may reach 9101").
  alone: boolean;
}

const EMPTY: Draft = { action: '', who: 'any', source: '', ports: 'all', port: '', proto: '', count: '6', unit: 'min', note: '', alone: false };

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
    alone: false,
  };
};

// keepsOthersOut says whether a draft can also block everyone else: a new incoming allow from one
// address or network, to given ports.
const keepsOthersOut = (draft: Draft, dir: 'in' | 'out', editing: boolean) =>
  !editing && dir === 'in' && draft.action === 'allow' && draft.who === 'some' && !!draft.source.trim() && draft.ports === 'some' && !!draft.port.trim();

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

// hitWords says what a rule caught: "dropped 1,204 · 82 KB", "let in 30" ("let out" for an outgoing rule).
function hitWords(r: FirewallRule, hits: FirewallStatus['hits']): string {
  const h = hits?.[r.id];
  if (!h || !h.packets) return 'nothing caught yet';
  const n = `${h.packets.toLocaleString()} · ${formatBytes(h.bytes)}`;
  return r.action === 'allow' ? `let ${r.direction === 'out' ? 'out' : 'in'} ${n}` : r.action === 'limit' ? `dropped ${n} over the limit` : `dropped ${n}`;
}

// withLists fills in the open ports when an answer has none: an agent from before they were read, or
// one that could read nothing from /proc/net, sends null.
// It also notes how far the machine's clock is from the browser's, for countdowns and times on it.
const withLists = (s: FirewallStatus | undefined): FirewallStatus | undefined => s && { ...s, listening: s.listening ?? [], skew: s.now ? s.now - Date.now() / 1000 : 0 };

// browserTime is a time on the machine's clock on the browser's, for counting down and saying how long ago.
const browserTime = (st: Pick<FirewallStatus, 'skew'>, t: number) => t - (st.skew ?? 0);

// FirewallManager is a machine's whole firewall. It refreshes itself every 15 seconds while shown,
// for what the rules caught.
export function FirewallManager({ target }: { target: FirewallTarget }) {
  const notify = useToast();
  // A viewer sees the rules, without what would change them.
  const canEdit = useCanEdit();
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
  // blocking is whether the Block SSH panel is open; it and the rule form take turns.
  const [blocking, setBlocking] = useState(false);
  // securing is which Secure preset's box is open (email, web); like Block SSH, it takes the form's place.
  const [securing, setSecuring] = useState<SecureKind>();
  // SSH logins are the admin's to see: a viewer is not asked for them, which the server would refuse.
  const ssh = useSSHAccess(canEdit ? target.ssh : undefined, target.name);
  // removing is the rule whose Remove is waiting on a yes: one click should not drop a rule.
  const [removing, setRemoving] = useState('');
  const busyRef = useRef('');
  busyRef.current = busy;
  // epoch moves on when a change starts or ends, and with each machine. A read that went out before
  // then may predate the change, and would bring back the old firewall (dropping the Keep/Undo banner
  // while the revert still counts down), so its reply is let go; the next read brings the new one.
  const epoch = useRef(0);

  const refresh = useCallback(() => {
    const at = epoch.current;
    return target.load().then(
      (s) => {
        if (epoch.current !== at) return;
        setOff(!s);
        setSt(withLists(s));
        setError('');
      },
      (e) => {
        if (epoch.current === at) setError(errorText(e));
      },
    );
  }, [target]);
  useEffect(() => {
    epoch.current++;
    setSt(undefined);
    setError('');
    setDraft(EMPTY);
    setEditing(undefined);
    setAdding(false);
    setAsking(false);
    setBlocking(false);
    setRemoving('');
    refresh();
    const t = window.setInterval(() => {
      if (document.visibilityState === 'visible' && !busyRef.current) refresh();
    }, 15000);
    return () => window.clearInterval(t);
  }, [refresh]);

  // run makes a change, shows the firewall after it, and says how it went.
  const run = async (key: string, change: () => Promise<FirewallStatus | undefined>, ok?: { title: string; body?: string }) => {
    setBusy(key);
    epoch.current++;
    try {
      const s = await change();
      if (s) {
        setSt(withLists(s));
        setError('');
      }
      if (ok) notify({ tone: 'ok', ...ok });
      return true;
    } catch (err) {
      notify({ tone: 'error', title: `Could not change the firewall · ${target.name}`, body: errorText(err) });
      return false;
    } finally {
      epoch.current++;
      setBusy('');
    }
  };

  const form = useRef<HTMLFormElement>(null);
  const toForm = () => form.current?.scrollIntoView?.({ behavior: 'smooth', block: 'start' });
  // ruleFor opens the form on a new rule: empty from Add a rule, a port's from the open ports, or a
  // block of an address that kept being dropped.
  const ruleFor = (d: Partial<Draft>, direction: 'in' | 'out' = 'in') => {
    setBlocking(false);
    setSecuring(undefined);
    setEditing(undefined);
    setFormError('');
    setDir(direction);
    setDraft(startDraft(d));
    setAdding(true);
    setTimeout(toForm);
  };
  const edit = (r: FirewallRule) => {
    setBlocking(false);
    setSecuring(undefined);
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
    epoch.current++;
    const rule: Partial<FirewallRule> = { ...ruleOf(draft, dir), paused: editing?.paused };
    const alone = draft.alone && keepsOthersOut(draft, dir, !!editing);
    try {
      let s = editing ? await target.update(editing.id, rule) : await target.add(rule);
      if (s) {
        setSt(withLists(s));
        setError('');
      }
      if (alone) {
        // The block for everyone else goes in behind the allow (the firewall puts it after an allow it
        // is an exception to), so the allow is the only way in. Should it be refused, the allow stays.
        const rest: Partial<FirewallRule> = { action: 'block', port: rule.port, proto: rule.proto, note: `everyone but ${sourceWords(rule.source ?? '', st?.dashboard)}` };
        try {
          s = await target.add(rest);
          if (s) setSt(withLists(s));
        } catch (err) {
          notify({ tone: 'error', title: `Added the allow, but not the block for everyone else · ${target.name}`, body: errorText(err) });
          setDraft(EMPTY);
          setAdding(false);
          return;
        }
        notify({ tone: 'ok', title: `Rules added · ${target.name}`, body: `Only ${sourceWords(rule.source ?? '', st?.dashboard)} may reach ${portWords(rule as FirewallRule)}.` });
        setDraft(EMPTY);
        setAdding(false);
        return;
      }
      notify({ tone: 'ok', title: `${editing ? 'Rule changed' : 'Rule added'} · ${target.name}`, body: describeRule(rule as FirewallRule) });
      setDraft(EMPTY);
      setEditing(undefined);
      setAdding(false);
    } catch (err) {
      setFormError(errorText(err));
    } finally {
      epoch.current++;
      setBusy('');
    }
  };

  // addForSSH adds one of Block SSH's rules the way run makes a change, but leaves a refusal to the
  // panel to show as it is.
  const addForSSH = async (rule: Partial<FirewallRule>) => {
    setBusy('ssh');
    epoch.current++;
    try {
      const s = await target.add(rule);
      if (s) {
        setSt(withLists(s));
        setError('');
      }
    } finally {
      epoch.current++;
      setBusy('');
    }
  };
  const openBlock = () => {
    cancelEdit();
    setDir('in');
    setSecuring(undefined);
    setBlocking(true);
  };
  const openSecure = (kind: SecureKind) => {
    cancelEdit();
    setDir('in');
    setBlocking(false);
    setSecuring(kind);
  };

  const rules = st?.rules ?? [];
  const shown = rules.filter((r) => (dir === 'out') === (r.direction === 'out'));
  const rulePages = usePaged(shown, 10, dir);
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

  // A failed read with nothing read yet is all there is to show; after one, the last firewall read
  // stays (with the Keep/Undo banner and a rule being typed) under a note that it could not be read.
  if (error && !st)
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
  const canChange = !st.problem && canEdit;
  const tool = TOOL[st.tool];
  const aside = `${tool}${st.policy ? ` · on its own ${st.policy === 'DROP' ? 'drops' : 'lets in'} what no rule decides` : ''}`;
  const closing = st.listening.filter((p) => !p.local && !p.allowed);
  const inCount = rules.filter((r) => r.direction !== 'out').length;
  const outCount = rules.length - inCount;

  // Hit bars are on a log scale: an allow rule lets in tens of thousands where a block drops hundreds,
  // and both should still show.
  const busiest = Math.log1p(Math.max(1, ...shown.map((r) => caught(st.hits, r.id))));
  return (
    <>
      {error && (
        <p className="alert" role="alert">
          Could not read the firewall again ({error}). This is how it was last read; retrying.
        </p>
      )}
      {full && <Glance st={st} rules={rules} tool={tool} />}
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
              {full && st.since ? <span className="small ink-3">counted since {formatAgo(browserTime(st, st.since))}</span> : null}
              {(dir === 'in' && ssh.access) || (canChange && !adding && !blocking) ? (
                <span className="fw-new">
                  {dir === 'in' && ssh.access && <SSHState ssh={ssh} name={target.name} />}
                  {canChange && !adding && !blocking && dir === 'in' && (
                    <button type="button" className="btn-ghost" onClick={openBlock}>
                      Block SSH…
                    </button>
                  )}
                  {canChange && !adding && !blocking && (
                    <button type="button" className="btn" onClick={() => ruleFor({}, dir)}>
                      + Add a rule
                    </button>
                  )}
                </span>
              ) : null}
            </div>
          )}

          {ssh.access?.pending && <SSHPending ssh={ssh} name={target.name} />}
          {canChange && blocking && <BlockSSH target={target} st={st} ssh={ssh} addRule={addForSSH} busy={busy === 'ssh'} onClose={() => setBlocking(false)} />}
          {canChange && securing && <SecureRules key={securing} kind={securing} name={target.name} add={addForSSH} busy={busy === 'ssh'} onClose={() => setSecuring(undefined)} />}

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
              onSecureSSH={openBlock}
              onSecure={openSecure}
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
              {/* i is the rule's place among all the rules in this direction, not just this page's. */}
              {rulePages.items.map((r, j, _, i = rulePages.offset + j) => (
                <li key={r.id} className={`fw-rule fw-is-${r.action}${r.paused ? ' paused' : ''}${editing?.id === r.id ? ' editing' : ''}`}>
                  {full && (
                    <span className="fw-move">
                      <span className="fw-order num" title="Checked in this order">
                        {i + 1}
                      </span>
                      {canEdit && (
                        <>
                          <button type="button" className="icon-btn" aria-label={`Move up: ${describeRule(r)}`} disabled={!!busy || i === 0 || !canChange} onClick={() => move(r, -1)}>
                            ↑
                          </button>
                          <button type="button" className="icon-btn" aria-label={`Move down: ${describeRule(r)}`} disabled={!!busy || i === shown.length - 1 || !canChange} onClick={() => move(r, 1)}>
                            ↓
                          </button>
                        </>
                      )}
                    </span>
                  )}
                  <span className={`fw-action ${r.action}`}>
                    <ActionIcon action={r.action} />
                    {ACTION[r.action]}
                  </span>
                  <span className="fw-rule-text min0">
                    <span className="fw-rule-head">
                      <span className="fw-rule-who mono">
                        {r.direction === 'out' && <span className="fw-to">to </span>}
                        {r.source && r.source === st.dashboard ? <span title={r.source}>Nightjar</span> : r.source || 'Anyone'}
                      </span>
                      <span className="fw-tag">{portWords(r)}</span>
                      {r.action === 'limit' && <span className="fw-tag">at most {rateWords(r.rate)} each</span>}
                    </span>
                    {r.note && <span className="fw-rule-note">{r.note}</span>}
                    {full && (
                      <span className="fw-rule-hits">
                        {!r.paused && (
                          <span className="fw-hitbar" aria-hidden="true">
                            <span style={{ width: `${(Math.log1p(caught(st.hits, r.id)) / busiest) * 100}%` }} />
                          </span>
                        )}
                        {r.paused ? 'paused: not in place' : hitWords(r, st.hits)}
                      </span>
                    )}
                  </span>
                  {canEdit && (
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
                      {removing === r.id ? (
                        <span className="confirm">
                          <span className="small ink-3">Remove?</span>
                          <button
                            type="button"
                            className="btn-quiet danger"
                            disabled={!!busy || !canChange}
                            onClick={() => run(r.id, () => target.remove(r.id), { title: `Rule removed · ${target.name}`, body: describeRule(r) }).then(() => setRemoving(''))}
                          >
                            {busy === r.id ? '…' : 'Remove'}
                          </button>
                          <button type="button" className="btn-quiet" onClick={() => setRemoving('')}>
                            Keep
                          </button>
                        </span>
                      ) : (
                        <button type="button" className="btn-quiet fw-remove" disabled={!!busy || !canChange} onClick={() => setRemoving(r.id)}>
                          Remove…
                        </button>
                      )}
                    </span>
                  )}
                </li>
              ))}
            </ul>
          )}
          <Pager paged={rulePages} label="Pages of rules" noun="rules" />

          {dir === 'in' && st.listening.length > 0 && (
            <OpenPorts
              st={st}
              canChange={canChange}
              busy={!!busy}
              ruleFor={(p) => ruleFor({ port: String(p.port), proto: p.proto })}
              lock={
                full && target.lock && Array.isArray(st.locks)
                  ? (p) =>
                      run(`lock:${p.proto}${p.port}`, () => target.lock!(p.port, p.proto), {
                        title: `Port locked · ${target.name}`,
                        body: `${p.port}/${p.proto} is locked to ${serviceName(p)}. Should anything else take it, Nightjar drops what comes to it and tells you.`,
                      })
                  : undefined
              }
            />
          )}
          {dir === 'in' && !!st.locks?.length && <PortLocks st={st} target={target} canChange={canChange} busy={busy} run={run} />}
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

// Secure presets are a few rules that set a machine up for one job safely, each said in words before
// it goes in.
export interface SecurePreset {
  title: string;
  // done is the toast's title once its rules are in: "Email secured".
  done: string;
  intro: string;
  rules: { rule: Partial<FirewallRule>; why: string }[];
  note: string;
}
export type SecureKind = 'email' | 'web';

export const SECURE: Record<SecureKind, SecurePreset> = {
  // Email: what a mail server with IMAP needs open, and nothing it shouldn't. Mail from other servers
  // arrives on 25, left open to everyone, unlimited: senders retry, and a cap would lose mail. Sending
  // (465, 587) and reading (993) go over TLS, limited per address, which cuts password guessing short
  // without bothering a mail app's few connections. Plain-text POP3 and IMAP (110, 143) are blocked,
  // so passwords never cross the network unencrypted.
  email: {
    title: 'Secure email',
    done: 'Email secured',
    intro: 'adds the rules a mail server with IMAP needs:',
    rules: [
      { rule: { action: 'allow', port: '25', proto: 'tcp', note: 'email: mail from other servers' }, why: 'Allow port 25 for everyone: mail arriving from other servers.' },
      {
        rule: { action: 'limit', port: '465,587,993', proto: 'tcp', rate: '30/min', note: 'email: sending and reading over TLS' },
        why: 'Limit ports 465, 587 and 993 to 30 new connections a minute from each address: sending and reading mail over TLS, with password guessing cut short.',
      },
      {
        rule: { action: 'block', port: '110,143', proto: 'tcp', note: 'email: plain-text POP3 and IMAP' },
        why: 'Block ports 110 and 143: plain-text POP3 and IMAP, which send passwords unencrypted.',
      },
    ],
    note: 'Webmail, if you run it, is separate: Secure web opens it.',
  },
  // Web: the site open to everyone (80 for certificates and the redirect to HTTPS, 443 over TCP and,
  // for HTTP/3, UDP), and the databases and caches behind a web app shut to the internet, where an
  // open one is the classic way a site's data leaks. Not limited: a browser opens many connections,
  // and visitors behind one address (a school, a mobile carrier) would be cut off; floods are the
  // attack defense's.
  web: {
    title: 'Secure web',
    done: 'Web secured',
    intro: 'adds the rules a web server needs:',
    rules: [
      {
        rule: { action: 'allow', port: '80,443', proto: 'tcp', note: 'web: HTTP and HTTPS' },
        why: 'Allow ports 80 and 443 for everyone: the site over HTTPS, and plain HTTP for certificate renewals and the redirect to HTTPS.',
      },
      { rule: { action: 'allow', port: '443', proto: 'udp', note: 'web: HTTP/3' }, why: 'Allow UDP port 443 for everyone: HTTP/3, which faster browsers use.' },
      {
        rule: { action: 'block', port: '3306,5432,6379,9200,11211,27017', proto: 'tcp', note: 'web: databases and caches' },
        why: 'Block MySQL (3306), PostgreSQL (5432), Redis (6379), Elasticsearch (9200), Memcached (11211) and MongoDB (27017): what a web app keeps behind it, which should never face the internet.',
      },
    ],
    note: 'Another machine of yours that uses one of those databases needs an allow for its address: add it, and it goes above this block by itself.',
  },
};

// SecureRules says what a preset adds and adds it, a rule at a time. One already there is fine; a
// refusal stops it and says how far it got, leaving the rules added so far in place.
function SecureRules({ kind, name, add, busy, onClose }: { kind: SecureKind; name: string; add: (rule: Partial<FirewallRule>) => Promise<void>; busy: boolean; onClose: () => void }) {
  const preset = SECURE[kind];
  const notify = useToast();
  const [error, setError] = useState('');
  const go = async () => {
    setError('');
    let added = 0;
    for (const { rule } of preset.rules) {
      try {
        await add(rule);
        added++;
      } catch (err) {
        const text = errorText(err);
        if (/already a rule like that/i.test(text)) continue;
        setError(added ? `Added ${plural(added, 'rule')}, then stopped: ${text}` : text);
        return;
      }
    }
    notify({ tone: 'ok', title: `${preset.done} · ${name}`, body: added ? `${plural(added, 'rule')} added.` : 'Its rules were all in place already.' });
    onClose();
  };
  return (
    <div className="fw-ask fw-secure" role="group" aria-label={preset.title}>
      <p>
        <strong>{preset.title}</strong> {preset.intro}
      </p>
      <ul>
        {preset.rules.map(({ why }) => (
          <li key={why}>{why}</li>
        ))}
      </ul>
      <p className="small ink-3">{preset.note}</p>
      {error && (
        <p className="alert" role="alert">
          {error}
        </p>
      )}
      <div className="fw-ask-acts">
        <button type="button" className="btn-quiet" onClick={onClose} disabled={busy}>
          Cancel
        </button>
        <button type="button" className="btn" onClick={go} disabled={busy}>
          {busy ? 'Adding…' : 'Add these rules'}
        </button>
      </div>
    </div>
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
  onSecureSSH,
  onSecure,
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
  // onSecureSSH opens Block SSH in the form's place, from the quick row's "Secure: SSH…".
  onSecureSSH?: () => void;
  // onSecure opens a Secure preset (email, web) in the form's place.
  onSecure?: (kind: SecureKind) => void;
}) {
  const set = (d: Partial<Draft>) => setDraft({ ...draft, ...d });
  const out = dir === 'out';
  // nightjar is the address a server's firewall sees Nightjar ask from (none for this machine's own).
  const nightjar = out ? undefined : st.dashboard;
  const fromNightjar = !!nightjar && draft.who === 'some' && draft.source.trim() === nightjar;
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
  // The agent's port on a server, which only Nightjar needs to reach: the kept-open port named for it.
  const agentPort = nightjar ? st.open?.find((o) => /agent/i.test(o.what))?.port : undefined;
  const secure = !out && !editing && (onSecureSSH || onSecure || agentPort);

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
      {secure && (
        <div className="fw-quick" role="group" aria-label="Secure">
          <span className="small ink-3">Secure:</span>
          {onSecureSSH && (
            <button type="button" className="chip" onClick={onSecureSSH} title="Let in only an address, or only key logins">
              SSH…
            </button>
          )}
          {agentPort && nightjar && (
            <button
              type="button"
              className="chip"
              title={`Let only Nightjar (${nightjar}) reach the agent on port ${agentPort}`}
              onClick={() => setDraft(startDraft({ action: 'allow', source: nightjar, port: String(agentPort), proto: 'tcp', note: 'Nightjar only', alone: true }))}
            >
              Agent
            </button>
          )}
          {onSecure && (
            <>
              <button type="button" className="chip" onClick={() => onSecure('web')} title="The rules a web server needs: the site open, its databases shut">
                Web…
              </button>
              <button type="button" className="chip" onClick={() => onSecure('email')} title="The rules a mail server with IMAP needs, done safely">
                Email…
              </button>
            </>
          )}
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
          {nightjar && (
            // A server's firewall: Nightjar itself, by the address that server sees it ask from. It also
            // ticks "keep everyone else out", since letting only Nightjar in is what it is for.
            <label className="fw-radio" title={`The address this server sees Nightjar ask from: ${nightjar}`}>
              <input type="radio" name="fw-who" checked={fromNightjar} onChange={() => set({ who: 'some', source: nightjar, alone: true })} />
              Nightjar <span className="mono small ink-3">{nightjar}</span>
            </label>
          )}
          <label className="fw-radio">
            <input type="radio" name="fw-who" checked={draft.who === 'some' && !fromNightjar} onChange={() => set({ who: 'some', ...(fromNightjar ? { source: '', alone: false } : {}) })} />
            Only
          </label>
          <input
            className="field mono"
            name="who"
            value={fromNightjar ? '' : draft.source}
            onChange={(e) => set({ source: e.target.value, ...(e.target.value.trim() ? { who: 'some' as const } : {}) })}
            onFocus={() => set({ who: 'some', ...(fromNightjar ? { source: '', alone: false } : {}) })}
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

      {keepsOthersOut(draft, dir, !!editing) && (
        <label className="fw-step fw-alone">
          <span className="fw-step-label" aria-hidden="true" />
          <span className="fw-radio">
            <input type="checkbox" className="select-box" checked={draft.alone} onChange={(e) => set({ alone: e.target.checked })} />
            Keep everyone else out: also block {portWords(ruleOf(draft, dir) as FirewallRule)} for everyone but {sourceWords(draft.source.trim(), nightjar)}
          </span>
        </label>
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
              {describeRule({ ...(ruleOf(draft, dir) as FirewallRule), source: fromNightjar ? 'Nightjar' : ruleOf(draft, dir).source }).replace(/^\w/, (c) => c.toLowerCase())}
              {draft.alone && keepsOthersOut(draft, dir, !!editing) ? ', and block everyone else there' : ''}.
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

// Glance is the firewall in four figures: what happens to traffic no rule decides, the rules in
// place, what was dropped, and how many ports anyone can reach.
function Glance({ st, rules, tool }: { st: FirewallStatus; rules: FirewallRule[]; tool: string }) {
  const dropping = st.default === 'drop' || (!st.default && st.policy === 'DROP');
  const live = rules.filter((r) => !r.paused);
  const paused = rules.length - live.length;
  const ins = live.filter((r) => r.direction !== 'out').length;
  const stopped = rules.filter((r) => r.action !== 'allow');
  const dropped = caught(st.hits, 'default') + stopped.reduce((n, r) => n + caught(st.hits, r.id), 0);
  const droppedBytes = (st.hits?.default?.bytes ?? 0) + stopped.reduce((n, r) => n + (st.hits?.[r.id]?.bytes ?? 0), 0);
  // Reachable by the same test as the first tile: with the machine's own firewall dropping, only a
  // port a rule allows (or one kept open) gets through.
  const reachable = st.listening.filter((p) => !p.local && (p.allowed || !dropping));
  return (
    <div className="stats-row fw-glance">
      <div className={dropping ? 'fw-glance-good' : 'fw-glance-warn'}>
        <span className="label">Unmatched incoming</span>
        <div className="readout-value fw-glance-word">{dropping ? 'Dropped' : st.policy === 'ACCEPT' ? 'Let in' : 'Up to ' + tool}</div>
        <div className="readout-sub">{st.default === 'drop' ? 'set here' : `${tool}’s default`}</div>
      </div>
      <div>
        <span className="label">Rules</span>
        <div className="readout-value">{live.length}</div>
        <div className="readout-sub">
          {ins} in · {live.length - ins} out
          {paused ? ` · ${paused} paused` : ''}
        </div>
      </div>
      <div>
        <span className="label">Dropped</span>
        <div className="readout-value">{dropped.toLocaleString()}</div>
        <div className="readout-sub">
          {formatBytes(droppedBytes)}
          {st.since ? ` since ${formatAgo(browserTime(st, st.since))}` : ''}
        </div>
      </div>
      <div className={reachable.length ? '' : 'fw-glance-good'}>
        <span className="label">Open to anyone</span>
        <div className="readout-value">{reachable.length}</div>
        <div className="readout-sub truncate" title={reachable.map((p) => `${p.port}/${p.proto}`).join(', ')}>
          {reachable.length ? reachable.map((p) => p.port).join(' · ') : 'no ports reachable'}
        </div>
      </div>
    </div>
  );
}

// SecondsLeft counts down to until (on the browser's clock) by itself, so the panel around it does
// not redraw every second.
function SecondsLeft({ until }: { until: number }) {
  const now = useNow(1000);
  return <span className="num">{Math.max(0, Math.round(until - now))}s</span>;
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
  const canEdit = useCanEdit();
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

  // A viewer still sees which way it is set: the choices stay, greyed out.
  const viewerTitle = canEdit ? undefined : "Viewers can't change this";
  return (
    <div className="fw-default">
      <span className="label">Incoming traffic no rule decides</span>
      <div className="fw-default-choices" role="radiogroup" aria-label="Incoming traffic no rule decides">
        <button type="button" role="radio" aria-checked={def === ''} className={`fw-choice${def === '' ? ' on' : ''}`} disabled={disabled || !!busy} onClick={() => choose('')} title={viewerTitle}>
          <strong>Leave it to {tool}</strong>
          <span>{st.policy === 'DROP' ? `${tool} drops it, unless its own rules let it in.` : st.policy === 'ACCEPT' ? `${tool} lets it in, unless its own rules drop it.` : `${tool} decides.`}</span>
        </button>
        <button
          type="button"
          role="radio"
          aria-checked={def === 'drop'}
          className={`fw-choice${def === 'drop' ? ' on' : ''}`}
          disabled={disabled || !!busy}
          onClick={() => choose('drop')}
          title={viewerTitle}
        >
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
                    className="select-box"
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
            <SecondsLeft until={browserTime(st, st.pending.until)} />, so a change that cut you off undoes itself.
          </p>
          {canEdit && (
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
          )}
        </div>
      )}
    </div>
  );
}

// OpenPorts lists the ports something listens on and the service on each, whether the network can
// reach each, and shortcuts to a rule for it or to lock it to its service.
function OpenPorts({ st, canChange, busy, ruleFor, lock }: { st: FirewallStatus; canChange: boolean; busy: boolean; ruleFor: (p: FirewallPort) => void; lock?: (p: FirewallPort) => void }) {
  const dropping = st.default === 'drop';
  const paged = usePaged(st.listening, 12);
  return (
    <div className="fw-ports">
      <span className="label">Open ports</span>
      <ul>
        {paged.items.map((p) => {
          // How the network sees the port: only from here, let in by a rule, shut for everyone by a rule
          // (but for the addresses allowed ahead of it), shut by the default, or left to the machine's
          // own firewall. The rules decide before anything kept open, so a block wins over that.
          const only = (p.only ?? []).map((a) => sourceWords(a, st.dashboard));
          const state = p.local ? 'local' : p.allowed ? 'open' : p.blocked ? (only.length ? 'some' : 'closed') : dropping ? 'closed' : 'tool';
          const words = {
            local: 'this machine only',
            open: 'open to anyone',
            some: `only ${only.join(', ')}`,
            closed: p.blocked ? 'closed by a rule' : 'closed by the default',
            tool: `up to ${TOOL[st.tool]}`,
          }[state];
          const locked = lockFor(st, p);
          const name = p.unit || p.program || p.exe ? serviceName(p) : '';
          return (
            <li key={`${p.proto}${p.port}`} className={`fw-port ${state}`}>
              <span className="fw-port-num mono num">
                {p.port}
                <span className="fw-port-proto">/{p.proto}</span>
              </span>
              <span className="fw-port-prog truncate" title={serviceTitle(p) || undefined}>
                {name || '—'}
                {p.unit && p.program && name !== p.program && <span className="ink-3"> · {p.program}</span>}
              </span>
              {(st.full || p.local) && <span className="fw-port-state">{words}</span>}
              {locked && (
                <span className={`fw-port-lock ${locked.state}`} title={locked.state === 'taken' ? 'Another program holds it' : `Locked to ${serviceName(locked)}`}>
                  <LockIcon />
                  {locked.state === 'taken' ? (locked.watch_only ? 'taken · watched' : 'taken · dropping') : 'locked'}
                </span>
              )}
              {canChange && (
                <span className="fw-port-acts">
                  {lock && !locked && name && (
                    <button type="button" className="btn-quiet" disabled={busy} onClick={() => lock(p)} aria-label={`Lock ${p.port}/${p.proto} to ${name}`} title={`Lock to ${name}`}>
                      <LockIcon />
                    </button>
                  )}
                  {!p.local && (
                    <button type="button" className="btn-quiet" onClick={() => ruleFor(p)} aria-label={`Add a rule for ${p.port}/${p.proto}`}>
                      + Rule
                    </button>
                  )}
                </span>
              )}
            </li>
          );
        })}
      </ul>
      <Pager paged={paged} label="Pages of open ports" noun="ports" />
    </div>
  );
}

// PortLocks lists the ports locked to a service and how each stands: held by it, nothing listening,
// or taken by another program, whose traffic is dropped until the service has it back. The program
// there can be trusted instead (locked to it), or the lock let go.
function PortLocks({
  st,
  target,
  canChange,
  busy,
  run,
}: {
  st: FirewallStatus;
  target: FirewallTarget;
  canChange: boolean;
  busy: string;
  run: (key: string, change: () => Promise<FirewallStatus | undefined>, ok?: { title: string; body?: string }) => Promise<boolean>;
}) {
  const now = useNow(30000);
  const locks = st.locks ?? [];
  return (
    <div className="fw-locks">
      <span className="label">Locked ports</span>
      <p className="small ink-3">Each port is locked to the service that held it. Should another program take it, Nightjar drops what comes to that port, ahead of every rule, and alerts you.</p>
      <ul>
        {locks.map((l) => {
          const words =
            l.state === 'taken'
              ? `${l.holder ? holderName(l.holder) : 'another program'} holds it${l.since ? ` since ${formatAgo(browserTime(st, l.since), now)}` : ''}${l.watch_only ? ': the port is kept open, so it is only watched' : ': dropping what comes to it'}`
              : l.state === 'idle'
                ? 'nothing listens on it now'
                : 'held by its service';
          return (
            <li key={l.id} className={`fw-lock ${l.state}`}>
              <span className="fw-lock-port mono num">
                <LockIcon /> {l.port}
                <span className="fw-port-proto">/{l.proto}</span>
              </span>
              <span className="fw-lock-what min0">
                <span className="truncate" title={serviceTitle(l)}>
                  {serviceName(l)}
                  {l.exe && <span className="mono ink-3 small"> {l.exe}</span>}
                </span>
                <span className="fw-lock-state small" title={l.holder ? serviceTitle(l.holder) : undefined}>
                  {words}
                </span>
              </span>
              {canChange && (
                <span className="fw-rule-acts">
                  {l.state === 'taken' && target.relock && (
                    <button
                      type="button"
                      className="btn-quiet"
                      disabled={!!busy}
                      onClick={() =>
                        run(`relock:${l.id}`, () => target.relock!(l.id), {
                          title: `Port locked to the program on it · ${target.name}`,
                          body: `${l.port}/${l.proto} is locked to ${l.holder ? holderName(l.holder) : 'the program on it'} now.`,
                        })
                      }
                    >
                      Trust it
                    </button>
                  )}
                  {target.unlock && (
                    <button
                      type="button"
                      className="btn-quiet"
                      disabled={!!busy}
                      onClick={() =>
                        run(`unlock:${l.id}`, () => target.unlock!(l.id), { title: `Port unlocked · ${target.name}`, body: `${l.port}/${l.proto} is no longer locked to ${serviceName(l)}.` })
                      }
                    >
                      Unlock
                    </button>
                  )}
                </span>
              )}
            </li>
          );
        })}
      </ul>
    </div>
  );
}

// Dropped shows the latest dropped packets the kernel logged, and who was dropped most, with a
// shortcut to block them.
function Dropped({ st, rules, canChange, block }: { st: FirewallStatus; rules: FirewallRule[]; canChange: boolean; block: (source: string) => void }) {
  const now = useNow(30000);
  const drops = st.drops ?? [];
  const paged = usePaged(drops, 10);
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
    const l = st.locks?.find((l) => l.id === d.rule);
    if (l) return `port ${l.port} is locked to ${serviceName(l)}, and another program held it`;
    const r = byId.get(d.rule);
    return r ? describeRule(r) : 'a rule since removed';
  };
  const defaultHits = st.hits?.default?.packets ?? 0;
  return (
    <Section label="Dropped lately" aside={<span className="small ink-3">a sample, at most 10 a minute for each rule</span>}>
      <div className="fw">
        {st.drops_note && <p className="note">{st.drops_note}</p>}
        {defaultHits > 0 && (
          <p className="small ink-2 fw-default-hits">
            The default dropped {defaultHits.toLocaleString()} packets ({formatBytes(st.hits!.default.bytes)}){st.since ? ` since ${formatAgo(browserTime(st, st.since), now)}` : ''}.
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
                      <span className="fw-top-bar" aria-hidden="true">
                        <span style={{ width: `${(count / top[0][1]) * 100}%` }} />
                      </span>
                      <span className="small ink-2 num">{count}×</span>
                      {canChange && !blocked(source) ? (
                        <button type="button" className="btn-quiet" onClick={() => block(source)} aria-label={`Block ${source}`}>
                          Block
                        </button>
                      ) : (
                        <span className={blocked(source) ? 'fw-blocked' : ''}>{blocked(source) ? 'blocked' : ''}</span>
                      )}
                    </li>
                  ))}
                </ul>
              </div>
            )}
            <ul className="fw-drops">
              {paged.items.map((d, i) => (
                <li key={`${d.at}-${paged.offset + i}`}>
                  <span className="small ink-3 num fw-drop-at">{formatAgo(browserTime(st, d.at), now)}</span>
                  <span
                    className={`fw-drop-dot ${d.rule === 'default' ? 'default' : (byId.get(d.rule)?.action ?? (st.locks?.some((l) => l.id === d.rule) ? 'block' : 'default'))}`}
                    aria-hidden="true"
                  />
                  <span className="fw-drop-what min0">
                    <span className="mono">{d.direction === 'out' ? `→ ${d.dest}` : d.source}</span>
                    {(d.proto || d.port) && (
                      <span className="fw-tag">
                        {d.proto ?? ''}
                        {d.port ? ` ${d.port}` : ''}
                      </span>
                    )}
                    <span className="fw-drop-why">{why(d)}</span>
                  </span>
                </li>
              ))}
            </ul>
            <Pager paged={paged} label="Pages of dropped packets" noun="drops" />
          </>
        )}
      </div>
    </Section>
  );
}
