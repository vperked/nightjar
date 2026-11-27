import { useEffect, useRef, useState, type FormEvent } from 'react';
import { api, errorText } from '../api/client';
import type { Viewer } from '../api/types';
import { useConfig } from '../api/ConfigContext';
import { MIN_PASSWORD, Password, problemsWith, sentence } from './Account';
import { SettingsGroup } from './SettingsGroup';
import { configChanged } from './Sections';
import { useToast } from './Toasts';

// The account's Settings sections: the admin's password and two-factor sign-in, the viewers, and
// the page a viewer gets at /settings. Kept apart from the sign-in screens, which every visit loads.

// AccountSection changes the admin password from Settings. Other browsers are signed out.
export function AccountSection() {
  const config = useConfig();
  const notify = useToast();
  const [current, setCurrent] = useState('');
  const [next, setNext] = useState('');
  const [confirm, setConfirm] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const problem = problemsWith(next, confirm, config?.account?.username);
  const ready = current && next.length >= MIN_PASSWORD && confirm === next && !problem;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.changePassword(current, next);
      setCurrent('');
      setNext('');
      setConfirm('');
      notify({ tone: 'ok', title: 'Password changed', body: 'Other browsers were signed out.' });
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  if (!config?.account?.username) return null;
  return (
    <SettingsGroup
      id="account"
      name="Account"
      aside={
        <span className="small ink-3">
          signed in as <strong>{config.account.username}</strong>
        </span>
      }
    >
      <form className="account-form" onSubmit={submit}>
        <Password label="Current password" value={current} onChange={setCurrent} autoComplete="current-password" />
        <Password label="New password" value={next} onChange={setNext} autoComplete="new-password" />
        <Password label="Confirm new password" value={confirm} onChange={setConfirm} autoComplete="new-password" />
        <div className="account-actions">
          {(problem || error) && <span className="form-error">{error ? sentence(error) : problem}</span>}
          <button type="submit" className="btn" disabled={!ready || busy}>
            {busy ? 'Changing…' : 'Change password'}
          </button>
        </div>
      </form>
      {config.account.role !== 'viewer' && <TwoFactor on={!!config.account.two_factor} left={config.account.recovery_left ?? 0} />}
    </SettingsGroup>
  );
}

