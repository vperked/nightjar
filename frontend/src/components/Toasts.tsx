import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react';

export type Tone = 'ok' | 'info' | 'warning' | 'error';

export interface ToastInput {
  tone?: Tone;
  title: string;
  body?: string;
}

interface Toast extends Required<Pick<ToastInput, 'tone' | 'title'>> {
  id: number;
  body?: string;
  leaving?: boolean;
}

type Notify = (t: ToastInput) => void;

const Ctx = createContext<Notify>(() => {});

// Errors stay long enough to read; confirmations get out of the way.
const DURATION: Record<Tone, number> = { ok: 4000, info: 5000, warning: 7000, error: 9000 };
const MAX = 4;
// How long a toast takes to fade out; matches toast-out in toasts.css.
const LEAVE_MS = 160;

const GLYPH: Record<Tone, string> = { ok: 'M7.5 12.5l3 3 6-6.5', info: 'M12 11v5.5M12 7.5v.5', warning: 'M12 7v6M12 16.5v.5', error: 'M8 8l8 8M16 8l-8 8' };

// ToastItem counts down its own time, paused while the pointer is over it, and shows what is left as a hairline.
function ToastItem({ toast, onDone }: { toast: Toast; onDone: (id: number) => void }) {
  const [paused, setPaused] = useState(false);
  const left = useRef(DURATION[toast.tone]);
  useEffect(() => {
    if (paused || toast.leaving) return;
    const started = Date.now();
    const id = setTimeout(() => onDone(toast.id), left.current);
    return () => {
      clearTimeout(id);
      left.current -= Date.now() - started;
    };
  }, [paused, toast.leaving, toast.id, onDone]);
  return (
    <div
      className={`toast ${toast.tone} ${toast.leaving ? 'leaving' : ''} ${paused ? 'paused' : ''}`}
      style={{ ['--toast-time' as string]: `${DURATION[toast.tone]}ms` }}
      onMouseEnter={() => setPaused(true)}
      onMouseLeave={() => setPaused(false)}
    >
      <span className="toast-icon" aria-hidden="true">
        <svg viewBox="0 0 24 24">
          <path d={GLYPH[toast.tone]} />
        </svg>
      </span>
      <div className="toast-text">
        <strong>{toast.title}</strong>
        {toast.body && <span>{toast.body}</span>}
      </div>
      <button type="button" className="toast-close" aria-label="Dismiss" onClick={() => onDone(toast.id)}>
        <svg viewBox="0 0 24 24" aria-hidden="true">
          <path d="M7 7l10 10M17 7L7 17" />
        </svg>
      </button>
      <span className="toast-time" aria-hidden="true" />
    </div>
  );
}

// ToastProvider shows short messages in a corner stack, announced to screen readers.
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(1);

  // A toast fades out first, then leaves the stack.
  const dismiss = useCallback((id: number) => {
    setToasts((ts) => ts.map((t) => (t.id === id ? { ...t, leaving: true } : t)));
    setTimeout(() => setToasts((ts) => ts.filter((t) => t.id !== id)), LEAVE_MS);
  }, []);
  const notify = useCallback<Notify>(({ tone = 'info', title, body }) => {
    const id = next.current++;
    setToasts((ts) => [...ts.slice(-(MAX - 1)), { id, tone, title, body }]);
  }, []);

  return (
    <Ctx.Provider value={notify}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {[...toasts].reverse().map((t) => (
          <ToastItem key={t.id} toast={t} onDone={dismiss} />
        ))}
      </div>
    </Ctx.Provider>
  );
}

// useToast returns a stable function that shows a toast.
export const useToast = () => useContext(Ctx);
