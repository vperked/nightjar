# Finding your way around

## The tabs

- **[Overview](/)**: this machine at a glance. CPU, memory, disk and network with the last half hour, anything that needs attention, uptime checks, processes, the host's details, cores, storage, drive health and network interfaces.
- **[Services](/services)**: your [uptime checks](/docs/uptime-checks), and next to them the machine's [systemd services](/docs/system-services).
- **[Incidents](/incidents)**: each stretch of trouble with its [likely cause](/docs/incidents), [planned maintenance](/docs/incidents#planned-maintenance) and the [weekly report](/docs/incidents#the-weekly-report).
- **[Servers](/servers)**: [other machines](/docs/servers) you watch, and [Analytics](/docs/analytics) across all of them.
- **[Network](/network)**: a live meter of what each network interface is sending and receiving, and the **[Firewall](/network/firewall)** of this machine and each server: its [rules, default and what it dropped](/docs/firewall).
- **Media**: [Plex](/docs/plex), its [cleanup](/docs/plex#cleanup) and [qBittorrent](/docs/qbittorrent), when they are set up.
- **[Apps](/apps)**: the [app store](/docs/app-store) and [updates](/docs/updates).

A red dot on the logo means something is down. The browser tab shows it too.

## The header

- **Find** (or press <kbd>Ctrl</kbd> <kbd>K</kbd>, or <kbd>/</kbd>): jump to any page, check, system service, setting or help page by typing part of its name. It also runs actions such as _Send a test alert_ or _Add an uptime check_.
- **The bell**: unread warnings and criticals, with the latest few. Open it for the full [notifications](/docs/notifications) history.
- **The gear**: [Settings](/settings).
- **Your initial**: the account menu, with the theme (the dashboard opens at night; the day theme is a click away and remembered), the [monitor log](/docs/logs), these docs, the public status page when it is on, and signing out.

## Turning parts off

Don't use some of it? **[Settings → Sections](/settings#sections)** hides tabs (Servers, Network, Media, Apps), the bell, and any card on the Overview. Monitoring and alerts carry on for anything hidden; it only leaves the screen. Opening a hidden page offers to turn it back on.

## Changing the look

**[Settings → Appearance](/settings#appearance)** sets how the dashboard looks in every browser you sign in from. The day or night theme stays a choice of each browser, in the account menu.

- **Style**: _Ruled_ puts each panel on a soft, flat rounded panel with numbered titles, and sets readings in a monospaced type. _Cards_ raises every panel with a shadow, in a sans type.
- **Charts and meters**: smooth lines and solid bars (the default), or _Dots_, which draws them on a dot matrix. This covers the charts, the check history, the disk gauge and the usage bars.
- **Density**: _Compact_ tightens the spacing and type to fit more on a screen.
- **Your own CSS**: applied over the style on every page. The variables and classes it can use are listed under the box. To style one look only, start a rule with `:root[data-style="cards"]`; `data-charts`, `data-density` and `data-theme` work the same way. Images and fonts from other sites are blocked on the dashboard. The public status page has its own CSS, in [its designer](/docs/status-page).

A change shows at once across the dashboard as a preview. **Save appearance** keeps it; **Discard**, or leaving Settings without saving, puts the saved look back.

## On a phone

The dashboard works at phone width: the tabs move to the bottom of the screen, and cards stack.
