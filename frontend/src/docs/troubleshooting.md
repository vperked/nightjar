# Troubleshooting

## Start with the self-test

```bash
./bin/nightjar -test
```

It checks the collectors, the uptime checks, every alert channel (sending each a test), and the Plex and qBittorrent connections, and says what failed.

## No alerts arrive

- Run **Send test alert** under [Settings → Alert channels](/settings), which says which channels failed and why.
- Check [Notifications](/notifications): every alert is there with where it was sent, or why it wasn't. _Held_ means quiet hours, _muted_ a mute, _raised during maintenance_ a [maintenance window](/docs/incidents#planned-maintenance) over everything, and _kept_ that no rule sends it anywhere.
- Check the [rules](/notifications/rules) for that area.
- Alerts that Discord refused are also written to `ALERT_LOG_PATH`.

## Can't sign in

- **First time:** the setup code is in the log: `journalctl -u nightjar | grep setup_code | tail -1`.
- **Forgot the password:** see [Account and security](/docs/security#the-admin-account).
- **"Too many tries":** wait a minute.

## Can't reach the dashboard

It listens only on this machine by default. See [Reaching the dashboard from elsewhere](/docs/security#reaching-the-dashboard-from-elsewhere).

## Missing readings

- **No temperatures:** the machine must expose sensors in `/sys/class/hwmon`. On some, `apt install lm-sensors` then `sensors-detect` helps; some virtual machines have none.
- **No drive health:** install `smartmontools`.
- **No GPU:** only NVIDIA GPUs are read, with `nvidia-smi`.
- **Kernel checks find nothing:** the monitor needs to read `journalctl`, `dmesg` or `/var/log/kern.log`, which running as root allows.

## Another server won't connect

See [When it won't connect](/docs/servers#when-it-won-t-connect).

## Disk cleanup does nothing

It only cleans below the thresholds in [Resources, disks and the kernel](/docs/resources#disk-cleanup), and only the paths it is allowed. The [monitor log](/logs) says what it removed, or why it couldn't.

## Alerts arrive twice

Both the Discord bot and the Discord webhook are set. Clear `WEBHOOK` once the bot works; the bot sends everything the webhook did, with buttons.

## Discord buttons do nothing

- **Discord will not save the Interactions Endpoint URL:** it tries the address first. The dashboard must be reachable from the internet at that address, and `DISCORD_PUBLIC_KEY` must be the key of the same application. Save the key in Nightjar before you set the URL in Discord.
- **"This interaction failed":** Discord reached nobody. Check the reverse proxy passes `/discord/interactions` through.
- **"You are not allowed to act on Nightjar's alerts":** add your Discord user id to `DISCORD_ALLOWED_USERS`. Turn on Developer Mode in Discord, then right-click your name and **Copy User ID**.

## Lost the authenticator app

Sign in with one of the recovery codes, in place of the six-digit code, then turn two-factor off and on again under **Settings → Account** for a new key and new codes. With neither the app nor a code, reset the account from the machine: see [Account and security](/docs/security#the-admin-account).

## A heartbeat says a job is late

- The schedule is read in the server's own time zone. `timedatectl` shows which that is.
- The job must reach the ping URL. Run its `curl` line by hand on the machine the job runs on, and look for the ping on the check's page.
- A job that takes a while to finish needs more **grace**, or a `/start` ping and **May run for**.

## A repair or plugin script is not run

Nightjar only runs scripts that belong to root (or to its own user) and that no one else can change. The check's form says what is wrong with each script in the repairs folder and how to fix it, usually `sudo chown root:root` and `sudo chmod go-w` on the script and its folder.

## A Docker check says Docker is not reachable

Nightjar talks to Docker at `DOCKER_SOCKET` (`/var/run/docker.sock`). Check Docker runs (`systemctl status docker`), and if Docker uses another socket, as rootless Docker does, set `DOCKER_SOCKET` to it.

## Media cleanup lists nothing

- A title is only listed `MEDIA_CLEANUP_DAYS` (60) days after Plex added it, and only if no account has ever played any of it.
- When Plex's watch history cannot be read, nothing is listed, to be safe. The Cleanup page says so.
- `MEDIA_CLEANUP_LIBRARIES` may leave out the library you expect.
- Until `MEDIA_CLEANUP` is on, the page only shows what it would list, and nothing is removed.

## A restore was refused

Restore checks the whole file before it changes anything, so a refused restore changed nothing. It says why: a file that is not a Nightjar backup, or a check in it that is not valid any more. A backup made without secrets leaves the passwords and keys you have now as they are.

## Still stuck

The [monitor log](/logs) at `DEBUG` (`LOG_LEVEL` under Settings, applied at once) shows everything the monitor does.
