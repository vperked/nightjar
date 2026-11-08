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
})();
