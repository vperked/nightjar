import { useId, useState, type FormEvent, type ReactNode } from 'react';
import { ApiError, api } from '../api/client';
import { useConfig } from '../api/ConfigContext';
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
          {shown ? 'hide' : 'show'}
        </button>
      </span>
    </div>
  );
}

function AuthCard({ title, intro, children, onSubmit }: { title: string; intro: ReactNode; children: ReactNode; onSubmit: (e: FormEvent) => void }) {
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
  const command = `journalctl -u ${config?.account?.service || 'server-monitor'} | grep setup_code | tail -1`;
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
    <AuthCard title="Create the admin account" intro="The dashboard needs an admin account before it shows anything. You will sign in with it from now on." onSubmit={submit}>
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
            {copied ? 'copied' : 'copy'}
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

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError('');
    try {
      await api.login(username.trim(), password);
      onDone();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
      setPassword('');
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthCard title="Sign in" intro="Sign in with the admin account." onSubmit={submit}>
      <label className="auth-field">
        <span>Username</span>
        <input className="field" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus={!username} required />
      </label>
      <Password label="Password" value={password} onChange={setPassword} autoComplete="current-password" autoFocus={!!username} />
      {error && (
        <p className="form-error" role="alert">
          {sentence(error)}
        </p>
      )}
      <button type="submit" className="btn" disabled={!username.trim() || !password || busy}>
        {busy ? 'Signing in…' : 'Sign in'}
      </button>
      {forgot ? (
        <p className="auth-forgot">
          On the server, run <code>servermonitor -reset-admin</code> and restart the monitor, then create the account again with the setup code it logs.
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
    <section className="section" id="account">
      <div className="section-head">
        <span className="label">Account</span>
        <span className="small ink-3">
          signed in as <strong>{config.account.username}</strong>
        </span>
      </div>
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
    </section>
  );
}
