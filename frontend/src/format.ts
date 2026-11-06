export function formatBytes(n: number): string {
  if (!n) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB', 'TB', 'PB'];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n.toFixed(i === 0 ? 0 : 1)} ${units[i]}`;
}

export function formatRate(n: number): string {
  return `${formatBytes(n)}/s`;
}

export function formatPercent(v: number | null | undefined, digits = 1): string {
  return v === null || v === undefined ? '--' : `${v.toFixed(digits)}%`;
}

export function formatUptime(seconds: number): string {
  const d = Math.floor(seconds / 86400);
  const h = Math.floor((seconds % 86400) / 3600);
  const m = Math.floor((seconds % 3600) / 60);
  // Zero units are left out: "15d", "15d 4h", "2h 5m".
  const parts = d > 0 ? [`${d}d`, h && `${h}h`, m && `${m}m`] : h > 0 ? [`${h}h`, m && `${m}m`] : [`${m}m`];
  return parts.filter(Boolean).join(' ');
}

export function formatAgo(ts: number | null, now = Date.now() / 1000): string {
  if (!ts) return 'never';
  const s = Math.max(0, Math.round(now - ts));
  if (s < 60) return `${s}s ago`;
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

export function level(percent: number, warn = 80, crit = 90): 'ok' | 'warn' | 'crit' {
  if (percent >= crit) return 'crit';
  if (percent >= warn) return 'warn';
  return 'ok';
}

// formatKbps renders a Plex bandwidth figure (kilobits per second).
export function formatKbps(kbps: number): string {
  if (kbps < 1000) return `${Math.round(kbps)} kbps`;
  return `${(kbps / 1000).toFixed(kbps < 10000 ? 1 : 0)} Mbps`;
}

// formatClock renders a media position: 1:02:03 or 4:05.
export function formatClock(ms: number): string {
  const total = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = String(total % 60).padStart(2, '0');
  return h > 0 ? `${h}:${String(m).padStart(2, '0')}:${s}` : `${m}:${s}`;
}

// formatDuration renders a span of seconds in its two largest units: 3d 4h, 2h 5m, 4m 10s.
export function formatDuration(seconds: number): string {
  const s = Math.max(0, Math.round(seconds));
  const d = Math.floor(s / 86400);
  const h = Math.floor((s % 86400) / 3600);
  const m = Math.floor((s % 3600) / 60);
  // A zero second unit adds nothing: "30m", not "30m 0s".
  const two = (a: string, b: number, unit: string) => (b ? `${a} ${b}${unit}` : a);
  if (d > 0) return two(`${d}d`, h, 'h');
  if (h > 0) return two(`${h}h`, m, 'm');
  if (m > 0) return two(`${m}m`, s % 60, 's');
  return `${s}s`;
}

// formatResponse is a check's last response time, or for a heartbeat how long its job ran.
export function formatResponse(s: { type: string; latency_ms: number | null }): string {
  if (s.latency_ms === null) return '—';
  return s.type === 'heartbeat' ? formatDuration(s.latency_ms / 1000) : `${Math.round(s.latency_ms)}ms`;
}

// plural writes a count with its noun: plural(1, 'check') is "1 check", plural(3, 'check') "3 checks".
export const plural = (n: number, word: string) => `${n} ${noun(n, word)}`;

// noun is the word alone, for text that styles the number itself.
export const noun = (n: number, word: string) => (n === 1 ? word : `${word}s`);
