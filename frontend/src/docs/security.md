# Account and security

## The admin account

The dashboard has one account, created the first time you open it with the setup code from the log (see [Getting started](/docs/getting-started#create-the-admin-account)). Passwords are at least 10 characters and stored hashed (PBKDF2-SHA256).

A browser stays signed in for 30 days of use, and at most 90 days in all, with a cookie that page scripts can't read. Ten wrong passwords in a minute lock that address out for a minute. Twenty wrong tries at one account, from any number of addresses, hold its sign-ins back for half a minute, then longer after each further wrong try (up to 15 minutes); a browser that has signed in to that account before is not held back.

**Change the password** under **[Settings → Account](/settings)**. That signs out every other browser, and so does turning two-factor sign-in on or off.

**Forgot it?** Stop the monitor, delete the account, and start it again for a new setup code:

```bash
sudo systemctl stop nightjar
./bin/nightjar -reset-admin
sudo systemctl start nightjar
journalctl -u nightjar | grep setup_code | tail -1
```

## Two-factor sign-in

Under **Settings → Account**, **Set it up** shows a key to add to an authenticator app (1Password, Google Authenticator, Aegis and the like), or a link that opens it in the app on the same device. Type the app's current code and your password to turn it on. Signing in then asks for a code after the password; each code works once.

Ten recovery codes are shown once, when you turn it on: each signs you in once without the app. Keep them somewhere safe. If both the phone and the codes are lost, run `nightjar -reset-admin` on the server and create the account again.

## Viewers

A **viewer** is a read-only account for someone who should see the dashboard but not change it: a family member who wants to know whether Plex is up, or a friend who helps keep an eye on things. Add one under **[Settings → Viewers](/settings#viewers)** with a username and a password, then pass those on.

A viewer signs in on the same page and sees what you see, with a _Read-only_ note at the top, but:

- every change is refused: checks, settings, firewall rules, repairs, updates, restarts, stopping streams, marking notifications read;
- settings, backups, SSH keys, plugins, the setup guide, the status page designer and services' logs (which can hold passwords) are not shown at all;
- a viewer has no two-factor sign-in of its own, so give it a strong password.

A viewer changes its own password under **Your password** in the account menu; that signs out its other browsers. **New password** gives a viewer a fresh password and signs it out everywhere; **Remove** deletes it and signs it out. Changing your own password signs out your other browsers but leaves viewers signed in. What you change while a viewer watches shows up in the [activity log](/docs/security#the-activity-log) under your name.

## The activity log

**Settings → Activity** lists what was done and by whom for a year: every change made through the dashboard or the API (by the admin's name, or "API token"), each sign-in, each Discord button pressed (by Discord user), and what Nightjar did on its own: repairs and media removed by the cleanup. Trying things out (testing a check, a plugin or settings) is left out.

## Scripts and the API

Scripts can use the [API](/docs/api) without signing in. Set `WEB_AUTH_TOKEN` in `.env` and send it as `Authorization: Bearer <token>`. It must be at least 16 characters (`openssl rand -hex 24` makes a good one); a shorter token is ignored, and the log says so at startup.

## Reaching the dashboard from elsewhere

By default the dashboard listens on `127.0.0.1:8080`, only on this machine. You have three ways to reach it from elsewhere:

- **An SSH tunnel**, nothing to change: `ssh -L 8080:127.0.0.1:8080 you@server`, then open `http://localhost:8080`.
- **A VPN** such as Tailscale, with `WEB_HOST` set to the VPN address.
- **A reverse proxy** such as nginx in front of it, with HTTPS. Keep `WEB_HOST=127.0.0.1` and proxy to it:

```nginx
location / {
    proxy_pass http://127.0.0.1:8080;
    proxy_set_header Host $host;
    proxy_set_header X-Real-IP $remote_addr;
    proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    proxy_set_header X-Forwarded-Proto $scheme;
}
```

Behind a proxy on the same machine, the monitor takes the visitor's address from `X-Real-IP` or the last `X-Forwarded-For` hop, whichever the proxy sets (so sign-in limits apply per visitor), and marks its cookie secure when `X-Forwarded-Proto` says HTTPS.

While it listens only on this machine, the dashboard answers a host name on its own port only if it is `localhost` or the name in `DASHBOARD_URL` or `STATUS_PAGE_DOMAIN`, so a website can't point its own name at `127.0.0.1` to reach it through your browser; open it by IP address or `localhost`, or set `DASHBOARD_URL`, if it says it does not know the address.

Setting `WEB_HOST=0.0.0.0` listens on every address. Everyone must still sign in, but the traffic is plain HTTP, so prefer one of the ways above.

## What the monitor can do

It runs as root, because cleaning disks, restarting services, reading the kernel log and SMART need it. Changes made from the dashboard (settings, update commands, services, app installs) need the signed-in admin, and only from the dashboard's own pages, so another website can't make them through your browser.
