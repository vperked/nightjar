# Settings

**[Settings](/settings)** holds every setting, grouped and explained. About 45 essential ones show by default; **advanced** shows the tuning knobs, and searching finds any of them, advanced or not. Find (<kbd>Ctrl</kbd> <kbd>K</kbd>) searches settings too.

## Saving

Change as many as you like, then save them together. The page says before you save what each change does:

- **Most settings** (thresholds, intervals, the update window, qBittorrent rules, the status page, the log level) apply from the next monitoring cycle, with no restart.
- **Settings the monitor connects with at startup** (webhooks, alert channels, the Plex and qBittorrent connections, log files, the watchdog) restart it in place. It keeps the same process, so systemd keeps tracking it, and the page waits for it to come back.

**Test connection** (Plex, qBittorrent) and **Send test alert** use the values on screen, saved or not. Webhooks, tokens and passwords are never sent back to the browser: the page only says whether they are set.

## Where values come from

The monitor starts from its environment: the `.env` file (or the file given with `-env`), which `env.recommended` documents in full. A setting saved in the dashboard overrides the environment, and **reset** returns it to the `.env` value or the default.

A few settings decide how the dashboard itself is reached and stored, so only the environment can change them: `WEB_HOST`, `WEB_PORT`, `WEB_AUTH_TOKEN`, `HISTORY_DB` and the log files.

> **Tip:** values in `.env` must not carry comments on the same line when systemd reads the file, because systemd keeps them as part of the value.

## Other sections of the page

- **Account**: your username, and changing your password. See [Account and security](/docs/security).
- **Sections**: hide tabs and Overview cards. See [Finding your way around](/docs/dashboard#turning-parts-off).
- **Appearance**: the style, charts, density and your own CSS. See [Changing the look](/docs/dashboard#changing-the-look).
- **Activity**: what was changed and by whom. See [The activity log](/docs/security#the-activity-log).
- **Backup**: Nightjar's own setup in one file. See [Backups](/docs/settings#backups).

## Backups

**Settings → Backup** downloads Nightjar's own setup as one file: the checks, the settings made in the dashboard, alert rules and mutes, maintenance windows, the appearance, the status page design, other servers, plugin settings, and your incident feedback, notices and cleanup choices. History (check results, alerts and readings) is not in it.

**Without secrets** leaves out passwords, tokens and API keys; **With secrets** puts them in, so keep that file as safe as the server. **Restore one** reads a backup, says what is in it, and on **Restore** adds or replaces its checks (others are left as they are), replaces the rest, and restarts Nightjar to put it all in force. Restoring one made without secrets keeps the passwords and keys you have now. The whole file is checked before anything changes, so a backup that is refused changes nothing. The admin account and its sign-ins are never part of a backup.
