import { useEffect, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { useNavigate } from 'react-router';
import { api } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import { useServices } from '../api/ServicesContext';
import type { SettingsResponse, Unit } from '../api/types';
import { DOCS } from '../docs';
import { useDialog } from '../hooks/useDialog';
import { useToast } from './Toasts';

// The finder (Ctrl+K, ⌘K or /) jumps anywhere: a page, an uptime check, a system service, a
// setting, or a common action, by typing a few letters of it.

interface Item {
  kind: 'Page' | 'Check' | 'Service' | 'Setting' | 'Action' | 'Help';
  label: string;
  hint?: string;
  words: string; // what the query matches, lower-cased
  run: () => void;
}

// match keeps items containing every word of the query, best matches (label starts with it) first.
function match(items: Item[], query: string): Item[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return items.filter((i) => i.kind === 'Page' || i.kind === 'Action');
  const hits = items.filter((i) => words.every((w) => i.words.includes(w)));
  const score = (i: Item) => (i.label.toLowerCase().startsWith(words[0]) ? 0 : i.label.toLowerCase().includes(words[0]) ? 1 : 2);
  return hits.sort((a, b) => score(a) - score(b)).slice(0, 40);
}

const PAGES: [string, string, string][] = [
  ['Overview', '/', 'home dashboard cpu memory disk network drives health'],
  ['Uptime checks', '/services', 'services monitors ping http tcp tls'],
  ['Network', '/network', 'bandwidth traffic nload interfaces download upload speed'],
  ['Firewall', '/network/firewall', 'iptables ufw rules block allow limit ports open drop default policy outgoing'],
  ['Servers', '/servers', 'other servers remote machines agent ping hosts'],
  ['Analytics', '/servers/analytics', 'history trends uptime outages forecast reports'],
  ['Help and docs', '/docs', 'help documentation guide how to manual'],
  ['System services', '/system', 'systemd units daemons failed restart logs'],
  ['Plex', '/plex', 'streams media'],
  ['qBittorrent', '/qbittorrent', 'torrents downloads seeding'],
  [
    'App store',
    '/apps',
    'install apps store marketplace torrent sonarr radarr prowlarr jackett lidarr autobrr unpackerr ntfy qbittorrent navidrome music ombi requests syncthing sync filebrowser files adguard dns adblock gitea git memos notes',
  ],
  ['Updates', '/updates', 'upgrade packages apps'],
  ['Notifications', '/notifications', 'alerts inbox history bell unread'],
  ['Incidents', '/incidents', 'outages why cause root timeline what happened down'],
  ['Maintenance', '/incidents/maintenance', 'planned downtime window quiet updating'],
  ['Weekly report', '/incidents/report', 'summary week monday digest forecast'],
  ['Media cleanup', '/plex/cleanup', 'unwatched remove delete leaving keep sonarr radarr ratio free space'],
  ['Notification rules', '/notifications/rules', 'alerts routing channels quiet hours mute snooze discord ntfy email'],
  ['Monitor log', '/logs', 'logs logging debug errors warnings journal output'],
  ['Settings', '/settings', 'configuration options thresholds alerts'],
  ['Setup guide', '/setup', 'get started onboarding connect'],
  ['Status page designer', '/status-page', 'public status page design customize announcement groups'],
  ['Plugins', '/plugins', 'plugins extensions add-ons custom checks cards alert channels scripts'],
];

export function Finder({ onToggleTheme }: { onToggleTheme: () => void }) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const [units, setUnits] = useState<Unit[]>([]);
  const [settings, setSettings] = useState<SettingsResponse>();
  const input = useRef<HTMLInputElement>(null);
  // Focus starts in the search box (its first control), Tab stays in the finder, and closing it
  // puts focus back where it was.
  const box = useRef<HTMLDivElement>(null);
  useDialog(box, open);
  const navigate = useNavigate();
  const notify = useToast();
  const { data: checks } = useServices();
  // A viewer gets no actions that change things, and no settings, which are the admin's.
  const canEdit = useCanEdit();

  // Ctrl+K / ⌘K anywhere, or / when not typing in a field.
  useEffect(() => {
    const onKey = (e: globalThis.KeyboardEvent) => {
      const typing = e.target instanceof HTMLElement && (e.target.isContentEditable || ['INPUT', 'TEXTAREA', 'SELECT'].includes(e.target.tagName));
      if ((e.key === 'k' && (e.ctrlKey || e.metaKey)) || (e.key === '/' && !typing && !e.ctrlKey && !e.metaKey)) {
        e.preventDefault();
        setOpen((o) => !o);
      }
    };
    const onOpen = () => setOpen(true);
    window.addEventListener('keydown', onKey);
    window.addEventListener('open-finder', onOpen);
    return () => {
      window.removeEventListener('keydown', onKey);
      window.removeEventListener('open-finder', onOpen);
    };
  }, []);

  // Services and settings are fetched when the finder opens, not on every page.
  useEffect(() => {
    if (!open) return;
    setQuery('');
    setActive(0);
    api.units().then(
      (r) => setUnits(r.units),
      () => undefined,
    );
    if (!settings && canEdit) api.settings().then(setSettings, () => undefined);
  }, [open]); // only on opening: settings arriving later must not clear what was typed

  // Worked out only while open: closed, the checks' poll would rebuild the list for nobody.
  const items = useMemo<Item[]>(() => {
    if (!open) return [];
    const go = (to: string) => () => navigate(to);
    const out: Item[] = PAGES.map(([label, to, words]) => ({ kind: 'Page', label, words: `${label} ${words}`.toLowerCase(), run: go(to) }));
    for (const d of DOCS) out.push({ kind: 'Help', label: d.title, hint: d.group, words: `${d.title} ${d.words} help docs how`.toLowerCase(), run: go(`/docs/${d.slug}`) });
    if (canEdit)
      out.push(
        { kind: 'Action', label: 'Add an uptime check', words: 'add new uptime check monitor service', run: go('/services#add') },
        { kind: 'Action', label: 'Add a torrent', words: 'add torrent magnet download qbittorrent', run: go('/qbittorrent?add=1') },
        {
          kind: 'Action',
          label: 'Check for updates now',
          words: 'check for updates now upgrade',
          run: () => {
            api.checkUpdates().then(
              () => notify({ tone: 'info', title: 'Checking for updates', body: 'Results appear on the Updates page.' }),
              (e: Error) => notify({ tone: 'error', title: 'Could not start the check', body: e.message }),
            );
            navigate('/updates');
          },
        },
        { kind: 'Action', label: 'Send a test alert', words: 'send test alert notification discord', run: go('/settings?find=test%20alert') },
      );
    out.push({ kind: 'Action', label: 'Switch light / dark theme', words: 'theme dark light mode toggle', run: onToggleTheme });
    for (const c of checks?.services ?? []) {
      out.push({ kind: 'Check', label: c.name, hint: `${c.status} · ${c.target}`, words: `${c.name} ${c.target} ${c.type}`.toLowerCase(), run: go(`/services/${encodeURIComponent(c.name)}`) });
    }
    for (const u of units) {
      const name = u.name.replace(/\.service$/, '');
      out.push({ kind: 'Service', label: name, hint: `${u.active} · ${u.description}`, words: `${name} ${u.description}`.toLowerCase(), run: go(`/system?open=${encodeURIComponent(u.name)}`) });
    }
    for (const g of settings?.groups ?? []) {
      for (const f of g.fields) {
        out.push({ kind: 'Setting', label: f.label, hint: g.name, words: `${f.label} ${f.key} ${g.name} ${f.help ?? ''}`.toLowerCase(), run: go(`/settings?find=${encodeURIComponent(f.key)}`) });
      }
    }
    return out;
  }, [open, checks, units, settings, navigate, notify, onToggleTheme, canEdit]);

  const shown = useMemo(() => match(items, query), [items, query]);
  useEffect(() => setActive(0), [query]);
  useEffect(() => {
    if (open) document.getElementById(`finder-${active}`)?.scrollIntoView?.({ block: 'nearest' });
  }, [active, open]);

  if (!open) return null;
  const choose = (i: Item | undefined) => {
    if (!i) return;
    setOpen(false);
    i.run();
  };
  const onKey = (e: KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      setActive((a) => Math.min(shown.length - 1, a + 1));
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      setActive((a) => Math.max(0, a - 1));
    } else if (e.key === 'Enter') {
      e.preventDefault();
      choose(shown[active]);
    } else if (e.key === 'Escape') {
      // Escape closes only the finder, not a sheet or menu open underneath it.
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
    }
  };

  return (
    <div className="finder-backdrop" onMouseDown={() => setOpen(false)}>
      <div ref={box} className="finder" role="dialog" aria-modal="true" aria-label="Find" onMouseDown={(e) => e.stopPropagation()}>
        <div className="finder-input">
          <SearchIcon />
          <input
            ref={input}
            role="combobox"
            aria-expanded="true"
            aria-controls="finder-list"
            aria-activedescendant={shown.length ? `finder-${active}` : undefined}
            aria-label="Find a page, check, service or setting"
            placeholder="Find a page, check, service or setting…"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            onKeyDown={onKey}
          />
          <kbd>esc</kbd>
        </div>
        <ul className="finder-list" id="finder-list" role="listbox" aria-label="Results">
          {shown.length === 0 && <li className="finder-empty">Nothing matches “{query}”.</li>}
          {shown.map((i, n) => (
            <li
              key={`${i.kind}:${i.label}:${i.hint ?? ''}`}
              id={`finder-${n}`}
              role="option"
              aria-selected={n === active}
              className={n === active ? 'active' : ''}
              onMouseEnter={() => setActive(n)}
              onClick={() => choose(i)}
            >
              <span className={`finder-kind ${i.kind.toLowerCase()}`}>{i.kind}</span>
              <span className="finder-label truncate">{i.label}</span>
              {i.hint && <span className="finder-hint truncate">{i.hint}</span>}
            </li>
          ))}
        </ul>
        <div className="finder-foot">
          <span>
            <kbd>↑</kbd>
            <kbd>↓</kbd> move
          </span>
          <span>
            <kbd>enter</kbd> open
          </span>
          <span>
            <kbd>/</kbd> or <kbd>ctrl</kbd>
            <kbd>k</kbd> anywhere
          </span>
        </div>
      </div>
    </div>
  );
}

export const SearchIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
    <circle cx="11" cy="11" r="6.5" />
    <path d="m20 20-4.2-4.2" />
  </svg>
);

// shortcutLabel is the finder's shortcut as this keyboard writes it.
export const shortcutLabel = typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.userAgent) ? '⌘K' : 'Ctrl K';

// openFinder lets buttons elsewhere open it.
export const openFinder = () => window.dispatchEvent(new Event('open-finder'));
