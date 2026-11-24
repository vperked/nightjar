import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import { ApiError, api, isDemo, errorText } from '../api/client';
import type { Viewer } from '../api/types';
import { useConfig } from '../api/ConfigContext';
import { reducedMotion } from '../motion';
import { SettingsGroup } from './SettingsGroup';
import { configChanged } from './Sections';
import { useToast } from './Toasts';
import { useCopy } from '../clipboard';

// The admin account: the first-run screen that creates it, the sign-in screen, and the Settings
// section that changes its password.

const MIN_PASSWORD = 10;

function Password({ label, value, onChange, autoComplete, autoFocus }: { label: string; value: string; onChange: (v: string) => void; autoComplete: string; autoFocus?: boolean }) {
  const [shown, setShown] = useState(false);
  const id = useId();
  // The show button sits outside the label, so the field's name stays just the label.
  return (
    <div className="auth-field">
      <label htmlFor={id}>{label}</label>
      <span className="auth-password">
        <input id={id} className="field" type={shown ? 'text' : 'password'} value={value} onChange={(e) => onChange(e.target.value)} autoComplete={autoComplete} autoFocus={autoFocus} required />
        <button type="button" className="btn-quiet" onClick={() => setShown((s) => !s)} aria-label={shown ? `Hide ${label.toLowerCase()}` : `Show ${label.toLowerCase()}`}>
          {shown ? 'Hide' : 'Show'}
        </button>
      </span>
    </div>
  );
}

// The night sky behind the sign-in card: stars scattered the same way on every visit (a fixed seed),
// each twinkling in its own time, as x and y in percent, a size in px and a delay and period in s.
const STARS = (() => {
  let seed = 7;
  const rand = () => ((seed = (seed * 16807) % 2147483647) - 1) / 2147483646;
  return Array.from({ length: 70 }, () => ({ x: rand() * 100, y: rand() * 100, size: rand() < 0.15 ? 2.5 : rand() < 0.5 ? 1.5 : 1, delay: rand() * 6, period: 3 + rand() * 4 }));
})();

function NightSky() {
  return (
    <div className="auth-sky" aria-hidden="true">
      {STARS.map((s, i) => (
        <span key={i} className="auth-star" style={{ left: `${s.x}%`, top: `${s.y}%`, width: s.size, height: s.size, animationDelay: `${s.delay}s`, animationDuration: `${s.period}s` }} />
      ))}
      <span className="auth-meteor" />
      <span className="auth-meteor late" />
    </div>
  );
}

// help is where "Help with this" leads, or with helpNote, a sentence of its own around the link.
// Each time shake changes to a new truthy value (a failed try), the card shakes its head.
function AuthCard({
  title,
  intro,
  children,
  onSubmit,
  help,
  helpNote,
  shake,
}: {
  title: string;
  intro: ReactNode;
  children: ReactNode;
  onSubmit: (e: FormEvent) => void;
  help: string;
  helpNote?: { before: string; link: string; after: string };
  shake?: number;
}) {
  const card = useRef<HTMLFormElement>(null);
  useEffect(() => {
    if (!shake || reducedMotion()) return;
    card.current?.animate?.({ transform: ['translateX(0)', 'translateX(-9px)', 'translateX(7px)', 'translateX(-5px)', 'translateX(3px)', 'translateX(0)'] }, { duration: 420, easing: 'ease-out' });
  }, [shake]);
  return (
    <main className="auth">
      <NightSky />
      <form className="signin" onSubmit={onSubmit} ref={card}>
        <div className="auth-mark" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round">
            <rect x="5" y="11" width="14" height="9" rx="2" />
            <path d="M8 11V8a4 4 0 0 1 8 0v3" />
          </svg>
        </div>
        <h1>{title}</h1>
        <p className="auth-intro">{intro}</p>
        {children}
        {helpNote ? (
          <p className="auth-note">
            {helpNote.before} <Link to={help}>{helpNote.link}</Link> {helpNote.after}
          </p>
        ) : (
          <Link className="auth-help" to={help}>
            Help with this
          </Link>
        )}
      </form>
    </main>
  );
}

// sentence turns a server message ("wrong username or password") into one to show.
const sentence = (msg: string) => msg.charAt(0).toUpperCase() + msg.slice(1).replace(/\.?$/, '.');

// problemsWith says what is wrong with a new password before the server is asked.
function problemsWith(password: string, confirm: string, username = ''): string {
  if (password.length > 0 && password.length < MIN_PASSWORD) return `Use at least ${MIN_PASSWORD} characters.`;
  if (username && password.toLowerCase() === username.toLowerCase()) return 'The password cannot be the username.';
  if (confirm && confirm !== password) return 'The passwords do not match.';
  return '';
}

