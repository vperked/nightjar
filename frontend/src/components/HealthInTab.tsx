import { useEffect } from 'react';
import { useLocation } from 'react-router';
import { api } from '../api/client';
import { useServices } from '../api/ServicesContext';
import { usePoll } from '../hooks/usePoll';
import { isProblem } from './unitState';

const PAGE: [string, string][] = [
  ['/services', 'Services'],
  ['/system', 'System services'],
  ['/network', 'Network'],
  ['/plex', 'Plex'],
  ['/qbittorrent', 'qBittorrent'],
  ['/updates', 'Updates'],
  ['/apps', 'App store'],
  ['/logs', 'Monitor log'],
  ['/notifications', 'Notifications'],
  ['/settings', 'Settings'],
  ['/setup', 'Setup'],
  ['/status-page', 'Status page'],
  ['/plugins', 'Plugins'],
];

// Coloured like the dashboard's own status marks.
const COLOR = { good: '#0ca30c', bad: '#d03b3b' };

// MARK is Nightjar's crescent and star on a black tile.
export const MARK = `<rect width='32' height='32' rx='8' fill='#0a0a0a'/><path d='M26 19.2A10.9 10.9 0 1 1 12.8 6a8.7 8.7 0 0 0 13.2 13.2z' fill='none' stroke='#fff' stroke-width='2.6' stroke-linejoin='round'/><circle cx='22.9' cy='7.6' r='1.9' fill='#fff'/>`;

// favicon is the mark with a green dot while all is well, or a red badge counting the problems,
// which reads better than the mark at tab size.
function favicon(color: string, count: number): string {
  const body = count
    ? `<circle cx='16' cy='16' r='15' fill='${color}'/><text x='16' y='21.5' font-family='system-ui,sans-serif' font-size='15' font-weight='700' text-anchor='middle' fill='white'>${count > 9 ? '9+' : count}</text>`
    : `${MARK}<circle cx='26' cy='26' r='5.2' fill='${color}' stroke='#fff' stroke-width='1.8'/>`;
  return 'data:image/svg+xml,' + encodeURIComponent(`<svg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'>${body}</svg>`);
}

// useProblems counts what is wrong right now: uptime checks down, and system services failed or
// restarting. The header's mark and the browser tab both show it.
export function useProblems(enabled: boolean): number {
  const { data: checks } = useServices();
  const { data: units } = usePoll(api.units, 30000, enabled);
  return (checks?.services ?? []).filter((s) => s.status === 'down').length + (units?.units ?? []).filter(isProblem).length;
}

// HealthInTab puts the server's health in the browser tab: a green dot, or a red one with the
// number of problems, and "2 down" ahead of the page name, so another tab shows it at a glance.
export function HealthInTab({ down }: { down: number }) {
  const location = useLocation();
  const page = PAGE.find(([p]) => location.pathname.startsWith(p))?.[1] ?? (location.pathname === '/' ? 'Overview' : '');

  useEffect(() => {
    if (location.pathname === '/status') return; // the public page sets its own
    document.title = [down ? `${down} down` : '', page, 'Nightjar'].filter(Boolean).join(' · ');
    let link = document.querySelector<HTMLLinkElement>('link[rel="icon"]');
    if (!link) {
      link = document.createElement('link');
      link.rel = 'icon';
      document.head.appendChild(link);
    }
    link.href = favicon(down ? COLOR.bad : COLOR.good, down);
  }, [down, page, location.pathname]);
  return null;
}
