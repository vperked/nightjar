import { useEffect, useRef, type RefObject } from 'react';

// What a keyboard can land on inside a dialog or menu.
const FOCUSABLE = [
  'a[href]',
  'button:not([disabled])',
  'input:not([disabled]):not([type="hidden"])',
  'select:not([disabled])',
  'textarea:not([disabled])',
  'iframe',
  '[tabindex]:not([tabindex="-1"])',
  '[contenteditable="true"]',
].join(',');

// focusables lists what Tab reaches inside root, in order. Ones hidden by CSS (a column hidden on a
// phone, say) are left out, as Tab skips them; without layout (tests) every one counts.
export function focusables(root: HTMLElement): HTMLElement[] {
  const all = Array.from(root.querySelectorAll<HTMLElement>(FOCUSABLE)).filter((el) => !el.closest('fieldset:disabled, [inert], [hidden]'));
  const shown = all.filter((el) => el.getClientRects().length > 0);
  return shown.length ? shown : all;
}

// Open modal dialogs, newest last: only the top one keeps Tab inside it, so the finder opened over a
// sheet traps to itself and the sheet takes over again once it closes.
const stack: object[] = [];

// The page behind a modal does not scroll while it is open. Counted, so a dialog over another only
// gives the scroll back once both are closed; the scrollbar's width is kept as padding so the page
// does not shift sideways when it goes.
let locks = 0;
let saved = { overflow: '', paddingRight: '' };
function lockScroll(): () => void {
  const body = document.body;
  if (locks++ === 0) {
    saved = { overflow: body.style.overflow, paddingRight: body.style.paddingRight };
    const bar = window.innerWidth - document.documentElement.clientWidth;
    body.style.overflow = 'hidden';
    if (bar > 0) body.style.paddingRight = `${bar}px`;
  }
  return () => {
    if (--locks === 0) Object.assign(body.style, saved);
  };
}

export interface DialogOptions {
  // A modal dialog keeps Tab inside itself and stops the page behind from scrolling.
  modal?: boolean;
  // Where focus goes on opening: the first thing in it, or the dialog itself (a panel whose first
  // control would be a surprise to land on, like "Mark all read").
  focus?: 'first' | 'self';
}

// useDialog gives a dialog the keyboard behaviour people expect: focus moves into it on opening,
// Tab and Shift+Tab stay inside a modal one, and on closing focus goes back to what opened it. Each
// dialog keeps its own Escape and click-away handling, as they differ (the finder's Escape must not
// also close a sheet beneath it).
export function useDialog(ref: RefObject<HTMLElement>, open: boolean, { modal = true, focus = 'first' }: DialogOptions = {}) {
  // What had focus before the dialog opened, read while rendering: by the time an effect runs, a
  // control inside with autoFocus has already taken it.
  const opener = useRef<HTMLElement | null>(null);
  if (!open) opener.current = null;
  else if (!opener.current && typeof document !== 'undefined') opener.current = document.activeElement as HTMLElement | null;

  useEffect(() => {
    const el = ref.current;
    if (!open || !el) return;
    const before = opener.current;

    if (!el.contains(document.activeElement)) {
      const first = focus === 'first' ? focusables(el)[0] : undefined;
      if (first) first.focus();
      else {
        if (!el.hasAttribute('tabindex')) el.setAttribute('tabindex', '-1');
        el.focus();
      }
    }

    const entry = {};
    let unlock = () => {};
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Tab' || e.defaultPrevented || stack[stack.length - 1] !== entry) return;
      const list = focusables(el);
      const active = document.activeElement as HTMLElement | null;
      if (!list.length) {
        e.preventDefault();
        el.focus();
        return;
      }
      const inside = !!active && active !== el && el.contains(active);
      const first = list[0];
      const last = list[list.length - 1];
      if (e.shiftKey ? !inside || active === first : !inside || active === last) {
        e.preventDefault();
        (e.shiftKey ? last : first).focus();
      }
    };
    if (modal) {
      stack.push(entry);
      unlock = lockScroll();
      document.addEventListener('keydown', onKey);
    }

    return () => {
      if (modal) {
        stack.splice(stack.indexOf(entry), 1);
        unlock();
        document.removeEventListener('keydown', onKey);
      }
      // Focus goes back only if it was still in the dialog (or went with it); someone who clicked or
      // tabbed somewhere else meant to go there.
      const now = document.activeElement;
      if (before?.isConnected && before !== document.body && (!now || now === document.body || el.contains(now))) before.focus({ preventScroll: true });
    };
  }, [open, modal, focus]); // eslint-disable-line react-hooks/exhaustive-deps
}
