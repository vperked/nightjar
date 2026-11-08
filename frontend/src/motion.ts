import { useEffect } from 'react';

// Motion that needs script: the theme reveal's origin, the light that follows the cursor along card
// edges, and page titles that decode into place. Each one stands down when the visitor asks for
// reduced motion, and where the browser lacks what it needs (as in tests).

export const reducedMotion = () => window.matchMedia?.('(prefers-reduced-motion: reduce)').matches ?? true;

// The last place the pointer went down, so the theme can spread from the click that switched it.
let lastPress: { x: number; y: number; at: number } | undefined;
if (typeof window !== 'undefined') {
  window.addEventListener('pointerdown', (e) => (lastPress = { x: e.clientX, y: e.clientY, at: Date.now() }), { capture: true, passive: true });
}

// revealTheme spreads the new theme in a circle from the click that asked for it (or from the
// top-right corner, where the account menu is, when the keyboard did). It runs inside a view
// transition, which has already captured the old theme as a picture.
export function revealTheme(ready: Promise<unknown> | undefined) {
  ready
    ?.then(() => {
      const recent = lastPress && Date.now() - lastPress.at < 1500 ? lastPress : undefined;
      const x = recent?.x ?? window.innerWidth - 40;
      const y = recent?.y ?? 28;
      const r = Math.hypot(Math.max(x, window.innerWidth - x), Math.max(y, window.innerHeight - y));
      document.documentElement.animate?.(
        { clipPath: [`circle(0px at ${x}px ${y}px)`, `circle(${r}px at ${x}px ${y}px)`] },
        { duration: 560, easing: 'cubic-bezier(0.2, 0.7, 0.2, 1)', pseudoElement: '::view-transition-new(root)' },
      );
    })
    .catch(() => undefined);
}

const CARDS = '.section, .readout, .panel, .stats-row > div';

// useEdgeLight tells the card under the pointer where the pointer is, as --mx and --my, so its
// edge can catch the light there (see motion.css). One listener serves every card.
export function useEdgeLight() {
  useEffect(() => {
    if (reducedMotion() || !window.matchMedia?.('(hover: hover)').matches) return;
    let frame = 0;
    let last: PointerEvent | undefined;
    const place = () => {
      frame = 0;
      const card = last && (last.target as Element | null)?.closest?.<HTMLElement>(CARDS);
      if (!card || !last) return;
      const r = card.getBoundingClientRect();
      card.style.setProperty('--mx', `${last.clientX - r.left}px`);
      card.style.setProperty('--my', `${last.clientY - r.top}px`);
    };
    const move = (e: PointerEvent) => {
      last = e;
      if (!frame) frame = requestAnimationFrame(place);
    };
    window.addEventListener('pointermove', move, { passive: true });
    return () => {
      window.removeEventListener('pointermove', move);
      if (frame) cancelAnimationFrame(frame);
    };
  }, []);
}

const UPPER = 'ABCDEFGHJKLMNPQRSTUVWXYZ0123456789#%&*+=/<>';
const LOWER = 'abcdefghijkmnopqrstuvwxyz0123456789';
const pick = (set: string) => set[Math.floor(Math.random() * set.length)];

// decode scrambles an element's text and lets each character settle, left to right with a little
// jitter. It works on the text nodes React made, so it gives way at once if React changes one
// (data arriving mid-way), and otherwise always ends on the true text. It returns a cancel function.
function decode(el: HTMLElement): () => void {
  const nodes: { node: Text; text: string; written: string }[] = [];
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    const text = n.nodeValue ?? '';
    if (text.trim()) nodes.push({ node: n as Text, text, written: text });
  }
  const total = nodes.reduce((sum, n) => sum + n.text.length, 0);
  if (!total) return () => undefined;
  const duration = Math.min(700, 260 + total * 14);
  const settle: number[] = Array.from({ length: total }, (_, i) => (i / total) * duration * 0.7 + Math.random() * duration * 0.3);
  const start = performance.now();
  let frame = 0;
  let stopped = false;

  const restore = () => {
    for (const n of nodes) if (n.node.nodeValue === n.written) n.node.nodeValue = n.text;
  };
  const step = (now: number) => {
    const elapsed = now - start;
    let k = 0;
    let done = true;
    for (const n of nodes) {
      // React rewrote this node: leave everything as React has it.
      if (n.node.nodeValue !== n.written) {
        stopped = true;
        return;
      }
      let out = '';
      for (const ch of n.text) {
        if (/\s/.test(ch) || elapsed >= settle[k]) out += ch;
        else {
          out += pick(ch === ch.toLowerCase() && ch !== ch.toUpperCase() ? LOWER : UPPER);
          done = false;
        }
        k++;
      }
      n.node.nodeValue = n.written = out;
    }
    if (done) {
      restore();
      stopped = true;
      return;
    }
    frame = requestAnimationFrame(step);
  };
  frame = requestAnimationFrame(step);
  return () => {
    if (stopped) return;
    stopped = true;
    cancelAnimationFrame(frame);
    restore();
  };
}

