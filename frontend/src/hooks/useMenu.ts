import { useEffect, type FocusEvent, type KeyboardEvent, type RefObject } from 'react';

// The items of a menu, in order, skipping disabled ones.
const items = (menu: HTMLElement | null) => Array.from(menu?.querySelectorAll<HTMLElement>('[role^="menuitem"]:not([disabled])') ?? []);

// useMenu gives a menu button the keyboard behaviour of a menu: opening puts focus on the first
// item, the arrow keys, Home and End move between items, Escape closes it back onto its button, Tab
// closes it and carries on from the button, and it closes once focus moves anywhere outside it.
// done() closes it after choosing an item, putting focus back on the button first.
export function useMenu(menu: RefObject<HTMLElement>, button: RefObject<HTMLElement>, open: boolean, setOpen: (open: boolean) => void) {
  useEffect(() => {
    if (open) items(menu.current)[0]?.focus({ preventScroll: true });
  }, [open]); // eslint-disable-line react-hooks/exhaustive-deps

  const done = () => {
    setOpen(false);
    button.current?.focus({ preventScroll: true });
  };

  const onKeyDown = (e: KeyboardEvent) => {
    const list = items(menu.current);
    const at = list.indexOf(document.activeElement as HTMLElement);
    const go = (i: number) => {
      e.preventDefault();
      e.stopPropagation();
      list[(i + list.length) % list.length]?.focus();
    };
    if (e.key === 'ArrowDown') go(at + 1);
    else if (e.key === 'ArrowUp') go(at < 0 ? -1 : at - 1);
    else if (e.key === 'Home') go(0);
    else if (e.key === 'End') go(-1);
    else if (e.key === 'Escape') {
      // Only this menu closes, not a sheet or row it sits in.
      e.preventDefault();
      e.stopPropagation();
      done();
    } else if (e.key === 'Tab') {
      // From the button, the Tab itself then moves on to whatever follows it, as from a closed menu.
      done();
    }
  };

  // A focus that leaves for somewhere else closes the menu. One going nowhere (a click on the
  // menu's own text, or the window losing focus) does not: clicks away are handled on their own.
  const onBlur = (e: FocusEvent) => {
    const to = e.relatedTarget as Node | null;
    if (!open || !to || menu.current?.contains(to) || button.current?.contains(to)) return;
    setOpen(false);
  };

  return { onKeyDown, onBlur, done };
}
