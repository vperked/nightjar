# Watching other servers

**[Servers](/servers)** shows your other machines next to this one, each one of two ways:

- **With the agent**, a small program (about 7 MB) installed on it. You see its CPU, memory, CPU temperature, fans, GPU, disks, drive health and temperatures, network and uptime, with alerts when it goes offline, runs hot, fills a disk or has a failing drive.
- **SSH only**, with nothing installed: just its address and an SSH login. Nightjar shows whether its SSH port answers, lists its checks, and can log in to test or reboot it. It has no readings and sends no alerts of its own; give it checks for that.

The dashboard does the asking. The servers never connect to it, so it can stay private on `127.0.0.1`.

> **Servers and services.** A server is a machine you see inside. A service is something that answers: a website, Plex, a port, a router's page. Something you only want to know is up, with nothing installed, is an [uptime check](/docs/uptime-checks), with retries, alerts and history. A check can say which server it runs on, and that server's page lists it.

## Add a server with the agent

1. Go to **[Servers](/servers)** and press **Add a server**.
2. Enter the server's address, such as `192.168.0.20`.
3. Pick its processor (**x86-64** for most PCs and servers, **ARM64** for a Raspberry Pi 4 or 5) and press **Download**. The agent comes from the dashboard itself, so it always matches this version.
4. Drag the downloaded file onto the server, into any folder: with a file app such as WinSCP, FileZilla or Cyberduck, into a folder the server shares, or on a USB stick.
5. In a terminal on the server, in that folder, run the install command the sheet shows:

   ```bash
   chmod +x nightjar-agent-linux-amd64 && sudo ./nightjar-agent-linux-amd64 install -token <token>
   ```

6. Press **Connect**. The dashboard checks that the agent answers with that token before saving it.

The install command sets the agent up as `nightjar-agent.service`, starting on boot, listening on port 9101. It is locked down so it can read the machine but change nothing outside its own folder, `/var/lib/nightjar-agent`. It also:

- **opens its port** in `ufw`, `firewalld` or plain `iptables` rules when one is turning traffic away, and keeps an `iptables` rule over reboots with `iptables-persistent` (`-firewall=false` to leave the firewall alone). A cloud server usually has a firewall in front of it too, in the provider's control panel (OVH, AWS, Hetzner and others); allow the port there as well.
- **adds `smartmontools`** for drive readings when it is missing, with the system's package manager (`-smart=false` to skip it).

### Updates

Over HTTPS, agents keep themselves on the dashboard's version. When the dashboard sees an agent on another build, it sends it its own, at most once an hour; the agent checks it is whole and a Linux program for its processor, swaps it in and restarts. The server's page shows **updated**, or why not, next to the agent's version. Turn it off with **Update agents** under **[Settings → Updates](/settings)** (`AGENT_AUTO_UPDATE`).

An agent installed before this version can't replace itself: its page says **reinstall to auto-update**. With an [SSH login](/docs/servers#ssh-just-in-case) for the server, press **reinstall now** there, or **Reinstall agent** under **SSH**. Or, on the server, run `sudo nightjar-agent install` with no token: it reinstalls with the token and settings already there. Either way it updates itself from then on.

> **The token** is made for that server, shown only in that sheet, and kept by the dashboard; it is never sent back to the browser. To change it, reinstall the agent with a new one and enter it under the server's **Connection**.

### HTTPS

Agents serve HTTPS by default. Their key comes from their token, so the dashboard, which has the same token, only talks to the real agent: a machine without the token can't pretend to be it, and there is no certificate to copy or renew.

On a network you trust you can turn **encrypt with HTTPS** off in the sheet, which adds `-tls=false` to the install command. The address then starts with `http://`, and readings and the token cross the network unencrypted.

To move such an agent to HTTPS later, use **Move to HTTPS…** in the server's settings. Its token has crossed the network in the clear, and the HTTPS key comes from the token, so the move needs a new one: the sheet makes it, fills in the `https://` address, and shows the `sudo nightjar-agent install -token … -tls=true` command to run on the server. Both the dashboard and the agent refuse HTTPS on the old token.

### Reaching the agent

Only this machine needs to reach the agent's port. Over a LAN that usually just works; across the internet, use a VPN such as Tailscale, or firewall the port to this machine's address. To use another port, give the address with it (`192.168.0.20:9200`) and the sheet adds `-listen :9200` to the install command.

## Add a server with SSH only