// CreateAdmin is the first screen of a new install: nothing else works until the account exists.
export function CreateAdmin({ onDone }: { onDone: () => void }) {
  const config = useConfig();
  const [username, setUsername] = useState('admin');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [code, setCode] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  // On plain http, as a first visit often is, the command is selected and the button says to press Ctrl+C.
  const commandRef = useRef<HTMLElement>(null);
  const command = `journalctl -u ${config?.account?.service || 'nightjar'} | grep setup_code | tail -1`;
  const copy = useCopy(command, commandRef);
  const problem = problemsWith(password, confirm, username);
  const ready = username.trim() && password.length >= MIN_PASSWORD && confirm === password && code.trim() && !problem;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (!ready) return;
    setBusy(true);
    setError('');
    try {
      await api.createAdmin(username.trim(), password, code);
      onDone();
    } catch (err) {
      setError(errorText(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthCard
      title="Create the admin account"
      intro="The dashboard needs an admin account before it shows anything. You will sign in with it from now on."
      onSubmit={submit}
      help="/docs/getting-started#create-the-admin-account"
      helpNote={{ before: 'Confused?', link: 'Read the getting started guide.', after: 'It walks through this step and what to set up next.' }}
    >
      <label className="auth-field">
        <span>Username</span>
        <input className="field" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" required />
      </label>
      <Password label="Password" value={password} onChange={setPassword} autoComplete="new-password" autoFocus />
      <Password label="Confirm password" value={confirm} onChange={setConfirm} autoComplete="new-password" />
      <label className="auth-field">
        <span>Setup code</span>
        <input className="field mono" value={code} onChange={(e) => setCode(e.target.value)} placeholder="XXXXX-XXXXX" autoComplete="one-time-code" spellCheck={false} required />
      </label>
      <div className="auth-code-help">
        <span>So only you can do this, the monitor wrote a one-time code to its log when it started. On the server, run:</span>
        <span className="auth-command">
          <code ref={commandRef}>{command}</code>
          <button type="button" className="btn-quiet" onClick={copy.copy}>
            {copy.label()}
          </button>
        </span>
      </div>
      {(problem || error) && (
        <p className="form-error" role="alert">
          {error ? sentence(error) : problem}
        </p>
      )}
      <button type="submit" className="btn" disabled={!ready || busy}>
        {busy ? 'Creating…' : 'Create account and sign in'}
      </button>
    </AuthCard>
  );
}

export function SignIn({ onDone }: { onDone: () => void }) {
  const config = useConfig();
  const [username, setUsername] = useState(config?.account?.username ?? '');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [forgot, setForgot] = useState(false);
  // With two-factor sign-in on, the password is followed by a code from the authenticator.
  const [askCode, setAskCode] = useState(false);
  const [code, setCode] = useState('');
  const [fails, setFails] = useState(0);

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.login(username.trim(), password, code.trim());
      onDone();
    } catch (err) {
      const needsCode = err instanceof ApiError && !!err.fields.code;
      if (needsCode && !askCode) {
        setAskCode(true);
      } else {
        setError(errorText(err));
        setFails((n) => n + 1);
      }
      if (!needsCode) setPassword('');
      setCode('');
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthCard
      title="Sign in"
      intro={
        isDemo() ? (
          <>
            Demo data: sign in as <code>demo</code> with the password <code>demo</code>, or as <code>viewer</code> with <code>viewer password</code> to see what a read-only viewer sees.
          </>
        ) : (
          'Sign in with your account.'
        )
      }
      onSubmit={submit}
      help="/docs/troubleshooting#can-t-sign-in"
      shake={fails}
    >
      <label className="auth-field">
        <span>Username</span>
        <input className="field" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus={!username} required />
      </label>
      <Password label="Password" value={password} onChange={setPassword} autoComplete="current-password" autoFocus={!!username} />
      {askCode && (
        <label className="auth-field">
          <span>Code from your authenticator app</span>
          <input
            className="field mono"
            value={code}
            onChange={(e) => setCode(e.target.value)}
            autoComplete="one-time-code"
            inputMode="numeric"
            autoFocus
            placeholder="123456, or a recovery code"
            required
          />
        </label>
      )}
      {error && (
        <p className="form-error" role="alert">
          {sentence(error)}
        </p>
      )}
      <button type="submit" className="btn" disabled={!username.trim() || !password || (askCode && !code.trim()) || busy}>
        {busy ? 'Signing in…' : 'Sign in'}
      </button>
      {forgot ? (
        <p className="auth-forgot">
          On the server, run <code>nightjar -reset-admin</code> and restart the monitor, then create the account again with the setup code it logs.
        </p>
      ) : (
        <button type="button" className="btn-quiet auth-forgot-btn" onClick={() => setForgot(true)}>
          Forgot the password?
        </button>
      )}
    </AuthCard>
  );
}

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
