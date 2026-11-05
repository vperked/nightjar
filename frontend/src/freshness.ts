import { useEffect, useRef, useState } from 'react';
import { writesPending } from './api/client';

// A tab left open keeps running the build it loaded, however many updates go live after it. This
// notices a newer build (the page names its main script by content hash, so a new build has a new
// name) and moves the tab onto it when that loses nothing: on the next page change, or when the tab
// comes back into view with nothing edited in it and no change on its way. Otherwise it only says
// so, and the reader reloads.

const ENTRY = /\/assets\/index-[\w-]+\.js/;
const EVERY = 10 * 60_000;

// buildIn finds the build a page belongs to from its HTML or a script address; '' when it has none
// (the dev server serves source files, which have no build to compare).
export const buildIn = (text: string | null | undefined) => text?.match(ENTRY)?.[0] ?? '';

const loadedBuild = () => buildIn(document.querySelector<HTMLScriptElement>('script[type="module"][src*="/assets/index-"]')?.getAttribute('src'));

async function liveBuild(): Promise<string> {
  const res = await fetch(import.meta.env.BASE_URL || '/', { cache: 'no-store', credentials: 'same-origin' });
  return res.ok ? buildIn(await res.text()) : '';
}

// Editing anything marks the page as holding something a reload would throw away, until the next
// page: typing, ticking a box, or picking with a button inside a form (its draft is not saved yet).
let typed = false;
const onInput = (e: Event) => {
  const t = e.target as HTMLElement | null;
  if (t && (t.matches('input, textarea, select') || t.isContentEditable)) typed = true;
};
const onPick = (e: Event) => {
  const t = e.target as HTMLElement | null;
  if (t?.closest?.('form')) typed = true;
};

// Panels that keep an unsaved draft outside a form say so with useUnsaved.
let drafts = 0;

// useUnsaved holds off reloading onto a new build while dirty is true.
export function useUnsaved(dirty: boolean) {
  useEffect(() => {
    if (!dirty) return;
    drafts++;
    return () => {
      drafts--;
    };
  }, [dirty]);
}

// A reload now loses nothing: nothing edited, no draft open and no change waiting for its answer.
const safe = () => !typed && drafts === 0 && !writesPending();

// useNewBuild reports whether a newer build is live, and reloads onto it itself when that is safe.
// Pass the current path: a page change is a safe moment.
export function useNewBuild(path: string): boolean {
  const [stale, setStale] = useState(false);
  const staleRef = useRef(false);

  useEffect(() => {
    const mine = loadedBuild();
    if (!mine) return;
    let busy = false;
    const check = async () => {
      if (busy || staleRef.current) return;
      busy = true;
      try {
        const live = await liveBuild();
        if (live && live !== mine) {
          staleRef.current = true;
          setStale(true);
          if (document.visibilityState === 'hidden' && safe()) window.location.reload();
        }
      } catch {
        /* offline or restarting: try again later */
      } finally {
        busy = false;
      }
    };
    const onVisible = () => {
      if (document.visibilityState !== 'visible') return;
      if (staleRef.current && safe()) window.location.reload();
      else void check();
    };
    const timer = window.setInterval(check, EVERY);
    document.addEventListener('visibilitychange', onVisible);
    document.addEventListener('input', onInput, true);
    document.addEventListener('change', onInput, true);
    document.addEventListener('pointerdown', onPick, true);
    document.addEventListener('click', onPick, true);
    window.addEventListener('online', check);
    void check();
    return () => {
      window.clearInterval(timer);
      document.removeEventListener('visibilitychange', onVisible);
      document.removeEventListener('input', onInput, true);
      document.removeEventListener('change', onInput, true);
      document.removeEventListener('pointerdown', onPick, true);
      document.removeEventListener('click', onPick, true);
      window.removeEventListener('online', check);
    };
  }, []);

  // A page change leaves whatever was typed behind anyway, so it is the moment to load the new build.
  const first = useRef(true);
  useEffect(() => {
    if (first.current) {
      first.current = false;
      return;
    }
    // A change still on its way would be cut off, though; the next page change or a return to the tab moves over.
    if (staleRef.current && !writesPending()) window.location.reload();
    typed = false;
  }, [path]);

  return stale;
}
