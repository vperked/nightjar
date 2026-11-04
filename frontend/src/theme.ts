export type Theme = 'light' | 'dark';

// startingTheme is the one picked with the toggle, if any. Otherwise the dashboard opens at night,
// Nightjar's own look, and the public status page follows each visitor's system setting.
export function startingTheme(): Theme {
  try {
    const saved = localStorage.getItem('nightjar.theme');
    if (saved === 'light' || saved === 'dark') return saved;
  } catch {
    /* storage unavailable */
  }
  if (window.location.pathname === '/status') return window.matchMedia?.('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
  return 'dark';
}
