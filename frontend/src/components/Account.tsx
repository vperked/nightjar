import { useId, useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import { ApiError, api } from '../api/client';
import { useConfig } from '../api/ConfigContext';
import { SettingsGroup } from './SettingsGroup';
import { configChanged } from './Sections';
import { useToast } from './Toasts';

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

// help is where "Help with this" leads, or with helpNote, a sentence of its own around the link.
function AuthCard({
  title,
  intro,
  children,
  onSubmit,
  help,
  helpNote,
}: {
  title: string;
  intro: ReactNode;
  children: ReactNode;
  onSubmit: (e: FormEvent) => void;
  help: string;
  helpNote?: { before: string; link: string; after: string };
}) {
  return (
    <main className="auth">
      <form className="signin" onSubmit={onSubmit}>
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
  const [copied, setCopied] = useState(false);
  const command = `journalctl -u ${config?.account?.service || 'nightjar'} | grep setup_code | tail -1`;
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
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  };
  const copy = () => {
    navigator.clipboard?.writeText(command).then(
      () => setCopied(true),
      () => undefined,
    );
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
          <code>{command}</code>
          <button type="button" className="btn-quiet" onClick={copy}>
            {copied ? 'Copied' : 'Copy'}
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
        setError(err instanceof ApiError ? err.message : String(err));
      }
      if (!needsCode) setPassword('');
      setCode('');
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthCard title="Sign in" intro="Sign in with the admin account." onSubmit={submit} help="/docs/troubleshooting#can-t-sign-in">
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
      setError(err instanceof ApiError ? err.message : String(err));
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
      <TwoFactor on={!!config.account.two_factor} left={config.account.recovery_left ?? 0} />
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
  const run = async (fn: () => Promise<void>) => {
    setError('');
    try {
      await fn();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    }
  };
  const begin = () => run(async () => setSetup(await api.twoFactorBegin()));
  const enable = () =>
    run(async () => {
      const r = await api.twoFactorEnable(code.trim());
      setRecovery(r.recovery);
      setSetup(undefined);
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
        <button type="button" className="btn-quiet" onClick={begin}>
          Set it up
        </button>
      )}
      {setup && (
        <div className="twofa-setup">
          <span className="form-help">
            Add this key to your authenticator app, or <a href={setup.link}>open it in the app</a> on this device:
          </span>
          <code className="twofa-secret">{setup.secret.replace(/(.{4})/g, '$1 ').trim()}</code>
          <span className="inline-fields">
            <input
              className="field short mono"
              aria-label="Code from the app"
              placeholder="123456"
              inputMode="numeric"
              autoComplete="one-time-code"
              value={code}
              onChange={(e) => setCode(e.target.value)}
            />
            <button type="button" className="btn" onClick={enable} disabled={code.trim().length !== 6}>
              Turn on
            </button>
          </span>
        </div>
      )}
      {on && !turningOff && (
        <button type="button" className="btn-quiet" onClick={() => setTurningOff(true)}>
          Turn off
        </button>
      )}
      {on && turningOff && (
        <span className="inline-fields twofa-off">
          <Password label="Password" value={password} onChange={setPassword} autoComplete="current-password" />
          <input className="field short mono" aria-label="Code to turn it off" placeholder="123456" value={code} onChange={(e) => setCode(e.target.value)} />
          <button type="button" className="btn-quiet danger" onClick={disable} disabled={!password || !code.trim()}>
            Turn off
          </button>
        </span>
      )}
      {error && <p className="form-error">{sentence(error)}</p>}
    </div>
  );
}
