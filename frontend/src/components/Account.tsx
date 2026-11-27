import { useEffect, useId, useRef, useState, type FormEvent, type ReactNode } from 'react';
import { Link } from 'react-router';
import { ApiError, api, isDemo, errorText } from '../api/client';
import { useConfig } from '../api/ConfigContext';
import { reducedMotion } from '../motion';
import { useCopy } from '../clipboard';

// The admin account: the first-run screen that creates it and the sign-in screen. The Settings
// sections that change passwords and viewers are in AccountSettings, loaded with the page they are on.

export const MIN_PASSWORD = 10;

export function Password({ label, value, onChange, autoComplete, autoFocus }: { label: string; value: string; onChange: (v: string) => void; autoComplete: string; autoFocus?: boolean }) {
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
export const sentence = (msg: string) => msg.charAt(0).toUpperCase() + msg.slice(1).replace(/\.?$/, '.');

// problemsWith says what is wrong with a new password before the server is asked.
export function problemsWith(password: string, confirm: string, username = ''): string {
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
