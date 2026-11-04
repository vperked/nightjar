import type { Notification, Severity } from '../api/types';
import { plural } from '../format';

// Shared wording and links for the bell, the notification center and the mute buttons.

export const MUTE_FOR = [
  { minutes: 60, label: '1 hour' },
  { minutes: 240, label: '4 hours' },
  { minutes: 1440, label: '24 hours' },
];

export const muteSpan = (minutes: number) => MUTE_FOR.find((m) => m.minutes === minutes)?.label ?? (minutes % 60 ? plural(minutes, 'minute') : plural(minutes / 60, 'hour'));

// timeLeft says how long a mute has to run: "47m left", "3h left".
export function timeLeft(until: number, now = Date.now() / 1000): string {
  const minutes = Math.max(1, Math.ceil((until - now) / 60));
  if (minutes < 60) return `${minutes}m left`;
  return `${Math.round(minutes / 60)}h left`;
}

export const AREAS: Record<string, string> = {
  uptime: 'Uptime checks',
  services: 'System services',
  resources: 'CPU, memory and network',
  storage: 'Disks and drives',
  system: 'Monitor and kernel',
  updates: 'Updates',
  torrents: 'qBittorrent',
  media: 'Plex',
};

// Short names for the area chips.
export const AREA_SHORT: Record<string, string> = {
  uptime: 'Uptime',
  services: 'Services',
  resources: 'Resources',
  storage: 'Storage',
  system: 'Monitor',
  updates: 'Updates',
  torrents: 'qBittorrent',
  media: 'Plex',
};

export const SEVERITIES: { value: Severity; label: string }[] = [
  { value: 'critical', label: 'Critical' },
  { value: 'warning', label: 'Warning' },
  { value: 'ok', label: 'Recovered' },
  { value: 'info', label: 'Info' },
];

// subjectLabel is what a subject is called on a mute button: "check:Plex" is "Plex".
export const subjectLabel = (subject: string) => subject.slice(subject.indexOf(':') + 1).replace(/\.service$/, '');

// linkFor is the dashboard page a notification is about.
export function linkFor(n: Pick<Notification, 'area' | 'subject'>): string {
  const [kind, name] = n.subject ? [n.subject.slice(0, n.subject.indexOf(':')), n.subject.slice(n.subject.indexOf(':') + 1)] : ['', ''];
  if (kind === 'check') return `/services/${encodeURIComponent(name)}`;
  if (kind === 'unit') return `/system?open=${encodeURIComponent(name)}`;
  if (kind === 'drive') return '/#drive-health';
  switch (n.area) {
    case 'uptime':
      return '/services';
    case 'services':
      return '/system';
    case 'updates':
      return '/updates';
    case 'torrents':
      return '/qbittorrent';
    case 'media':
      return '/plex';
  }
  return '/';
}

// plain strips the Discord markdown alerts are written in.
export const plain = (s: string) =>
  s
    .replace(/```/g, '')
    .replace(/\*\*(.+?)\*\*/g, '$1')
    .replace(/`([^`]+)`/g, '$1');

// delivery says where a notification went.
export function delivery(n: Notification): string {
  const to = n.channels.join(', ');
  switch (n.status) {
    case 'sent':
      return n.error ? `sent to ${to}; ${n.error}` : `sent to ${to}`;
    case 'digest':
      return `sent to ${to} in a summary`;
    case 'held':
      return `held for a summary (${to})`;
    case 'throttled':
      return `not sent: ${n.error || 'too many alerts'}`;
    case 'muted':
      return 'muted: sent nowhere';
    case 'maintenance':
      return 'raised during maintenance: sent nowhere';
    case 'failed':
      return `not delivered: ${n.error || 'every channel failed'}`;
  }
  return 'no channel takes these';
}