1. Press **Add a server** and choose **SSH only**.
2. Enter its address and a name.
3. Choose a key, the user and the port, and say whether the key is on the server already, as under [SSH, just in case](/docs/servers#ssh-just-in-case). **Add a key you have…** takes a private key the server already accepts.
4. Press **Add server** (or **Add key and server**, with a password). Nightjar checks that the SSH port answers before saving it, then logs in once and says whether the key worked.

Then add [uptime checks](/docs/uptime-checks) for what it runs, with this server picked under **Server**, so they show on its page and alert when they go down.

## Several servers at once

On **Servers**, press **Select**, then tick servers (or **Select all**). A bar at the bottom offers what can be done to all of them:

- **Test SSH**, **Restart agents** and **Reinstall agents**, over each server's [SSH login](/docs/servers#ssh-just-in-case).
- **Mute…** silences their alerts for 1, 4 or 24 hours, or unmutes them.
- **Reboot…** and **Remove…** ask first.

An action is done only to the servers it suits: one without an SSH login is left out of a test or a reboot, and the button counts the servers it will act on, such as **Reboot (2)**. Up to four servers are worked on at a time. Each server's answer is listed above the servers as it comes in: done, didn't work and why, or left out. **Done** or Esc stops choosing.

## A server's page

Open a server for its details: live readings, the last hour of charts (switch to 24 hours, 7 days or 30 days, from [Analytics](/docs/analytics)), cores, storage, drives, and its **Connection** settings for renaming it, changing its address or token, or removing it. **Mute** silences its alerts for a while.

**Services on it** lists the uptime checks that belong to the server, with their status. **Add a check** opens a new check with the server already picked; on any check, choose **Server** in its form to move it.

Servers added before this were sometimes watched with nothing installed. Those became uptime checks the first time this version started: same name, target and answer, with their history.

## SSH, just in case

When the agent stops answering or the server misbehaves, Nightjar can log in over SSH and act on it. Under **SSH** on the server's page, press **Set up SSH…**:

1. **Choose a key.** Nightjar lists the key pairs it finds in its own SSH folder (`SSH_DIR`, `ssh` beside the database unless you change it) and in `~/.ssh` of the user it runs as. Or:
   - **Add a key you have…**: paste a private key the server already accepts, such as your laptop's `~/.ssh/id_ed25519` (the file without `.pub`). It must have no passphrase, since Nightjar logs in with nobody there to type one. Nightjar keeps it in its SSH folder, readable only by itself, and never shows it again.
   - **Make a new key…**: an ed25519 key with no passphrase, made in that folder.
2. **Enter the user and port**, `root` and 22 unless you change them. **Host** is left empty to use the server's own address.
3. **Say whether the key is on the server already**:
   - **Yes**: it's in `~/.ssh/authorized_keys` for that user.
   - **Add it with a password**: type the user's password. Nightjar logs in once with it, adds the key to `~/.ssh/authorized_keys`, and keeps the password nowhere. A key you just made or added starts here. The first time, Nightjar first shows the server's host-key fingerprint: check it on the server with `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` and confirm it, and only then does the password go.
   - **I'll add it**: shows the key's public line to add to that file yourself, for a server that refuses password logins.
4. **Save and test** (**Add key, save and test** with a password). It's one step: Nightjar adds the key if asked, saves the login, and logs in with it at once. If the password is turned down, nothing is saved.

Once it's set up, the section shows whether the login works: **Logs in as root@nas** and when it last worked, or **Can't log in** with why and when it was tried. It's updated whenever Nightjar uses the login, and kept across restarts. Under it is the server's **host key**, the fingerprint Nightjar trusts for it. Then:

- **Test login** (**Test again** once it has been tried): logs in and updates the status.
- **Restart agent** (servers with the agent): `systemctl restart nightjar-agent`.
- **Reinstall agent** (servers with the agent): sends this dashboard's build of the agent over SSH and installs it with the token and settings already on the server, so nothing secret goes on a command line. It works while the agent is down, too.
- **Reboot…**: asks first, then `systemctl reboot`.
- **Log in yourself**: the `ssh` command that logs in the same way, to copy into a terminal.
- **Change login…** and **Forget…**. A changed login shows **Not tried yet** until Nightjar uses it.

Restarting, reinstalling and rebooting need root, or a user that may run them with `sudo` and no password. Nightjar runs only these actions, never a command typed into the page. Restarts, reinstalls, reboots and changes to a login go in the [activity log](/docs/security#the-activity-log).

The private key stays a file on this machine: Nightjar saves only its path and never reads it or shows it. The first login trusts the server's host key and remembers it in `known_hosts` in Nightjar's SSH folder; a key you confirmed before sending a password is also kept in `known_hosts.confirmed` there. A key that only a key login saved does not count as confirmed, so a password is never sent to it. If the server is reinstalled and its key changes, logins are refused until you remove its lines from `known_hosts` and `known_hosts.confirmed`. A [backup](/docs/settings) keeps each server's login but not the key files, so copy the SSH folder too if you move Nightjar to another machine.

## Alerts

All of these go through your [notification rules](/docs/notifications) under **Other servers**, which go to Discord by default:

- **Offline** after three missed checks (30 seconds), and **back online** when it answers again.
- CPU temperature over `TEMP_THRESHOLD` for two readings in a row, memory over `MEMORY_THRESHOLD` for three, a disk over `DISK_THRESHOLD`, and a drive whose health turns to warning or failing.

Each alert is sent once and clears with a recovery message once the reading is back a little under the limit, so it doesn't flap. A restart of the monitor doesn't send them again.

## Removing a server

**Remove** under its **Connection** stops watching it and deletes its history. Checks that belonged to it stay, belonging to none. The agent keeps running until you uninstall it on the server:

```bash
sudo nightjar-agent uninstall
```

That also closes the port it opened in the firewall.

## When it won't connect

The sheet says why:

- **Connection refused**: the agent isn't running, or is on another port. On the server, `systemctl status nightjar-agent`.
- **Timed out**: a firewall is blocking the port. Check the server's own (`sudo ufw status`), and on a cloud server the provider's firewall in its control panel.
- **The agent turned the token down**: the token doesn't match the one it was installed with.
- **Serves plain HTTP / serves HTTPS**: change the address to start with `http://` or `https://` as it says.
- **Its certificate does not belong to an agent with this token**: something other than the agent answered on that port.

For SSH, **Test login** says why it didn't work:

- **The server refused the key**: the key's public line isn't in `~/.ssh/authorized_keys` for that user.
- **The server's host key is not the one Nightjar saw before**: the server was reinstalled, or something else answers on its address. If you expected it, remove the server's lines from `known_hosts` and `known_hosts.confirmed` in Nightjar's SSH folder, then confirm the new key when you next add a key with a password.
- **That user needs a password for sudo**: log in as root, or allow the user to run `systemctl` with `sudo` and no password.
- **Nothing answered SSH on that port** or **did not answer in time**: SSH isn't running there, or a firewall blocks the port.
