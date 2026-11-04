# Public status page

A status page shows chosen uptime checks to anyone, without signing in. Nothing else about the machine is shown, and never a check's target, error details or response times.

## What visitors see

- **A headline** saying whether everything is up, some services are having trouble, or everything is down, with when the page last updated. It refreshes itself every 30 seconds, and the browser tab says so too when something is wrong.
- **Each group** of services, with how many are working.
- **Each service's last 90 days**, one bar a day: green with no downtime, amber with some, red for a major outage, grey before there was any data. Hovering a day (or moving along with the arrow keys) shows its uptime and about how long it was down. A phone shows the last 30 days.
- **Its uptime** over those 90 days.

## Turn it on

Set `STATUS_PAGE_ENABLED` under **[Settings → Dashboard](/settings)**. The page is then at `/status` on this dashboard's address, and the account menu links to it.

## Design it

**[The status page designer](/status-page)** edits it on the left and previews it live on the right, with the checks' real state:

- **Title**, **intro** and **footer** text.
- **Groups** of checks, each with an optional heading, in any order, and each check under the name you choose. Only checks you add here are ever shown.
- Whether to show **uptime percentages** and the **90-day history**.
- An **announcement** at the top, such as _Plex is down for maintenance until 3 am_, styled as **Information**, **Warning** or **Incident**.

Changes go live when you save.

## Your own CSS and HTML

**Your own code**, at the bottom of the designer, takes three things:

- **CSS** styles the whole public page. Rules for `body` and `:root` work too. In the designer, your CSS only styles the preview, so the dashboard around it never changes. **Classes you can style** lists the page's parts, such as `.status-hero`, `.status-row` and `.day`.
- **Top HTML** is shown above the title. Use it for a logo or links to your other sites.
- **Bottom HTML** is shown above the footer. Use it for contact details.

The page takes scripts, frames, forms and event handlers like `onclick` out of your HTML. Links may only go to web, mail or phone addresses. Images, fonts and stylesheets may come from other HTTPS sites, so an `@import` of a web font works.

The CSS can be up to 50,000 characters, and each HTML box up to 20,000.

## Sharing it

The dashboard listens only on this machine by default. To share the status page, make the dashboard reachable, ideally behind a reverse proxy with HTTPS (see [Account and security](/docs/security)). Visitors who aren't signed in can open `/status` and nothing else.
