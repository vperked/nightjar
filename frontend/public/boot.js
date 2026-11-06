// Runs before the page paints, so the loading screen opens in the right colours: the theme picked
// with the toggle, else night for the dashboard and the visitor's own setting on the public status
// page (the same choice as startingTheme in src/theme.ts). A file, not inline, because the page's
// policy allows no inline script.
(function () {
  var d = document.documentElement;
  var status = location.pathname === '/status';
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