// TwoFactor turns on sign-in codes from an authenticator app, or off again.
function TwoFactor({ on, left }: { on: boolean; left: number }) {
  const notify = useToast();
  const [setup, setSetup] = useState<{ secret: string; link: string }>();
  const [code, setCode] = useState('');
  const [password, setPassword] = useState('');
  const [recovery, setRecovery] = useState<string[]>();
  const [error, setError] = useState('');
  const [turningOff, setTurningOff] = useState(false);
  // One request at a time: a double-click would otherwise send the same change twice. The ref
  // catches a second click before the disabled button has rendered.
  const [busy, setBusy] = useState(false);
  const sending = useRef(false);
  const run = async (fn: () => Promise<void>) => {
    if (sending.current) return;
    sending.current = true;
    setBusy(true);
    setError('');
    try {
      await fn();
    } catch (err) {
      setError(errorText(err));
    } finally {
      sending.current = false;
      setBusy(false);
    }
  };
  // Cancel leaves the setup or the turning off without a trace of what was typed.
  const cancel = () => {
    setSetup(undefined);
    setTurningOff(false);
    setPassword('');
    setCode('');
    setError('');
  };
  const begin = () => run(async () => setSetup(await api.twoFactorBegin()));
  const submit = (e: FormEvent, fn: () => void) => {
    e.preventDefault();
    fn();
  };
  const enable = () =>
    run(async () => {
      const r = await api.twoFactorEnable(password, code.trim());
      setRecovery(r.recovery);
      setSetup(undefined);
      setPassword('');
      setCode('');
      configChanged();
      notify({ tone: 'ok', title: 'Two-factor sign-in is on' });
    });
  const disable = () =>
    run(async () => {
      await api.twoFactorDisable(password, code.trim());
      setTurningOff(false);
      setPassword('');
      setCode('');
      // Codes from before are spent with it; showing them still would only mislead.
      setRecovery(undefined);
      configChanged();
      notify({ tone: 'ok', title: 'Two-factor sign-in is off' });
    });
  return (
    <div className="twofa">
      <div className="twofa-head">
        <strong>Two-factor sign-in</strong>
        <span className={`state ${on ? 'up' : 'paused'}`}>{on ? 'On' : 'Off'}</span>
      </div>
      <p className="form-help">
        {on
          ? `Signing in asks for a code from your authenticator app after the password. ${left} of 10 recovery codes left.`
          : 'Ask for a code from an authenticator app (1Password, Google Authenticator, Aegis…) after the password.'}
      </p>
      {recovery && (
        <div className="twofa-recovery">
          <strong>Your recovery codes</strong>
          <span className="form-help">Each signs you in once without the app. Keep them somewhere safe: they are not shown again.</span>
          <code>{recovery.join('  ')}</code>
        </div>
      )}
      {!on && !setup && (
        <button type="button" className="btn-quiet" onClick={begin} disabled={busy}>
          Set it up
        </button>
      )}
      {setup && (
        <div className="twofa-setup">
          <span className="form-help">
            Add this key to your authenticator app, or <a href={setup.link}>open it in the app</a> on this device:
          </span>
          <code className="twofa-secret">{setup.secret.replace(/(.{4})/g, '$1 ').trim()}</code>
          <form className="inline-fields" onSubmit={(e) => submit(e, () => password && code.trim().length === 6 && enable())}>
            <Password label="Password" value={password} onChange={setPassword} autoComplete="current-password" />
            <input
              className="field short mono"
              aria-label="Code from the app"
              placeholder="123456"
              inputMode="numeric"
              autoComplete="one-time-code"
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
            <button type="submit" className="btn" disabled={busy || !password || code.trim().length !== 6}>
              {busy ? 'Turning on…' : 'Turn on'}
            </button>
            <button type="button" className="btn-quiet" onClick={cancel} disabled={busy}>
              Cancel
            </button>
          </form>
        </div>
      )}
      {on && !turningOff && (
        <button type="button" className="btn-quiet" onClick={() => setTurningOff(true)}>
          Turn off
        </button>
      )}
      {on && turningOff && (
        <form className="inline-fields twofa-off" onSubmit={(e) => submit(e, () => password && code.trim() && disable())}>
          <Password label="Password" value={password} onChange={setPassword} autoComplete="current-password" />
          <input className="field short mono" aria-label="Code to turn it off" placeholder="123456" value={code} onChange={(e) => setCode(e.target.value)} />
          <button type="submit" className="btn-quiet danger" disabled={busy || !password || !code.trim()}>
            {busy ? 'Turning off…' : 'Turn off'}
          </button>
          <button type="button" className="btn-quiet" onClick={cancel} disabled={busy}>
            Cancel
          </button>
        </form>
      )}
      {error && (
        <p className="form-error" role="alert">
          {sentence(error)}
        </p>
      )}
    </div>
  );
}

