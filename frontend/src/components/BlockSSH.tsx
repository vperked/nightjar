import { useCallback, useEffect, useRef, useState } from 'react';
import { api, ApiError } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import type { FirewallRule, FirewallStatus, SSHAccess, SSHKey } from '../api/types';
import { plural } from '../format';
import { useNow } from '../hooks/useNow';
import type { FirewallTarget, SSHTarget } from './Firewall';
import { useToast, type ToastInput } from './Toasts';

// Block SSH, on the firewall page: keep everyone off a machine's SSH but one address (two firewall
// rules), or add a key to an account and then, if wanted, turn password logins off. Turning them off
// undoes itself after two minutes unless kept, the way the firewall's default does, so a key that
// does not work cannot lock anyone out.

const errText = (e: unknown) => (e instanceof ApiError || e instanceof Error ? e.message : String(e));

// SSHAccessState is a machine's SSH logins as last read, shared by the state beside the buttons, the
// countdown while keys only waits to be kept, and the Block SSH panel.
export interface SSHAccessState {
  target?: SSHTarget;
  access?: SSHAccess;
  // failed is why the logins could not be read (an agent too old to know them, say); the state then
  // stays hidden, and only the key path says so.
  failed: string;
  busy: boolean;
  set: (a: SSHAccess) => void;
  act: (change: (s: SSHTarget) => Promise<SSHAccess>, ok: ToastInput) => Promise<boolean>;
}

// tidy fills in the lists an answer may leave out (Go sends null for an empty one).
// It also notes how far the machine's clock is from the browser's, for the countdown.
const tidy = (a: SSHAccess): SSHAccess => ({ ...a, ports: a.ports ?? [], users: a.users ?? [], skew: a.now ? a.now - Date.now() / 1000 : 0 });

export function useSSHAccess(target: SSHTarget | undefined, name: string): SSHAccessState {
  const notify = useToast();
  const [access, setAccess] = useState<SSHAccess>();
  const [failed, setFailed] = useState('');
  const [busy, setBusy] = useState(false);
  // seq moves on with each change, so a read that went out before it cannot bring back the old state.
  const seq = useRef(0);

  const reload = useCallback(() => {
    if (!target) return;
    const at = seq.current;
    target.load().then(
      (a) => {
        if (seq.current !== at) return;
        setAccess(tidy(a));
        setFailed('');
      },
      (e) => {
        if (seq.current === at) setFailed(errText(e));
      },
    );
  }, [target]);
  useEffect(() => {
    seq.current++;
    setAccess(undefined);
    setFailed('');
    reload();
  }, [reload]);
  // Keys only not kept in time goes back on the machine by itself: read it again once it has.
  useEffect(() => {
    if (!access?.pending) return;
    const t = window.setTimeout(reload, Math.max(0, (access.pending.until - (access.skew ?? 0)) * 1000 - Date.now()) + 1500);
    return () => window.clearTimeout(t);
  }, [access, reload]);

  const set = useCallback((a: SSHAccess) => {
    seq.current++;
    setAccess(tidy(a));
    setFailed('');
  }, []);
  const act = async (change: (s: SSHTarget) => Promise<SSHAccess>, ok: ToastInput) => {
    if (!target) return false;
    setBusy(true);
    try {
      set(await change(target));
      notify({ tone: 'ok', ...ok });
      return true;
    } catch (e) {
      notify({ tone: 'error', title: `Could not change SSH logins · ${name}`, body: errText(e) });
      return false;
    } finally {
      setBusy(false);
    }
  };
  return { target, access: target ? access : undefined, failed, busy, set, act };
}

// sshPorts are the ports SSH listens on: as sshd says, else those the firewall keeps open for SSH,
// else 22.
export function sshPorts(access: SSHAccess | undefined, st: FirewallStatus): number[] {
  if (access?.ports.length) return access.ports;
  const open = (st.open ?? []).filter((o) => o.what === 'SSH').map((o) => o.port);
  return open.length ? [...new Set(open)] : [22];
}

