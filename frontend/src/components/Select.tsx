import { useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react';

export interface Option {
  value: string;
  label: ReactNode;
  // text is what type-ahead matches; defaults to the value.
  text?: string;
  // hint is a short line under the label in the open menu, saying what the choice does.
  hint?: string;
}

// Select is the dashboard's dropdown: a button showing the choice and a listbox menu, styled like
// the rest of the page instead of the browser's own. It follows the ARIA combobox pattern: focus
// stays on the button, arrow keys move through the options, Enter picks, Escape closes, and typing
// a letter jumps to the next option starting with it.
export function Select({ value, options, onChange, label, className = '' }: { value: string; options: Option[]; onChange: (v: string) => void; label: string; className?: string }) {
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const [above, setAbove] = useState(false);
  const [right, setRight] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const menu = useRef<HTMLUListElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const id = useId();
  const selected = Math.max(
    0,
    options.findIndex((o) => o.value === value),
  );

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => {
      if (!root.current?.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener('mousedown', close);
    return () => document.removeEventListener('mousedown', close);
  }, [open]);

  // A menu wider than its button lines up with the button's right edge when it would run off the screen.
  useLayoutEffect(() => {
    if (!open) return setRight(false);
    const rect = menu.current?.getBoundingClientRect();
    if (rect && rect.right > window.innerWidth - 8) setRight(true);
  }, [open]);

  // Keep the active option in view while moving through a long list.
  useEffect(() => {
    if (open) document.getElementById(`${id}-${active}`)?.scrollIntoView?.({ block: 'nearest' });
  }, [open, active, id]);

  const show = () => {
    const rect = button.current?.getBoundingClientRect();
    // Open upwards when there is no room below (the save bar sits along the bottom).
    const tall = options.length * (options.some((o) => o.hint) ? 50 : 36) + 16;
    setAbove(!!rect && window.innerHeight - rect.bottom < Math.min(300, tall) && rect.top > window.innerHeight - rect.bottom);
    setActive(selected);
    setOpen(true);
  };
  const pick = (i: number) => {
    onChange(options[i].value);
    setOpen(false);
    button.current?.focus();
  };
  const onKey = (e: KeyboardEvent) => {
    const last = options.length - 1;
    const move = (i: number) => {
      e.preventDefault();
      if (!open) show();
      setActive(Math.max(0, Math.min(last, i)));
    };
    switch (e.key) {
      case 'ArrowDown':
        return move(open ? active + 1 : selected);
      case 'ArrowUp':
        return move(open ? active - 1 : selected);
      case 'Home':
        return open && move(0);
      case 'End':
        return open && move(last);
      case 'Enter':
      case ' ':
        e.preventDefault();
        return open ? pick(active) : show();
      case 'Escape':
        if (open) {
          // Closes the menu only, not a form it sits in.
          e.preventDefault();
          e.stopPropagation();
          setOpen(false);
        }
        return;
      case 'Tab':
        return setOpen(false);
    }
    if (e.key.length === 1 && /\S/.test(e.key)) {
      const from = open ? active : selected;
      const key = e.key.toLowerCase();
      for (let step = 1; step <= options.length; step++) {
        const i = (from + step) % options.length;
        if ((options[i].text ?? options[i].value).toLowerCase().startsWith(key)) {
          if (open) setActive(i);
          else onChange(options[i].value);
          return;
        }
      }
    }
  };

  return (
    <div className={`select ${open ? 'open' : ''} ${className}`} ref={root}>
      <button
        ref={button}
        type="button"
        className="select-trigger"
        role="combobox"
        aria-label={label}
        aria-haspopup="listbox"
        aria-expanded={open}
        aria-controls={`${id}-list`}
        aria-activedescendant={open ? `${id}-${active}` : undefined}
        onClick={() => (open ? setOpen(false) : show())}
        onKeyDown={onKey}
      >
        <span className="select-value">{options[selected]?.label ?? value}</span>
        <svg className="select-chevron" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
          <path d="m7 10 5 5 5-5" />
        </svg>
      </button>
      {open && (
        <ul ref={menu} className={`select-menu ${above ? 'above' : ''} ${right ? 'right' : ''}`} role="listbox" id={`${id}-list`} aria-label={label}>
          {options.map((o, i) => (
            <li
              key={o.value}
              id={`${id}-${i}`}
              role="option"
              aria-selected={i === selected}
              aria-labelledby={`${id}-${i}-label`}
              aria-describedby={o.hint ? `${id}-${i}-hint` : undefined}
              className={`${i === active ? 'active' : ''} ${i === selected ? 'selected' : ''}`}
              onMouseEnter={() => setActive(i)}
              // mousedown, so the pick lands before the button loses focus.
              onMouseDown={(e) => {
                e.preventDefault();
                pick(i);
              }}
            >
              <span className="select-option">
                <span className="select-label" id={`${id}-${i}-label`}>
                  {o.label}
                </span>
                {o.hint && (
                  <span className="select-hint" id={`${id}-${i}-hint`}>
                    {o.hint}
                  </span>
                )}
              </span>
              {i === selected && (
                <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.4" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
                  <path d="M5 12.5l4.5 4.5L19 7.5" />
                </svg>
              )}
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}
