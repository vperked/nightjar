# Public status page

A status page shows chosen uptime checks to anyone, without signing in. Nothing else about the machine is shown, and never a check's target, error details or response times.

## What visitors see

- **A headline** saying whether everything is up, some services are having trouble, or everything is down, with the average uptime of the services listed and when the page last updated. When only some are down, it names them (_API is down. Everything else is running normally._), and it names services under planned maintenance the same way. It refreshes itself every 30 seconds, and the browser tab says so too when something is wrong.
- **Ongoing incidents and planned maintenance**, right under the headline: each [incident notice](/docs/incidents) you published that hasn't ended yet, then each maintenance window you chose to show, marked _Maintenance now_ while it is on.
- **Each group** of services, with how many are working: amber when some are down, red when all are.
- **Each service's last 90 days**, one bar a day: green with no downtime, amber with some, red for a major outage, grey before there was any data. Hovering a day (or moving along with the arrow keys) shows its uptime and about how long it was down. A phone shows the last 30 days.
- **Its uptime** over those 90 days, beside its state; on a phone, over the 30 days its bars show.
- **Recent incidents** at the foot: notices for incidents that have ended, marked _Resolved_ with when they started and ended.

If the page can't get its data, it says so and tries again every 30 seconds.

## Turn it on

Set `STATUS_PAGE_ENABLED` under **[Settings → Dashboard](/settings)**, or use the **Public** switch in the designer. The page is then at `/status` on this dashboard's address (or the path you choose, below), and the account menu links to it.

## Design it

**[The status page designer](/status-page)** edits it on the left and previews it live on the right, with the checks' real state:

- **Title**, **intro** and **footer** text.
- **Groups** of checks, each with an optional heading, in any order, and each check under the name you choose. Only checks you add here are ever shown.
- Whether to show **uptime percentages** and the **90-day history**.
- An **announcement** at the top, such as _Plex is down for maintenance until 3 am_, styled as **Information**, **Warning** or **Incident**.

Changes go live when you save.

## Your own CSS and HTML

**Your own code**, at the bottom of the designer, takes three things:

- **CSS** styles the whole public page. Rules for `body` and `:root` work too. In the designer, your CSS only styles the preview, so the dashboard around it never changes. **Classes you can style** lists the page's parts, such as `.status-hero`, `.status-notice`, `.status-row` and `.day`.
- **Top HTML** is shown above the title. Use it for a logo or links to your other sites.
- **Bottom HTML** is shown above the footer. Use it for contact details.

The page takes scripts, frames, forms and event handlers like `onclick` out of your HTML. Links may only go to web, mail or phone addresses. Images, fonts and stylesheets may come from other HTTPS sites, so an `@import` of a web font works.

The CSS can be up to 50,000 characters, and each HTML box up to 20,000.

## Its address

**Address**, at the top of the designer, says where visitors find the page:

- **Path**: where it is on the dashboard's own address, such as `/uptime` (`STATUS_PAGE_PATH`). `/status` always works too. The path is one word, and can't be one of the dashboard's own pages.
- **Its own domain**, such as `status.example.com` (`STATUS_PAGE_DOMAIN`). There the status page is the whole site, at `/`: visitors get the page, its styles and its data, and nothing of the dashboard. Its sign-in, settings and API can't be reached through that domain, even with a token.

### Putting the domain online

First point the domain's DNS at this machine (an A record to its public address, and AAAA for IPv6). Then, in the designer:

- **Set up nginx and HTTPS** (when nginx is installed): Nightjar adds a site for the domain, checks nginx accepts it, reloads nginx, and asks certbot for a Let's Encrypt certificate, which certbot then renews on its own. Each step is listed as it goes. If nginx refuses the configuration, the site is taken out again, so nginx keeps running as it was. If certbot can't get a certificate yet (the DNS hasn't spread, or port 80 is closed), the page works over HTTP meanwhile; run it again later. An email address for certificate notices is optional.
- **Take it out of nginx** removes that site again. The certificate stays with certbot (`certbot delete --cert-name status.example.com` removes it).
- **Set it up yourself** shows the nginx server block to use instead, for another reverse proxy or a hand-kept nginx. Whatever the proxy, it must pass the host it was asked for (nginx: `proxy_set_header Host $host`), which is how Nightjar knows to show only the status page.

Nightjar never overwrites or removes a site it didn't write, and won't add one for a domain another site already serves. Setting up nginx needs Nightjar to run as root, as the installed service does.

Behind Cloudflare's proxy, certbot's check over port 80 still works, unless **Always Use HTTPS** is on for the domain. Turn it off while the certificate is issued, or use Cloudflare's own certificate with SSL set to Full.

## Sharing it

The dashboard listens only on this machine by default. To share only the status page, give it its own domain (above) and expose just that. To share the dashboard as well, put it behind a reverse proxy with HTTPS (see [Account and security](/docs/security)); visitors who aren't signed in can open the status page and nothing else.
