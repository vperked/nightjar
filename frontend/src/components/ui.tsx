import { useEffect, useLayoutEffect, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { isDemo } from '../api/client';
import { useConfig } from '../api/ConfigContext';
import type { Beat, ServiceStatus } from '../api/types';
import { formatPercent, level } from '../format';

export function Section({ label, aside, children }: { label: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <section className="section">
      <div className="section-head">
        <span className="label">{label}</span>
        {aside}
      </div>
      {children}
    </section>
  );
}

const WORD: Record<ServiceStatus, string> = { up: 'Up', down: 'Down', unknown: 'Pending', paused: 'Paused', maintenance: 'Maintenance' };

// State is a square mark plus a word, so colour never carries the status alone. When the status
// changes while it is on screen, it flashes once (.changed, motion.css), so a change is not missed.
export function State({ status }: { status: ServiceStatus }) {
  const last = useRef(status);
  const [changes, setChanges] = useState(0);
  useEffect(() => {
    if (last.current === status) return;
    last.current = status;
    setChanges((n) => n + 1);
  }, [status]);
  // A new key per change remounts the mark, which restarts its flash.
  return (
    <span key={changes} className={`state ${status}${changes ? ' changed' : ''}`}>
      {WORD[status]}
    </span>
  );
}

// Ticks shows the last `slots` checks, oldest on the left, padded with empty slots.
export function Ticks({ beats, slots = 40, tall = false }: { beats: Beat[]; slots?: number; tall?: boolean }) {
  const recent = beats.slice(-slots);
  const fails = recent.filter((b) => !b.up).length;
  return (
    <div className={`ticks ${tall ? 'tall' : ''}`} role="img" aria-label={`Last ${recent.length} checks, ${fails} failed`}>
      {Array.from({ length: Math.max(0, slots - recent.length) }, (_, i) => (
        <span key={`p${i}`} className="tick pad" />
      ))}
      {recent.map((b) => {
        const ms = b.latency_ms === null || b.latency_ms === undefined ? '' : ` · ${Math.round(b.latency_ms)} ms`;
        return <span key={b.ts} className={`tick ${b.up ? 'up' : 'down'}`} title={`${new Date(b.ts * 1000).toLocaleString()} · ${b.up ? 'up' : 'down'}${ms}`} />;
      })}
    </div>
  );
}

export function Pct({ value, digits = 2 }: { value: number | null; digits?: number }) {
  const cls = value === null ? 'ink-3' : value >= 99 ? 'pct-good' : value >= 95 ? 'pct-fair' : 'pct-poor';
  return <span className={`num ${cls}`}>{formatPercent(value, digits)}</span>;
}

export function Bar({ percent, label }: { percent: number; label: string }) {
  const lvl = level(percent);
  return (
    <div className={`bar ${lvl === 'ok' ? '' : lvl}`} role="meter" aria-label={label} aria-valuenow={Math.round(percent)} aria-valuemin={0} aria-valuemax={100}>
      <span style={{ width: `${Math.max(0, Math.min(100, percent))}%` }} />
    </div>
  );
}

// slide moves a highlight (thumb) under the element `on` inside box, gliding from where it was
// unless asked to jump. It works in offsets, so the box may scroll.
function slide(thumb: HTMLElement | null, on: HTMLElement | null | undefined, jump: boolean) {
  if (!thumb) return;
  if (!on) {
    thumb.style.opacity = '0';
    return;
  }
  if (jump) thumb.style.transition = 'none';
  thumb.style.opacity = '1';
  thumb.style.width = `${on.offsetWidth}px`;
  thumb.style.transform = `translateX(${on.offsetLeft}px)`;
  if (jump) {
    void thumb.offsetWidth; // apply the jump before turning the glide back on
    thumb.style.transition = '';
  }
}

// onResize calls fn when el's size changes, but not for the observer's first report, which only
// says the size it started at: answering that would cut short a glide that has just begun.
function onResize(el: HTMLElement, fn: () => void): () => void {
  if (typeof ResizeObserver === 'undefined') return () => undefined;
  let size = '';
  const ro = new ResizeObserver(([e]) => {
    const now = `${Math.round(e.contentRect.width)}x${Math.round(e.contentRect.height)}`;
    if (size && now !== size) fn();
    size = now;
  });
  ro.observe(el);
  return () => ro.disconnect();
}

// useSlide keeps a thumb under a group's chosen item (found by `selector`), gliding on each change
// and jumping when the group itself changes size (fonts arriving, a window resize).
function useSlide<E extends HTMLElement>(selector: string, deps: readonly unknown[]) {
  const box = useRef<E>(null);
  const thumb = useRef<HTMLSpanElement>(null);
  const placed = useRef(false);
  useLayoutEffect(() => {
    slide(thumb.current, box.current?.querySelector<HTMLElement>(selector), !placed.current);
    placed.current = true;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, deps);
  useEffect(() => {
    const el = box.current;
    return el ? onResize(el, () => slide(thumb.current, el.querySelector<HTMLElement>(selector), true)) : undefined;
  }, [selector]);
  return { box, thumb };
}

export function Seg<T extends string | number>({ options, value, onChange, label }: { options: { value: T; label: ReactNode }[]; value: T; onChange: (v: T) => void; label: string }) {
  const { box, thumb } = useSlide<HTMLDivElement>('button.active', [value, options.length]);
  return (
    <div className="seg slides" role="group" aria-label={label} ref={box}>
      <span className="seg-thumb" ref={thumb} aria-hidden="true" />
      {options.map((o) => (
        <button type="button" key={String(o.value)} className={o.value === value ? 'active' : ''} aria-pressed={o.value === value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

// DemoBanner marks pages showing demo data instead of a real backend.
export function DemoBanner() {
  if (!isDemo()) return null;
  return (
    <div className="demo-banner" role="note">
      Demo data · no backend is connected. Changes stay in this browser tab.
    </div>
  );
}

// SubTabs switches between pages that share one nav tab (Services: checks and system services;
// Media: Plex and qBittorrent).
// Each group of sub-tabs is a new element on each page, so where its underline last stood is kept
// here, by group: on the next page the underline starts there and glides to the new tab.
const lastTab = new Map<string, { left: number; width: number }>();

export function SubTabs({ label, items, current }: { label: string; items: { to: string; label: string }[]; current: string }) {
  const box = useRef<HTMLElement>(null);
  const ink = useRef<HTMLSpanElement>(null);
  useLayoutEffect(() => {
    const el = ink.current;
    const on = box.current?.querySelector<HTMLElement>('a.active');
    if (!el || !on) return;
    const from = lastTab.get(label);
    if (from && (from.left !== on.offsetLeft || from.width !== on.offsetWidth)) {
      el.style.transition = 'none';
      el.style.width = `${from.width}px`;
      el.style.transform = `translateX(${from.left}px)`;
      void el.offsetWidth;
      el.style.transition = '';
    }
    slide(el, on, !from);
    lastTab.set(label, { left: on.offsetLeft, width: on.offsetWidth });
  }, [label, current]);
  useEffect(() => {
    const el = box.current;
    if (!el) return;
    return onResize(el, () => {
      const on = el.querySelector<HTMLElement>('a.active');
      slide(ink.current, on, true);
      if (on) lastTab.set(label, { left: on.offsetLeft, width: on.offsetWidth });
    });
  }, [label]);
  return (
    <nav className="subtabs slides" aria-label={label} ref={box}>
      <span className="subtabs-ink" ref={ink} aria-hidden="true" />
      {items.map((i) => (
        <Link key={i.to} className={i.to === current ? 'active' : ''} to={i.to} aria-current={i.to === current ? 'page' : undefined}>
          {i.label}
        </Link>
      ))}
    </nav>
  );
}

// ServersTabs switches between the servers and their analytics.
export function ServersTabs({ current }: { current: '/servers' | '/servers/analytics' }) {
  return (
    <SubTabs
      label="Servers"
      current={current}
      items={[
        { to: '/servers', label: 'Servers' },
        { to: '/servers/analytics', label: 'Analytics' },
      ]}
    />
  );
}

// Switch is an on/off switch, with a word beside it: On or Off, or text instead (Auto, say).
export function Switch({ on, label, title = label, disabled, text, onChange }: { on: boolean; label: string; title?: string; disabled?: boolean; text?: ReactNode; onChange: (on: boolean) => void }) {
  return (
    <label className={`switch ${disabled ? 'disabled' : ''}`} title={title}>
      <input type="checkbox" role="switch" aria-label={label} checked={on} disabled={disabled} onChange={(e) => onChange(e.target.checked)} />
      <span className="switch-track" aria-hidden="true" />
      <span className="switch-text">{text ?? (on ? 'On' : 'Off')}</span>
    </label>
  );
}

// NetworkTabs switches between the live network meter, the firewall and Tailscale.
export function NetworkTabs({ current }: { current: '/network' | '/network/firewall' | '/network/tailscale' }) {
  return (
    <SubTabs
      label="Network"
      current={current}
      items={[
        { to: '/network', label: 'Live' },
        { to: '/network/firewall', label: 'Firewall' },
        { to: '/network/tailscale', label: 'Tailscale' },
      ]}
    />
  );
}

// MediaTabs joins Plex, qBittorrent and the media cleanup under one Media tab, for those watched.
export function MediaTabs({ current }: { current: '/plex' | '/qbittorrent' | '/plex/cleanup' }) {
  const config = useConfig();
  const items = [
    ...(config?.plex_enabled ? [{ to: '/plex', label: 'Plex' }] : []),
    ...(config?.qbittorrent_enabled ? [{ to: '/qbittorrent', label: 'qBittorrent' }] : []),
    ...(config?.plex_enabled ? [{ to: '/plex/cleanup', label: 'Cleanup' }] : []),
  ];
  if (items.length < 2) return null;
  return <SubTabs label="Media" current={current} items={items} />;
}

// AppsTabs joins the app store and updates under one Apps tab.
export function AppsTabs({ current }: { current: '/apps' | '/updates' }) {
  return (
    <SubTabs
      label="Apps"
      current={current}
      items={[
        { to: '/apps', label: 'App store' },
        { to: '/updates', label: 'Updates' },
      ]}
    />
  );
}
