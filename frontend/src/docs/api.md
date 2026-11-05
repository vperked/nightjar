# API

Everything the dashboard shows comes from a JSON API on the same address. Scripts authenticate with `WEB_AUTH_TOKEN` (see [Account and security](/docs/security#scripts-and-the-api)):

```bash
curl -H "Authorization: Bearer $WEB_AUTH_TOKEN" http://127.0.0.1:8080/api/stats
```

Changes (`POST`, `PUT`, `DELETE`) take JSON bodies:

```bash
curl -X POST -H "Authorization: Bearer $WEB_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"subject": "check:Plex", "label": "Plex", "minutes": 60}' \
  http://127.0.0.1:8080/api/notifications/mute
```

A request that fails answers with a 4xx or 5xx status and `{"error": "what went wrong"}`, in the same words the dashboard shows. Every change made with the token is kept in the [activity log](/docs/security#the-activity-log) as "API token".

## Public

| Route                                                                                                   | What                                                                                                                             |
| ------------------------------------------------------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------- |
| `GET /health`                                                                                           | 200 when healthy, 503 degraded; details only when signed in                                                                      |
| `GET /api/config`                                                                                       | whether the account exists and you are signed in, which pages are on, and the look (`appearance`; its `css` only once signed in) |
| `GET /api/status`                                                                                       | the public status page's checks, maintenance and notices, when it is on                                                          |
| `GET` or `POST /ping/{token}`, `/ping/{token}/start`, `/ping/{token}/fail`, `/ping/{token}/{exit code}` | a heartbeat's job reporting in; the token is the credential                                                                      |
| `POST /discord/interactions`                                                                            | Discord's button presses, checked against `DISCORD_PUBLIC_KEY`; see [Discord buttons](/docs/alerts#discord-buttons)              |

## Signing in

| Route                                                   | What                                                                                                                     |
| ------------------------------------------------------- | ------------------------------------------------------------------------------------------------------------------------ |
| `POST /api/auth/setup`                                  | create the account: `{"username", "password", "setup_code"}`                                                             |
| `POST /api/auth/login`, `POST /api/auth/logout`         | `{"username", "password"}`, and `"code"` with two-factor sign-in on                                                      |
| `POST /api/auth/password`                               | `{"current", "new"}`; signs out every other browser                                                                      |
| `POST /api/auth/2fa/begin`, `POST /api/auth/2fa/enable` | a new key (`secret` and an `otpauth` `link`), then `{"password", "code"}` to turn it on; returns the recovery codes once |
| `POST /api/auth/2fa/disable`                            | `{"password", "code"}`                                                                                                   |

## This machine

| Route                                                                                                                                                                               | What                                                                                                                                                                                                                                     |
| ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /api/stats`                                                                                                                                                                    | CPU, memory, disks, network, processes and the last half hour                                                                                                                                                                            |
| `GET /api/network`                                                                                                                                                                  | the live network meter                                                                                                                                                                                                                   |
| `GET /api/attacks`                                                                                                                                                                  | the attack watch: calm or under attack, the methods and their strength, the addresses most involved, past attacks                                                                                                                        |
| `PUT /api/attacks/defense`                                                                                                                                                          | switch defending on or off: `{"on": false}`                                                                                                                                                                                              |
| `DELETE /api/attacks/blocked/{address}`                                                                                                                                             | let an address the defense blocked back in, and leave it alone for an hour; IPv6 is blocked per /64, such as `2001:db8::/64`, URL-encoded here                                                                                           |
| `GET /api/firewall`                                                                                                                                                                 | this machine's firewall: which it runs, its default, the rules in order with what each caught, open ports, the latest drops and its own rules                                                                                            |
| `POST /api/firewall/rules`, `PUT` or `DELETE /api/firewall/rules/{id}`                                                                                                              | add a rule (`{"action": "block", "source": "203.0.113.7", "port": "22", "proto": "tcp", "note": "..."}`; `"direction": "out"` for outgoing; `"action": "limit"` with `"rate": "6/min"`), change it (also `"paused": true`), or remove it |
| `PUT /api/firewall/order`                                                                                                                                                           | put the rules in order: `{"ids": [...]}`, every rule once                                                                                                                                                                                |
| `PUT /api/firewall/default`, `POST /api/firewall/default/keep`                                                                                                                      | drop what no rule allows (`{"default": "drop"}`) or leave it to the machine's firewall (`{"default": ""}`); it goes back after a minute unless kept with `{"keep": true}`                                                                |
| `GET /api/hosts/{id}/firewall`                                                                                                                                                      | a server's firewall and defense, through its agent                                                                                                                                                                                       |
| `POST`, `PUT`, `DELETE /api/hosts/{id}/firewall/rules…`, `PUT /api/hosts/{id}/firewall/order`, `PUT /api/hosts/{id}/firewall/default`, `POST /api/hosts/{id}/firewall/default/keep` | the same on a server                                                                                                                                                                                                                     |
| `PUT /api/hosts/{id}/defense`, `DELETE /api/hosts/{id}/blocked/{address}`                                                                                                           | switch a server's defense, or let an address back in there                                                                                                                                                                               |
| `GET /api/drives`                                                                                                                                                                   | SMART readings for each drive                                                                                                                                                                                                            |
| `GET /api/units`, `GET /api/units/{name}/logs`                                                                                                                                      | systemd services, and one's log                                                                                                                                                                                                          |
| `POST /api/units/{name}/{start,stop,restart}`                                                                                                                                       | control a service; restarting Nightjar's own answers `202 {"restarting": true}` first, and stopping it is refused                                                                                                                        |
| `GET /api/logs?after=&level=&component=&q=`                                                                                                                                         | the monitor's own log                                                                                                                                                                                                                    |

## Uptime checks

| Route                                                                                        | What                                                                                       |
| -------------------------------------------------------------------------------------------- | ------------------------------------------------------------------------------------------ |
| `GET /api/services`                                                                          | every check's status, uptime and recent results                                            |
| `GET /api/services/history?name=&hours=`                                                     | one check's stored results                                                                 |
| `GET /api/checks`, `POST /api/checks`, `PUT /api/checks/{name}`, `DELETE /api/checks/{name}` | list, add, change and delete checks; `"server"` is the id of the watched server it runs on |
| `POST /api/checks/test`                                                                      | run a check once without saving it, with the same body                                     |
| `GET /api/repairs?check=name`                                                                | repair attempts, and the scripts in the repairs folder                                     |
| `POST /api/checks/{name}/repair`                                                             | run a check's repair once, now                                                             |
| `GET /api/docker/containers`                                                                 | Docker's containers, for a Docker check or a repair                                        |

## Incidents and maintenance

| Route                                                                 | What                                                                     |
| --------------------------------------------------------------------- | ------------------------------------------------------------------------ |
| `GET /api/incidents?days=14`                                          | incidents, newest first, with their timeline, likely cause and readings  |
| `PUT /api/incidents/{id}/feedback`, `DELETE`                          | `{"verdict": "wrong", "actual": "disk", "note": "..."}`                  |
| `PUT /api/incidents/{id}/notice`, `DELETE`                            | `{"title": "...", "text": "...", "published": true}` for the status page |
| `GET /api/maintenance`, `POST`, `PUT /api/maintenance/{id}`, `DELETE` | planned maintenance windows                                              |
| `GET /api/report`, `POST /api/report/send`                            | the weekly report, or send it now                                        |

## Other servers and analytics

| Route                                                              | What                                                                                                                                                                                                                                                                                    |
| ------------------------------------------------------------------ | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `GET /api/hosts`, `GET /api/hosts/{id}`                            | watched servers with their readings                                                                                                                                                                                                                                                     |
| `POST /api/hosts`, `PUT /api/hosts/{id}`, `DELETE /api/hosts/{id}` | add, change and remove: `{"name", "address", "token"}`; removing one leaves its checks, belonging to none                                                                                                                                                                               |
| `PUT /api/hosts/{id}/ssh`, `DELETE`                                | a server's SSH login: `{"key": "/path/to/private/key", "user": "root", "port": 22, "host": ""}`; the key must be one `GET /api/ssh/keys` lists; tried at once, and the server comes back with `ssh_check` (`ok`, `at`, `as`, `error`, `host_key`)                                       |
| `POST /api/hosts/{id}/ssh/{action}`                                | log in and run `test`, `restart-agent`, `reinstall-agent` or `reboot`; returns `{"output": "..."}` and updates the server's `ssh_check`                                                                                                                                                 |
| `GET /api/ssh/keys`, `POST /api/ssh/keys`                          | the key pairs Nightjar can use (paths and public halves only), or make one: `{"name": "nightjar"}`                                                                                                                                                                                      |
| `POST /api/ssh/authorize`                                          | put a login's key on its server by logging in once with a password, kept nowhere: `{"id": "<server, optional>", "login": {...}, "password"}`. The password only goes to a server whose host key was confirmed: until then it answers 422 with the key the server presents in `host_key` |
| `POST /api/ssh/trust`                                              | confirm that host key once it is checked on the server (`ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`): `{"id", "login", "host_key": "ED25519 SHA256:…"}`                                                                                                                          |
| `POST /api/hosts/token`                                            | a fresh agent token                                                                                                                                                                                                                                                                     |
| `GET /api/agent/download?arch=amd64`                               | the agent itself (`amd64` or `arm64`)                                                                                                                                                                                                                                                   |
| `GET /api/analytics?range=24h`                                     | every server over `1h`, `24h`, `7d` or `30d`                                                                                                                                                                                                                                            |
| `GET /api/analytics/{id}?range=`                                   | one server (`local` for this one)                                                                                                                                                                                                                                                       |

## Alerts and notifications

| Route                                                          | What                                                                   |
| -------------------------------------------------------------- | ---------------------------------------------------------------------- |
| `POST /api/alerts/test`                                        | send a test alert to every channel                                     |
| `GET /api/notifications?q=&severity=&area=&unread=1&before=`   | the alert history                                                      |
| `GET /api/notifications/summary`                               | what the bell shows: unread counts, mutes and quiet hours              |
| `POST /api/notifications/read`                                 | `{"ids": [...]}` or `{"all": true}`; add `"unread": true` to undo      |
| `GET /api/notifications/rules`, `PUT /api/notifications/rules` | routing rules and quiet hours                                          |
| `POST /api/notifications/mute`                                 | `{"subject": "check:Plex", "label": "Plex", "minutes": 60}`; 0 unmutes |

## Media

| Route                                                     | What                                                                                                                       |
| --------------------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| `GET /api/plex`                                           | the server, streams and libraries                                                                                          |
| `GET /api/plex/stats?days=7`                              | the look back, over 1 to 90 days                                                                                           |
| `POST /api/plex/streams/{id}/stop`                        | stop a stream: `{"message": "..."}`, shown on the player                                                                   |
| `GET /api/cleanup`, `POST /api/cleanup/scan`              | what the media cleanup lists, and look again now                                                                           |
| `PUT /api/cleanup/{key}/keep`, `DELETE`                   | keep a title for good, or let it be listed again                                                                           |
| `GET /api/qbittorrent`, `GET /api/qbittorrent/categories` | torrents, and the categories to add them under                                                                             |
| `POST /api/qbittorrent/action`                            | `{"action": "start", "hashes": [...]}`: `start`, `stop`, `reannounce`, `recheck` or `delete` (with `"delete_files": true`) |
| `POST /api/qbittorrent/add`                               | `{"urls": "magnet links, one per line", "category": "", "stopped": false}`                                                 |

## Settings and administration

| Route                                                                          | What                                                                               |
| ------------------------------------------------------------------------------ | ---------------------------------------------------------------------------------- |
| `GET /api/settings`, `PUT /api/settings`                                       | every setting; save with `{"values": {"KEY": "value"}}`                            |
| `POST /api/settings/test`                                                      | try a connection with unsaved values: `{"target": "plex", "values": {...}}`        |
| `GET /api/appearance`, `PUT /api/appearance`                                   | the look: `{"style": "matrix", "charts": "", "density": "comfortable", "css": ""}` |
| `GET /api/statuspage`, `PUT /api/statuspage`                                   | the status page's design                                                           |
| `GET /api/activity?limit=100&before=`                                          | the activity log, newest first, up to 500 at a time                                |
| `GET /api/backup?secrets=1`                                                    | a backup; leave out `secrets` for one without passwords and keys                   |
| `POST /api/backup/restore`                                                     | the backup file as the body; restarts Nightjar                                     |
| `GET /api/setup`                                                               | what the setup guide has found and done                                            |
| `GET /api/updates`, `POST /api/updates/check`, `POST /api/updates/{id}/update` | updates                                                                            |
| `POST /api/updates`, `PUT /api/updates/{id}`, `DELETE /api/updates/{id}`       | your own update targets                                                            |
| `GET /api/apps`, `POST /api/apps/{id}/install`, `POST /api/apps/{id}/remove`   | the app store                                                                      |
| `GET /api/plugins`, `POST /api/plugins/rescan`                                 | the plugins found, or look in the folder again                                     |
| `PUT /api/plugins/{id}`                                                        | `{"enabled": true, "settings": {...}, "clear": [...]}`                             |
| `POST /api/plugins/{id}/try`                                                   | run once: `{"mode": "check", "argument": "..."}` or card                           |
| `GET /api/plugins/cards`                                                       | every turned-on plugin's card                                                      |