// ViewersSection lets the admin make read-only accounts: they see the dashboard but change nothing,
// and never see settings, backups or keys.
export function ViewersSection() {
  const config = useConfig();
  const notify = useToast();
  const [viewers, setViewers] = useState<Viewer[]>();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [armed, setArmed] = useState('');
  const [resetting, setResetting] = useState('');
  const [newPassword, setNewPassword] = useState('');
  const admin = config?.account?.role !== 'viewer' && !!config?.account?.username;

  useEffect(() => {
    if (!admin) return;
    api.viewers().then(
      (r) => setViewers(r.viewers),
      (err) => setError(errorText(err)),
    );
  }, [admin]);

  const run = async (fn: () => Promise<{ viewers: Viewer[] }>, done: string) => {
    setBusy(true);
    setError('');
    try {
      setViewers((await fn()).viewers);
      notify({ tone: 'ok', title: done });
      return true;
    } catch (err) {
      setError(errorText(err));
      return false;
    } finally {
      setBusy(false);
    }
  };

  const add = async (e: FormEvent) => {
    e.preventDefault();
    const name = username.trim();
    if (await run(() => api.addViewer(name, password), `${name} can now sign in as a viewer`)) {
      setUsername('');
      setPassword('');
    }
  };

  const reset = async (e: FormEvent) => {
    e.preventDefault();
    if (await run(() => api.setViewerPassword(resetting, newPassword), `New password set for ${resetting}`)) {
      setResetting('');
      setNewPassword('');
    }
  };

  if (!admin) return null;
  const problem = password && password.length < MIN_PASSWORD ? `Use at least ${MIN_PASSWORD} characters.` : '';
  return (
    <SettingsGroup id="viewers" name="Viewers" aside={<span className="small ink-3">read-only accounts</span>}>
      <p className="form-help">
        A viewer signs in like you and sees the dashboard, but cannot change anything, run repairs or updates, or see settings, backups and keys. Changing your own password leaves viewers signed in;
        removing one signs it out everywhere.
      </p>
      {viewers && viewers.length > 0 && (
        <ul className="viewer-list">
          {viewers.map((v) => (
            <li key={v.username}>
              <span>
                <strong>{v.username}</strong>{' '}
                <span className="small ink-3">
                  added {new Date(v.created * 1000).toLocaleDateString()}
                  {v.sessions > 0 ? ` · signed in on ${v.sessions} browser${v.sessions === 1 ? '' : 's'}` : ''}
                </span>
              </span>
              <span className="viewer-actions">
                <button type="button" className="btn btn-quiet" disabled={busy} onClick={() => setResetting(resetting === v.username ? '' : v.username)}>
                  New password
                </button>
                <button
                  type="button"
                  className={`btn btn-quiet${armed === v.username ? ' danger' : ''}`}
                  disabled={busy}
                  onBlur={() => setArmed('')}
                  onClick={() => (armed === v.username ? run(() => api.removeViewer(v.username), `${v.username} was removed`).then(() => setArmed('')) : setArmed(v.username))}
                >
                  {armed === v.username ? 'Remove for good?' : 'Remove'}
                </button>
              </span>
              {resetting === v.username && (
                <form className="account-form" onSubmit={reset}>
                  <Password label={`New password for ${v.username}`} value={newPassword} onChange={setNewPassword} autoComplete="new-password" autoFocus />
                  <div className="account-actions">
                    <button type="submit" className="btn" disabled={busy || newPassword.length < MIN_PASSWORD}>
                      Set password
                    </button>
                  </div>
                </form>
              )}
            </li>
          ))}
        </ul>
      )}
      {viewers && viewers.length === 0 && <p className="note">No viewers yet.</p>}
      <form className="account-form" onSubmit={add}>
        <label className="auth-field">
          <span>Viewer's username</span>
          <input className="field" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="off" spellCheck={false} />
        </label>
        <Password label="Their password" value={password} onChange={setPassword} autoComplete="new-password" />
        <div className="account-actions">
          {(problem || error) && <span className="form-error">{error ? sentence(error) : problem}</span>}
          <button type="submit" className="btn" disabled={busy || !username.trim() || password.length < MIN_PASSWORD}>
            {busy ? 'Saving…' : 'Add viewer'}
          </button>
        </div>
      </form>
    </SettingsGroup>
  );
}

// ViewerAccountPage is what a viewer gets at /settings: its own password, and nothing else.
export function ViewerAccountPage() {
  return (
    <main>
      <div className="page-title">
        <div>
          <h1>Your account</h1>
          <div className="page-sub">You are a viewer: you see the dashboard but cannot change it. The admin manages everything else.</div>
        </div>
      </div>
      <AccountSection />
    </main>
  );
}
