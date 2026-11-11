import { useCallback, useEffect, useRef, useState, type RefObject } from 'react';

// Copying text, also where the browser keeps the clipboard API away: a dashboard opened over plain
// http (http://192.168.1.10:8080) has no navigator.clipboard at all.

// Copied says how a copy went: 'copied' to the clipboard, 'selected' when only the text could be
// selected (the user presses Ctrl+C), 'failed' when neither worked.
export type Copied = 'copied' | 'selected' | 'failed';

// viaTextarea copies with the older execCommand, which still works on plain http in most browsers:
// the text goes in a hidden textarea, is selected and copied, and the textarea goes again.
function viaTextarea(text: string): boolean {
  if (typeof document.execCommand !== 'function') return false;
  const active = document.activeElement as HTMLElement | null;
  const ta = document.createElement('textarea');
  ta.value = text;
  ta.setAttribute('readonly', '');
  ta.setAttribute('aria-hidden', 'true');
  // Fixed and see-through, so the page neither scrolls to it nor shows it.
  Object.assign(ta.style, { position: 'fixed', top: '0', left: '0', width: '1px', height: '1px', opacity: '0', pointerEvents: 'none' });
  document.body.appendChild(ta);
  ta.focus({ preventScroll: true });
  ta.select();
  let ok = false;
  try {
    ok = document.execCommand('copy');
  } catch {
    ok = false;
  }
  ta.remove();
  active?.focus?.({ preventScroll: true });
  return ok;
}

// selectText selects what an element shows, so Ctrl+C copies it.
function selectText(el: Element): boolean {
  const sel = window.getSelection();
  if (!sel) return false;
  const range = document.createRange();
  range.selectNodeContents(el);
  sel.removeAllRanges();
  sel.addRange(range);
  return true;
}

// copyText copies text by the best way the browser allows: the clipboard API, then execCommand, and
// last selecting the text in show (when given) for the user to copy.
export async function copyText(text: string, show?: Element | null): Promise<Copied> {
  try {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return 'copied';
    }
  } catch {
    // Refused (no permission, or the page not focused): try the other ways.
  }
  if (viaTextarea(text)) return 'copied';
  return show && selectText(show) ? 'selected' : 'failed';
}

// copyKeys is the shortcut for copying on this machine: ⌘C on a Mac, Ctrl+C elsewhere.
export const copyKeys = () => (/Mac|iPhone|iPad/.test(navigator.platform || navigator.userAgent) ? '⌘C' : 'Ctrl+C');

// useCopy is a copy button's state: copy() copies, and label() says what the button reads now
// ("Copy", "Copied", or "Press Ctrl+C" when the text could only be selected), back to "Copy" after
// a moment.
export function useCopy(text: string, show?: RefObject<Element | null>) {
  const [state, setState] = useState<Copied | ''>('');
  const timer = useRef(0);
  useEffect(() => () => window.clearTimeout(timer.current), []);
  const copy = useCallback(async () => {
    const got = await copyText(text, show?.current);
    setState(got);
    window.clearTimeout(timer.current);
    // A selection waits longer: the user still has to press the keys.
    timer.current = window.setTimeout(() => setState(''), got === 'copied' ? 1600 : 4000);
    return got;
  }, [text, show]);
  const label = (idle = 'Copy') => (state === 'copied' ? 'Copied' : state === 'selected' ? `Press ${copyKeys()}` : state === 'failed' ? 'Could not copy' : idle);
  return { state, copy, label };
}
