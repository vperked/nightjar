import type { ReactNode } from 'react';

// A Settings group: an icon, a name and a line saying what it covers, then its settings. The page's
// sidebar uses the same icons.

const ICONS: Record<string, string> = {
  Account: 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 20a8 8 0 0 1 16 0',
  Sections: 'M4 5h16M4 12h16M4 19h10M18 17l2 2 3-3',
  Activity: 'M4 6h16M4 12h10M4 18h7M17 15l2 2 3-4',
  Backup: 'M12 3v12M7 10l5 5 5-5M4 19h16',
  Appearance: 'M12 21a9 9 0 1 1 9-9c0 2-1.5 3-3.5 3H16a2 2 0 0 0-1.5 3.3A1.6 1.6 0 0 1 12 21zM7.5 11h.01M10.5 7.5h.01M15 8h.01',
  Monitoring: 'M3 12h4l3-8 4 16 3-8h4',
  Notifications: 'M6 16V11a6 6 0 1 1 12 0v5l2 2H4zM10 20a2 2 0 0 0 4 0',
  'Alert channels': 'M3 11l18-8-8 18-2-8z',
  Resources: 'M7 7h10v10H7zM10 3v4M14 3v4M10 17v4M14 17v4M3 10h4M3 14h4M17 10h4M17 14h4',
  Disks: 'M4 6c0-1.7 3.6-3 8-3s8 1.3 8 3-3.6 3-8 3-8-1.3-8-3zM4 6v12c0 1.7 3.6 3 8 3s8-1.3 8-3V6M4 12c0 1.7 3.6 3 8 3s8-1.3 8-3',
  Logs: 'M6 3h9l3 3v15H6zM9 9h6M9 13h6M9 17h4',
  'Media directory': 'M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z',
  Kernel: 'M9 4h6l5 5v6l-5 5H9l-5-5V9zM12 9v4M12 16v.01',
  Watchdog: 'M12 3l7 3v6c0 4.5-3 7.5-7 9-4-1.5-7-4.5-7-9V6z',
  'System services': 'M12 3l9 5-9 5-9-5zM3 13l9 5 9-5',
  'Drive health': 'M20.8 8.6a5 5 0 0 0-8.8-3.2 5 5 0 0 0-8.8 3.2c0 5 8.8 10.4 8.8 10.4s8.8-5.4 8.8-10.4zM7 11h3l1.5-2 2 4 1.5-2h2',
  Updates: 'M20 11a8 8 0 0 0-14.6-4.5M4 4v4h4M4 13a8 8 0 0 0 14.6 4.5M20 20v-4h-4',
  qBittorrent: 'M12 4v11M7 10l5 5 5-5M5 20h14',
  Plex: 'M6 4l13 8-13 8z',
  Dashboard: 'M4 4h7v7H4zM13 4h7v4h-7zM13 10h7v10h-7zM4 13h7v7H4z',
  'Storage and logging': 'M4 5h16v5H4zM4 14h16v5H4zM8 7.5h.01M8 16.5h.01',
};

const TEXT: Record<string, string> = {
  Account: 'Your sign-in. Changing the password signs out every other browser.',
  Sections: "Hide what you don't use. Monitoring and alerts carry on; only the dashboard leaves it out.",
  Activity: 'What was changed and by whom: here, through Discord, or by Nightjar.',
  Backup: "Nightjar's own setup in one file, for a new machine or to undo a mistake.",
  Appearance: 'How the dashboard looks in every browser: its style, charts, density and your own CSS.',
  Monitoring: 'How often the monitor looks, how alerts repeat, and how long history is kept.',
  Notifications: 'Discord, where alerts go first, and links back to this dashboard.',
  'Alert channels': 'Phone push, chat and email, alongside Discord.',
  Resources: 'When CPU, memory, GPU, temperature and network count as too much.',
  Disks: 'When disks are getting full, and when the monitor makes room.',
  Logs: 'Trimming oversized logs and clearing old rotated ones.',
  'Media directory': 'Alerts when a media folder grows past a size.',
  Kernel: 'Crashes, hardware errors and unexpected reboots.',
  Watchdog: 'Noticing, and recovering from, a stalled monitor or a frozen machine.',
  'System services': 'systemd services that fail or keep restarting.',
  'Drive health': 'SMART checks and drive temperature limits.',
  Updates: 'Checking for updates, and the window automatic ones may run in.',
  qBittorrent: 'The connection, stalled-torrent recovery and seeding limits.',
  Plex: 'The connection, stream alerts and transcode limits.',
  Dashboard: 'The public status page and how this dashboard is served.',
  'Storage and logging': "The database, the monitor's own log and its level.",
};

export function GroupIcon({ name }: { name: string }) {
  return (
    <svg className="set-icon" viewBox="0 0 24 24" aria-hidden="true">
      <path d={ICONS[name] ?? ICONS.Monitoring} />
    </svg>
  );
}

export function SettingsGroup({ id, name, aside, children }: { id: string; name: string; aside?: ReactNode; children: ReactNode }) {
  return (
    <section className="section set-group" id={id} aria-labelledby={`${id}-title`}>
      <header className="set-group-head">
        <span className="set-group-icon">
          <GroupIcon name={name} />
        </span>
        <div className="min0">
          <h2 id={`${id}-title`}>{name}</h2>
          {TEXT[name] && <p>{TEXT[name]}</p>}
        </div>
        {aside && <div className="set-group-aside">{aside}</div>}
      </header>
      <div className="set-group-body">{children}</div>
    </section>
  );
}
