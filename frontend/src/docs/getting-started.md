# Getting started

Nightjar watches the machine it runs on (CPU, memory, temperatures, disks, drives, network, services) and, if you like, other servers too. It alerts you when something goes wrong and keeps history so you can see trends. Everything is one program with this dashboard built in.

## Install it

You need Go 1.26 or later and Node 20.19 or later to build it.

```bash
cp env.recommended .env
make
./bin/nightjar
```

`make` builds the dashboard and the monitor into one file, `bin/nightjar`, which is all you need to deploy. The dashboard then answers on `http://127.0.0.1:8080`.

To keep it running and start it on boot, install it as a systemd service:

```bash
make install-service
```

That creates `nightjar.service`. Check on it with `systemctl status nightjar`.

## Create the admin account

The dashboard shows nothing until it has an admin account. The first time you open it, it asks for a username, a password of at least 10 characters, and a **setup code**. The setup code is printed in the monitor's log when it starts, so only someone who can read that log can claim a fresh install:

```bash
journalctl -u nightjar | grep setup_code | tail -1
```

Not running under systemd? Look in the log file (`nightjar.log` by default) or the terminal it runs in.

After that you sign in with that account. A browser stays signed in for 30 days of use. See [Account and security](/docs/security) for changing the password or resetting a forgotten one.

## Run the setup guide

The [setup guide](/setup) finds what is running on the machine and offers to watch it, one step at a time:

- **Alerts**: a Discord webhook, tested before it is saved. Other channels can be added later under [Alert channels](/docs/alerts).
- **Plex** and **qBittorrent**: found and connected for you, tokens included.
- **Uptime checks**: suggested for the apps that answer on this machine.
- **Updates**: checks switched on.

The Overview links to the guide until alerts and checks are set up. You can skip any step and come back.

## Where to go next

- [Finding your way around](/docs/dashboard): what each tab and menu does.
- [Uptime checks](/docs/uptime-checks): watch websites, apps and cron jobs, and repair them when they go down.
- [Incidents](/docs/incidents): what went wrong, and why.
- [Watching other servers](/docs/servers): add your other machines.
- [Notifications](/docs/notifications): decide which alerts reach you, and when.