// useTitleDecode decodes the page title each time the page changes. The title may only appear
// once the page's data loads, so it watches for it for a moment.
export function useTitleDecode(page: string) {
  useEffect(() => {
    if (reducedMotion() || typeof MutationObserver === 'undefined') return;
    const find = () => document.querySelector<HTMLElement>('.page main h1');
    let cancel = () => undefined as void;
    const now = find();
    if (now) {
      cancel = decode(now);
      return () => cancel();
    }
    const watch = new MutationObserver(() => {
      const h1 = find();
      if (h1) {
        watch.disconnect();
        cancel = decode(h1);
      }
    });
    watch.observe(document.body, { childList: true, subtree: true });
    const giveUp = setTimeout(() => watch.disconnect(), 1500);
    return () => {
      watch.disconnect();
      clearTimeout(giveUp);
      cancel();
    };
  }, [page]);
}

// The page's headline figures: the overview's readings, each page's stat cards, each server's meters.
const FIGURES = '.page main :is(.readout-value, .host-metric-value)';
// A figure is counted only when it starts with its number ("99.61%", "1,909", "58m"), not when a
// number sits inside words ("since Oct 31").
const LEADING = /^(\s*)(\d[\d,]*(?:\.\d+)?)/;

// countUp runs a figure's leading number up from zero to what React wrote. Like decode it writes
// only to React's own text node, and stops the moment React changes that node, so live data always
// wins. It returns a cancel function.
function countUp(el: HTMLElement): () => void {
  const walker = document.createTreeWalker(el, NodeFilter.SHOW_TEXT);
  let node: Text | null = null;
  for (let n = walker.nextNode(); n; n = walker.nextNode()) {
    if ((n.nodeValue ?? '').trim()) {
      node = n as Text;
      break;
    }
  }
  const text = node?.nodeValue ?? '';
  const m = LEADING.exec(text);
  if (!node || !m) return () => undefined;
  const target = Number(m[2].replace(/,/g, ''));
  if (!Number.isFinite(target) || target === 0) return () => undefined;
  const decimals = m[2].split('.')[1]?.length ?? 0;
  const grouped = m[2].includes(',');
  const rest = text.slice(m[0].length);
  const show = (v: number) => m[1] + (grouped ? v.toLocaleString('en-US', { minimumFractionDigits: decimals, maximumFractionDigits: decimals }) : v.toFixed(decimals)) + rest;
  const duration = 650;
  const start = performance.now();
  let written = text;
  let frame = 0;
  let stopped = false;
  const step = (now: number) => {
    if (node!.nodeValue !== written) {
      stopped = true; // React wrote a new value: it stands.
      return;
    }
    const p = Math.min(1, (now - start) / duration);
    const eased = 1 - Math.pow(1 - p, 3);
    node!.nodeValue = written = p < 1 ? show(target * eased) : text;
    if (p < 1) frame = requestAnimationFrame(step);
    else stopped = true;
  };
  node.nodeValue = written = show(0);
  frame = requestAnimationFrame(step);
  return () => {
    if (stopped) return;
    stopped = true;
    cancelAnimationFrame(frame);
    if (node!.nodeValue === written) node!.nodeValue = text;
  };
}

// useCountUp counts each headline figure up from zero the first time it appears on a page. Figures
// come with the page's data, a moment after the page, so it watches for them for a few seconds;
// each is counted once, and later updates are left to the live data.
export function useCountUp(page: string) {
  useEffect(() => {
    if (reducedMotion() || typeof MutationObserver === 'undefined') return;
    const counted = new WeakSet<Element>();
    const cancels: (() => void)[] = [];
    const scan = () => {
      for (const el of document.querySelectorAll<HTMLElement>(FIGURES)) {
        if (counted.has(el)) continue;
        counted.add(el);
        cancels.push(countUp(el));
      }
    };
    scan();
    const watch = new MutationObserver(scan);
    watch.observe(document.body, { childList: true, subtree: true });
    const giveUp = setTimeout(() => watch.disconnect(), 3000);
    return () => {
      watch.disconnect();
      clearTimeout(giveUp);
      for (const c of cancels) c();
    };
  }, [page]);
}
