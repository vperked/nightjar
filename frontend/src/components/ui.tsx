import type { ReactNode } from 'react';
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

// State is a square mark plus a word, so colour never carries the status alone.
export function State({ status }: { status: ServiceStatus }) {
  return <span className={`state ${status}`}>{WORD[status]}</span>;
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

export function Seg<T extends string | number>({ options, value, onChange, label }: { options: { value: T; label: ReactNode }[]; value: T; onChange: (v: T) => void; label: string }) {
  return (
    <div className="seg" role="group" aria-label={label}>
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
export function SubTabs({ label, items, current }: { label: string; items: { to: string; label: string }[]; current: string }) {
  return (
    <nav className="subtabs" aria-label={label}>
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
