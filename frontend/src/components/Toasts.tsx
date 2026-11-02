import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from 'react';

export type Tone = 'ok' | 'info' | 'warning' | 'error';

export interface ToastInput {
  tone?: Tone;
  title: string;
  body?: string;
}

interface Toast extends Required<Pick<ToastInput, 'tone' | 'title'>> {
  id: number;
  body?: string;
}

type Notify = (t: ToastInput) => void;

const Ctx = createContext<Notify>(() => {});

// Errors stay long enough to read; confirmations get out of the way.
const DURATION: Record<Tone, number> = { ok: 4000, info: 5000, warning: 7000, error: 9000 };
const MAX = 4;

// ToastProvider shows short messages in a corner stack, announced to screen readers.
export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([]);
  const next = useRef(1);

  const dismiss = useCallback((id: number) => setToasts((ts) => ts.filter((t) => t.id !== id)), []);
  const notify = useCallback<Notify>(
    ({ tone = 'info', title, body }) => {
      const id = next.current++;
      setToasts((ts) => [...ts.slice(-(MAX - 1)), { id, tone, title, body }]);
      setTimeout(() => dismiss(id), DURATION[tone]);
    },
    [dismiss],
  );

  return (
    <Ctx.Provider value={notify}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`toast ${t.tone}`}>
            <span className="toast-mark" aria-hidden="true" />
            <div className="toast-text">
              <strong>{t.title}</strong>
              {t.body && <span>{t.body}</span>}
            </div>
            <button className="toast-close" aria-label="Dismiss" onClick={() => dismiss(t.id)}>
              ×
            </button>
          </div>
        ))}
      </div>
    </Ctx.Provider>
  );
}

// useToast returns a stable function that shows a toast.
export const useToast = () => useContext(Ctx);