// SSHState says in a few words how the machine takes SSH logins, and turns passwords back on when
// Nightjar turned them off (after asking: one click should not undo it).
export function SSHState({ ssh, name }: { ssh: SSHAccessState; name: string }) {
  const canEdit = useCanEdit();
  const [confirming, setConfirming] = useState(false);
  const a = ssh.access;
  if (!a) return null;
  const keysOnly = !a.password_login;
  return (
    <span className="fw-ssh-state" title={a.problem || undefined}>
      <span className="ink-3">SSH:</span> <span className={keysOnly ? 'fw-ssh-keys-only' : 'ink-2'}>{keysOnly ? 'keys only' : 'passwords on'}</span>
      {canEdit &&
        a.keys_only &&
        !a.pending &&
        !a.problem &&
        (confirming ? (
          <span className="confirm">
            <span className="small ink-3">Allow passwords again?</span>
            <button
              type="button"
              className="btn-quiet"
              disabled={ssh.busy}
              onClick={() => ssh.act((s) => s.keysOnly(false), { title: `Passwords allowed again · ${name}`, body: 'Password logins work again, as well as keys.' }).then(() => setConfirming(false))}
            >
              {ssh.busy ? '…' : 'Allow'}
            </button>
            <button type="button" className="btn-quiet" onClick={() => setConfirming(false)}>
              Cancel
            </button>
          </span>
        ) : (
          <button type="button" className="btn-quiet" onClick={() => setConfirming(true)}>
            Allow passwords again
          </button>
        ))}
    </span>
  );
}

// SSHPending counts down while keys only waits to be kept, like the firewall's own Keep/Undo.
export function SSHPending({ ssh, name }: { ssh: SSHAccessState; name: string }) {
  const canEdit = useCanEdit();
  const now = useNow(1000);
  const until = (ssh.access?.pending?.until ?? 0) - (ssh.access?.skew ?? 0);
  const left = Math.max(0, Math.round(until - now));
  return (
    <div className="fw-pending" role="alert">
      <p>
        <strong>Only keys log in to {name} now.</strong> Check that your key works with a new SSH login, then keep it. Passwords come back by themselves in <span className="num">{left}s</span>;
        sessions already open stay open.
      </p>
      {canEdit && (
        <div className="fw-ask-acts">
          <button type="button" className="btn-quiet" disabled={ssh.busy} onClick={() => ssh.act((s) => s.keep(false), { title: `Passwords back · ${name}`, body: 'Password logins work again.' })}>
            Put passwords back
          </button>
          <button type="button" className="btn" disabled={ssh.busy} onClick={() => ssh.act((s) => s.keep(true), { title: `Keys only · ${name}`, body: 'Password logins stay off.' })}>
            Keep keys only
          </button>
        </div>
      )}
    </div>
  );
}

// A private key is refused before it leaves the browser: it should never be pasted anywhere.
const PRIVATE = /-----BEGIN [A-Z ]*PRIVATE KEY-----|PuTTY-User-Key-File|^b3BlbnNzaC1rZXktdjE/m;
const PUBLIC = /^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp\d+|sk-[\w@.-]+)\s+[A-Za-z0-9+/=]{20,}(\s.*)?$/;

// pastedGap says what is wrong with a pasted key, in words, or '' when it looks like a public key.
export function pastedGap(text: string): string {
  const key = text.trim();
  if (!key) return 'Paste the public key: one line starting ssh-ed25519, ssh-rsa or ecdsa-sha2-….';
  if (PRIVATE.test(key)) return 'That is a private key, which should never leave your computer. Paste the public half instead: the .pub file, such as ~/.ssh/id_ed25519.pub.';
  if (key.includes('\n')) return 'Paste one key, on one line.';
  if (!PUBLIC.test(key)) return 'That does not look like a public key: it is one line starting ssh-ed25519, ssh-rsa or ecdsa-sha2-….';
  return '';
}

