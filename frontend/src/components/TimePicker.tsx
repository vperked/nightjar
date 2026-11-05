import { useEffect, useId, useRef, useState, type FocusEvent, type KeyboardEvent } from 'react';

// TimePicker is the dashboard's time field: a button showing the time and a small panel of hour and
// minute columns, in place of the browser's own picker. The value is "HH:MM" on a 24-hour clock;
// what it shows follows the browser's language, so 23:00 reads "11:00 PM" where that is the custom.

const pad = (n: number) => String(n).padStart(2, '0');

function parse(value: string): [number, number] {
  const [h, m] = value.split(':').map(Number);
  return [Number.isFinite(h) ? Math.min(23, Math.max(0, h)) : 0, Number.isFinite(m) ? Math.min(59, Math.max(0, m)) : 0];
}

const twelveHour = () => {
  try {
    return new Intl.DateTimeFormat(undefined, { hour: 'numeric' }).resolvedOptions().hour12 === true;
  } catch {
    return false;
  }
};

export function formatTime(value: string, hour12 = twelveHour()): string {
  const [h, m] = parse(value);
  return hour12 ? `${h % 12 || 12}:${pad(m)} ${h < 12 ? 'AM' : 'PM'}` : `${pad(h)}:${pad(m)}`;
}

const ClockIcon = () => (
  <svg className="tp-icon" viewBox="0 0 24 24" aria-hidden="true">
    <circle cx="12" cy="12" r="8.5" />
    <path d="M12 7.5V12l3 2" />
  </svg>
);

function Column({ label, items, selected, onPick }: { label: string; items: { value: number; text: string }[]; selected: number; onPick: (v: number) => void }) {
  const list = useRef<HTMLDivElement>(null);
  // Opens with the chosen item in the middle of the column.
  useEffect(() => {
    const el = list.current?.querySelector<HTMLElement>('[aria-selected="true"]');
    if (el && list.current) list.current.scrollTop = el.offsetTop - list.current.clientHeight / 2 + el.clientHeight / 2;
  }, []);
  return (
    <div className="tp-col">
      <span className="tp-col-head">{label}</span>
      <div className="tp-list" role="listbox" aria-label={label} ref={list}>
        {items.map((it) => (
          <button key={it.value} type="button" role="option" aria-selected={it.value === selected} className={it.value === selected ? 'selected' : ''} onClick={() => onPick(it.value)}>
            {it.text}
          </button>
        ))}
      </div>
    </div>
  );
}

export function TimePicker({ value, onChange, label, disabled }: { value: string; onChange: (v: string) => void; label: string; disabled?: boolean }) {
  const [open, setOpen] = useState(false);
  const [above, setAbove] = useState(false);
  const [right, setRight] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const id = useId();
  const hour12 = twelveHour();
  const [h, m] = parse(value);

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [open]);
  useEffect(() => {
    if (disabled) setOpen(false);
  }, [disabled]);

  const set = (hour: number, minute: number) => onChange(`${pad(hour)}:${pad(minute)}`);
  const show = () => {
    const rect = button.current?.getBoundingClientRect();
    setAbove(!!rect && window.innerHeight - rect.bottom < 300 && rect.top > window.innerHeight - rect.bottom);
    // Lines up with the field's right edge when the panel would run off the screen.
    setRight(!!rect && rect.left + 220 > window.innerWidth - 8);
    setOpen(true);
  };
  const close = () => {
    setOpen(false);
    button.current?.focus();
  };
  // Arrow keys on the closed field step a quarter of an hour, wrapping round midnight. Escape closes
  // the panel from the field too, where focus stays after opening it.
  const onKey = (e: KeyboardEvent) => {
    if (open && e.key === 'Escape') {
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
      return;
    }
    if (e.key === 'ArrowUp' || e.key === 'ArrowDown') {
      e.preventDefault();
      const t = (h * 60 + m + (e.key === 'ArrowUp' ? 15 : -15) + 1440) % 1440;
      set(Math.floor(t / 60), t % 60);
    }
  };
  const onPanelKey = (e: KeyboardEvent) => {
    if (e.key === 'Escape') {
      // Closes the panel only, not a form it sits in.
      e.preventDefault();
      e.stopPropagation();
      close();
    }
  };

  const minutes = Array.from({ length: 12 }, (_, i) => i * 5);
  if (!minutes.includes(m)) minutes.splice(minutes.findIndex((x) => x > m) === -1 ? minutes.length : minutes.findIndex((x) => x > m), 0, m);
  const hours = hour12 ? Array.from({ length: 12 }, (_, i) => ({ value: i === 0 ? 12 : i, text: String(i === 0 ? 12 : i) })) : Array.from({ length: 24 }, (_, i) => ({ value: i, text: pad(i) }));
  const pm = h >= 12;

  // Closes when focus moves on to something else on the page, as tabbing past the panel does. A blur
  // with nowhere to go (a click on a button Safari does not focus) is left to the mousedown above.
  const onBlur = (e: FocusEvent) => {
    if (open && e.relatedTarget && !root.current?.contains(e.relatedTarget as Node)) setOpen(false);
  };

  return (
    <div className={`tp ${open ? 'open' : ''} ${disabled ? 'disabled' : ''}`} ref={root} onBlur={onBlur}>
      <button
        ref={button}
        type="button"
        className="tp-trigger"
        aria-label={label}
        aria-haspopup="dialog"
        aria-expanded={open}
        aria-controls={open ? `${id}-panel` : undefined}
        disabled={disabled}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={onKey}
      >
        <span className="tp-value">{formatTime(value, hour12)}</span>
        <ClockIcon />
      </button>
      {open && (
        <div className={`tp-panel ${above ? 'above' : ''} ${right ? 'right' : ''}`} id={`${id}-panel`} role="dialog" aria-label={label} onKeyDown={onPanelKey}>
          <div className="tp-cols">
            <Column label="Hour" items={hours} selected={hour12 ? h % 12 || 12 : h} onPick={(v) => set(hour12 ? (v % 12) + (pm ? 12 : 0) : v, m)} />
            <Column label="Min" items={minutes.map((v) => ({ value: v, text: pad(v) }))} selected={m} onPick={(v) => set(h, v)} />
            {hour12 && (
              <div className={`tp-col tp-ampm ${pm ? '' : 'am'}`}>
                <span className="tp-col-head">&nbsp;</span>
                <div className="tp-list" role="listbox" aria-label="AM or PM">
                  {(['AM', 'PM'] as const).map((p) => (
                    <button
                      key={p}
                      type="button"
                      role="option"
                      aria-selected={(p === 'PM') === pm}
                      className={(p === 'PM') === pm ? 'selected' : ''}
                      onClick={() => set((h % 12) + (p === 'PM' ? 12 : 0), m)}
                    >
                      {p}
                    </button>
                  ))}
                </div>
              </div>
            )}
          </div>
          <div className="tp-foot">
            <span className="tp-preview">{formatTime(value, hour12)}</span>
            <button type="button" className="btn tp-done" onClick={close}>
              Done
            </button>
          </div>
        </div>
      )}
    </div>
  );
}
