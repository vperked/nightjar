// The docs, in reading order. Titles live here (each page's own first heading must match), so the
// finder can list them without loading every page; the pages themselves load with the Docs page.

export interface DocEntry {
  slug: string;
  title: string;
  group: string;
  summary: string; // one line, for the docs home
  words: string; // extra search words for the finder
}

export const DOC_GROUPS: { name: string; pages: DocEntry[] }[] = [
  {
    name: 'Getting started',
    pages: [
      { slug: 'getting-started', title: 'Getting started', summary: 'Install Nightjar, create the admin account and run the setup guide.', words: 'install first run setup code admin account make' },
      {
        slug: 'dashboard',
        title: 'Finding your way around',
        summary: "What each tab, menu and shortcut does, and how to hide what you don't use.",
        words: 'tabs header menu bell find sections phone appearance look style ruled cards dots lines density compact css theme',
      },
    ],
  },
  {
    name: 'This server',
    pages: [
      {
        slug: 'uptime-checks',
        title: 'Uptime checks',
        summary: 'Watch websites, apps, devices and cron jobs, and repair them when they go down.',
        words: 'http tcp ping tls monitor website heartbeat cron backup repair restart script docker container',
      },
      {
        slug: 'incidents',
        title: 'Incidents',
        summary: 'Each stretch of trouble and its likely cause, planned maintenance, and the weekly report.',
        words: 'outage why cause root timeline memory crash network maintenance planned downtime weekly report feedback notice',
      },
      {
        slug: 'system-services',
        title: 'System services and drives',
        summary: 'systemd services that fail or keep restarting, and SMART drive health.',
        words: 'systemd restart failed smart disks health',
      },
      {
        slug: 'resources',
        title: 'Resources, disks and the kernel',
        summary: 'CPU, memory, temperature and disk thresholds, cleanup, and the kernel.',
        words: 'cpu memory temperature thresholds cleanup cooldown watchdog',
      },
    ],
  },
  {
    name: 'Other servers',
    pages: [
      {
        slug: 'servers',
        title: 'Watching other servers',
        summary: 'Add other machines with the agent or just an SSH login, see the services on each, and log in over SSH.',
        words: 'agent https token install remote ssh key private reboot restart authorized_keys services on it',
      },
      { slug: 'analytics', title: 'Analytics', summary: 'A day, a week or a month of every server: trends, outages and disk forecasts.', words: 'history trends uptime outages disk forecast' },
    ],
  },
  {
    name: 'Alerts',
    pages: [
      {
        slug: 'alerts',
        title: 'Alert channels',
        summary: 'Discord with buttons, ntfy, Telegram, Slack, email and webhooks, and how severity works.',
        words: 'discord bot buttons interactions ntfy telegram slack email webhook severity',
      },
      {
        slug: 'notifications',
        title: 'Notifications, rules and quiet hours',
        summary: 'The alert history, who hears what, quiet hours and mutes.',
        words: 'bell routing mute snooze quiet hours history',
      },
    ],
  },
  {
    name: 'Apps and media',
    pages: [
      { slug: 'app-store', title: 'App store', summary: 'Install qBittorrent, the *arr apps, autobrr and ntfy in a click.', words: 'install sonarr radarr prowlarr qbittorrent ntfy' },
      { slug: 'updates', title: 'Updates', summary: 'Keep packages and self-hosted apps current, by hand or overnight.', words: 'upgrade packages auto update window' },
      {
        slug: 'plex',
        title: 'Plex',
        summary: 'Streams and why they transcode, the last week, stopping a stream, and cleaning up what nobody watches.',
        words: 'streams transcode reason subtitles libraries stop kill terminate manage stats history cleanup unwatched delete remove leaving keep sonarr radarr disk full',
      },
      {
        slug: 'qbittorrent',
        title: 'qBittorrent',
        summary: 'Torrents at a glance, stalled downloads recovered or replaced, and seeding limits.',
        words: 'torrents stalled stuck replace blocklist sonarr radarr seeding ratio',
      },
      { slug: 'plugins', title: 'Plugins', summary: 'Add your own checks, overview cards and alert channels.', words: 'plugins extensions add-ons custom scripts checks cards channels' },
      {
        slug: 'status-page',
        title: 'Public status page',
        summary: 'A public page of the services you choose, with 90 days of history.',
        words: 'status share public designer announcement custom css html logo branding',
      },
    ],
  },
  {
    name: 'Administration',
    pages: [
      {
        slug: 'settings',
        title: 'Settings',
        summary: 'How settings apply, where values come from, what needs a restart, and backups.',
        words: 'configuration env restart reset backup restore export import',
      },
      {
        slug: 'security',
        title: 'Account and security',
        summary: 'The admin account, two-factor sign-in, the activity log, and reaching the dashboard safely.',
        words: 'password reset reverse proxy nginx https token sign in two-factor 2fa totp authenticator recovery codes activity audit log',
      },
      { slug: 'logs', title: 'Logs', summary: "Reading the monitor's own log in the dashboard and on the machine.", words: 'journal log level debug json' },
      { slug: 'api', title: 'API', summary: 'Every JSON endpoint, for scripts and your own tools.', words: 'json curl token endpoints scripts' },
      {
        slug: 'troubleshooting',
        title: 'Troubleshooting',
        summary: 'What to check when alerts, sign-in or readings go wrong.',
        words: 'problems help not working self-test twice duplicate discord buttons authenticator heartbeat late docker socket cleanup restore',
      },
    ],
  },
].map((g) => ({ name: g.name, pages: g.pages.map((p) => ({ ...p, group: g.name })) }));

export const DOCS: DocEntry[] = DOC_GROUPS.flatMap((g) => g.pages);