// BlockSSH asks who should still get in over SSH: an address (an allow for it, then a block for
// everyone else, through the firewall), or a key (added to an account, then, if wanted, keys only).
export function BlockSSH({
  target,
  st,
  ssh,
  addRule,
  busy,
  onClose,
}: {
  target: FirewallTarget;
  st: FirewallStatus;
  ssh: SSHAccessState;
  addRule: (rule: Partial<FirewallRule>) => Promise<void>;
  busy: boolean;
  onClose: () => void;
}) {
  const notify = useToast();
  const [mode, setMode] = useState<'' | 'address' | 'key'>('');
  const [source, setSource] = useState('');
  const [error, setError] = useState('');
  // allowedOnly is the address whose allow went in when the block after it was refused.
  const [allowedOnly, setAllowedOnly] = useState('');
  const [keys, setKeys] = useState<SSHKey[]>();
  // chose is the key picked by hand (its path, or 'paste'); until then pick falls on one of Nightjar's.
  const [chose, setPick] = useState('');
  const [pasted, setPasted] = useState('');
  const [user, setUser] = useState('');
  // asking is the account a key was just added for, while "Only allow keys?" waits for an answer.
  const [asking, setAsking] = useState('');
  const [working, setWorking] = useState(false);

  // Nightjar's own keys are read the first time the key path is picked.
  useEffect(() => {
    if (mode !== 'key' || keys) return;
    let live = true;
    api.sshKeys().then(
      (r) => live && setKeys(r.keys ?? []),
      () => live && setKeys([]),
    );
    return () => {
      live = false;
    };
  }, [mode, keys]);

  const ports = sshPorts(ssh.access, st);
  const port = ports.join(',');
  const portText = `tcp ${ports.length > 1 ? 'ports' : 'port'} ${ports.join(', ')}`;
  const nightjar = st.dashboard;
  const src = source.trim();
  const who = src && src === nightjar ? `Nightjar (${src})` : src;
  const users = ssh.access?.users ?? [];
  // The account a key goes on: the one picked, else root, else the first.
  const account = users.find((u) => u.name === user) ?? users.find((u) => u.name === 'root') ?? users[0];
  // Unless one is picked by hand: the first of Nightjar's keys not on the account yet, else the
  // first, else pasting one.
  const pick = chose || (keys ? (keys.find((k) => !account?.fingerprints?.includes(k.fingerprint)) ?? keys[0])?.path || 'paste' : '');
  const chosen = keys?.find((k) => k.path === pick);
  const keyText = pick === 'paste' ? pasted.trim() : (chosen?.public ?? '');
  // cannot is why the key path can't be used on this machine, as the machine said it.
  const cannot = !target.ssh ? `SSH logins can't be managed on ${target.name} from here.` : ssh.failed || ssh.access?.problem || '';
  const privatePasted = pick === 'paste' && PRIVATE.test(pasted);

  const gap = (() => {
    if (!mode) return 'Pick who should still get in.';
    if (mode === 'address') {
      if (!src) return 'Type who: an address such as 203.0.113.7, or a network such as 198.51.100.0/24.';
      if (!/^[0-9a-f.:/]+$/i.test(src)) return `"${src}" is not an address, or a network such as 198.51.100.0/24.`;
      return '';
    }
    if (cannot) return 'Keys can’t be added here.';
    if (!ssh.access) return 'Reading the SSH logins…';
    if (!account) return `No account on ${target.name} can log in over SSH.`;
    if (!pick) return 'Reading Nightjar’s keys…';
    if (pick === 'paste') return pastedGap(pasted);
    return '';
  })();

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (gap || working || busy) return;
    setError('');
    setAllowedOnly('');
    setWorking(true);
    if (mode === 'address') {
      // An allow for this address on SSH already in place is not added twice (on a second try after
      // the block was refused, say).
      const has = st.rules.some((r) => r.action === 'allow' && !r.paused && r.direction !== 'out' && r.source === src && r.port === port && r.proto === 'tcp');
      try {
        if (!has) await addRule({ action: 'allow', source: src, port, proto: 'tcp', note: 'SSH' });
      } catch (err) {
        setError(errText(err));
        setWorking(false);
        return;
      }
      // The block goes in behind the allow (the firewall puts it after an allow it is an exception
      // to). The firewall refuses it if it would cut off the dashboard or an SSH login open now; the
      // allow then stays, and the refusal is shown as it came.
      try {
        await addRule({ action: 'block', port, proto: 'tcp', note: `SSH for everyone but ${who}` });
      } catch (err) {
        setError(errText(err));
        setAllowedOnly(src);
        setWorking(false);
        return;
      }
      notify({ tone: 'ok', title: `Only ${src} can reach SSH on ${target.name}`, body: `Everyone else is dropped on ${portText}.` });
      onClose();
      return;
    }
    try {
      const a = await target.ssh!.addKey(account!.name, keyText);
      ssh.set(a);
      // With passwords already off there is nothing more to ask.
      if (a.password_login) setAsking(account!.name);
      else {
        notify({ tone: 'ok', title: `Key added for ${account!.name} · ${target.name}` });
        onClose();
      }
    } catch (err) {
      setError(errText(err));
    } finally {
      setWorking(false);
    }
  };

  const keysOnly = async () => {
    setError('');
    setWorking(true);
    try {
      ssh.set(await target.ssh!.keysOnly(true, asking));
      onClose();
    } catch (err) {
      setError(errText(err));
      setWorking(false);
    }
  };
  const done = () => {
    notify({ tone: 'ok', title: `Key added for ${asking} · ${target.name}`, body: 'Passwords still work too.' });
    onClose();
  };

  const choices: { value: 'address' | 'key'; title: string; body: string }[] = [
    { value: 'address', title: 'An address', body: 'Only this address or network reaches SSH; the firewall drops everyone else.' },
    { value: 'key', title: 'A key', body: 'Put a key on an account, then, if you like, turn password logins off.' },
  ];

  return (
    <form className="fw-form" onSubmit={submit} aria-label="Block SSH">
      <div className="fw-form-head">
        <strong>Block SSH on {target.name}</strong>
        <span className="small ink-3">{portText}</span>
      </div>

      {asking ? (
        <div className="fw-ask" role="group" aria-label="Only allow keys?">
          <p>
            <strong>Key added for {asking}. Only allow keys?</strong>
          </p>
          <p>Password logins are turned off. Sessions already open stay open. If your key doesn’t work, passwords come back by themselves in 2 minutes unless you press Keep.</p>
          {error && <p className="form-error">{error}</p>}
          <div className="fw-ask-acts">
            <button type="button" className="btn-quiet" disabled={working} onClick={done}>
              No, keep passwords
            </button>
            <button type="button" className="btn" disabled={working} onClick={keysOnly}>
              {working ? 'Turning off…' : 'Yes, only keys'}
            </button>
          </div>
        </div>
      ) : (
        <>
          <p className="fw-ssh-q">Who should still get in over SSH?</p>
          <div className="fw-actions n2" role="radiogroup" aria-label="Who should still get in over SSH?">
            {choices.map((c) => (
              <button
                key={c.value}
                type="button"
                role="radio"
                aria-checked={mode === c.value}
                className={`fw-choice${mode === c.value ? ' on' : ''}`}
                onClick={() => {
                  setMode(c.value);
                  setError('');
                  setAllowedOnly('');
                }}
              >
                <strong>{c.title}</strong>
                <span>{c.body}</span>
              </button>
            ))}
          </div>

          {mode === 'address' && (
            <div className="fw-step">
              <span className="fw-step-label">Only from</span>
              <div className="fw-pick">
                {nightjar && (
                  <button type="button" className={`chip${src === nightjar ? ' active' : ''}`} onClick={() => setSource(nightjar)} title="The address this server sees Nightjar ask from">
                    Nightjar <span className="mono">{nightjar}</span>
                  </button>
                )}
                <input
                  className="field mono"
                  value={source}
                  onChange={(e) => setSource(e.target.value)}
                  placeholder="203.0.113.7 or 198.51.100.0/24"
                  aria-label="Address or network allowed to reach SSH"
                  spellCheck={false}
                  autoFocus
                />
              </div>
            </div>
          )}

          {mode === 'key' &&
            (cannot ? (
              <p className="form-error fw-error">{cannot}</p>
            ) : (
              <>
                <fieldset className="fw-step">
                  <legend className="fw-step-label">Key</legend>
                  <div className="fw-ssh-keys">
                    {(keys ?? []).map((k) => {
                      const there = !!account?.fingerprints?.includes(k.fingerprint);
                      return (
                        <label key={k.path} className="fw-radio fw-ssh-key">
                          <input type="radio" name="fw-ssh-key" checked={pick === k.path} onChange={() => setPick(k.path)} />
                          <span className="min0">
                            <strong>{k.name}</strong> <span className="mono small ink-3">{k.fingerprint}</span>
                            {there && <span className="fw-tag">already on {account!.name}</span>}
                          </span>
                        </label>
                      );
                    })}
                    {!keys && <span className="small ink-3">Reading Nightjar’s keys…</span>}
                    <label className="fw-radio">
                      <input type="radio" name="fw-ssh-key" checked={pick === 'paste'} onChange={() => setPick('paste')} />
                      Paste a public key
                    </label>
                    {pick === 'paste' && (
                      <textarea
                        className="field mono fw-ssh-paste"
                        rows={3}
                        value={pasted}
                        onChange={(e) => setPasted(e.target.value)}
                        placeholder="ssh-ed25519 AAAA… you@laptop"
                        aria-label="Public key"
                        aria-invalid={privatePasted || undefined}
                        spellCheck={false}
                      />
                    )}
                    {privatePasted && (
                      <p className="form-error fw-error" role="alert">
                        {pastedGap(pasted)}
                      </p>
                    )}
                  </div>
                </fieldset>
                {users.length > 0 && (
                  <label className="fw-step">
                    <span className="fw-step-label">Account</span>
                    <span className="fw-pick">
                      <select className="field" value={account?.name ?? ''} onChange={(e) => setUser(e.target.value)} aria-label="Account">
                        {users.map((u) => (
                          <option key={u.name} value={u.name}>
                            {u.name} · {plural(u.keys, 'key')}
                          </option>
                        ))}
                      </select>
                    </span>
                  </label>
                )}
              </>
            ))}

          <div className="fw-form-foot">
            <p className={`fw-preview${gap ? ' unfinished' : ''}`} aria-live="polite" id="fw-ssh-preview">
              {gap ||
                (mode === 'address' ? (
                  <>
                    <span className="ink-3">This will </span>allow {who} on {portText}, then block everyone else there.
                  </>
                ) : (
                  <>
                    <span className="ink-3">This will </span>add {pick === 'paste' ? 'the pasted key' : chosen?.name} to {account?.name}’s keys
                    {ssh.access?.password_login ? ', then ask whether to allow only keys' : ''}.
                  </>
                ))}
            </p>
            <div className="fw-submit">
              <button type="button" className="btn-quiet" onClick={onClose}>
                Cancel
              </button>
              <button className="btn" type="submit" disabled={working || busy || !!gap} aria-describedby="fw-ssh-preview">
                {working ? 'Saving…' : mode === 'key' ? 'Add key' : 'Block SSH'}
              </button>
            </div>
          </div>
          {error && <p className="form-error fw-error">{error}</p>}
          {allowedOnly && <p className="form-help fw-help">The allow for {allowedOnly} went in; remove it from the rules below if you no longer want it.</p>}
        </>
      )}
    </form>
  );
}
