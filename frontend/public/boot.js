// Runs before the page paints, so the loading screen opens in the right colours: the theme picked
// with the toggle, else night for the dashboard and the visitor's own setting on the public status
// page (the same choice as startingTheme in src/theme.ts). A file, not inline, because the page's
// policy allows no inline script.
(function () {
  var d = document.documentElement;
  // The status page: at /status, or wherever the server marked it (its own domain, a chosen path).
  var status = location.pathname === '/status' || d.dataset.page === 'status';
  var theme = 'dark';
  try {
    var saved = localStorage.getItem('nightjar.theme');
    if (saved === 'light' || saved === 'dark') theme = saved;
    else if (status && window.matchMedia && matchMedia('(prefers-color-scheme: light)').matches) theme = 'light';
  } catch (e) {
    /* storage unavailable */
  }
  d.dataset.theme = theme;
  // The dashboard's first question is its config, which the app could only ask once its script had
  // downloaded and run. Asked here instead, it is on its way alongside the script, a round trip
  // sooner; the app takes this answer for its first look (earlyConfig in src/api/client.ts). The
  // status page needs no config.
  if (!status && window.fetch) {
    try {
      var early = fetch('/api/config', { cache: 'no-store', credentials: 'same-origin' });
      early['catch'](function () {});
      window.__nightjarConfig = early;
    } catch (e) {
      /* no fetch: the app asks itself */
    }
  }
})();
