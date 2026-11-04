// Latin subsets only: other scripts fall back to the system font instead of bloating the binary.
// Sans also carries latin-ext for accented names in titles; mono is numbers, hosts and paths.
import '@fontsource/ibm-plex-sans/latin-400.css';
import '@fontsource/ibm-plex-sans/latin-500.css';
import '@fontsource/ibm-plex-sans/latin-600.css';
import '@fontsource/ibm-plex-sans/latin-ext-400.css';
import '@fontsource/ibm-plex-sans/latin-ext-500.css';
import '@fontsource/ibm-plex-sans/latin-ext-600.css';
import '@fontsource/ibm-plex-mono/latin-400.css';
import '@fontsource/ibm-plex-mono/latin-500.css';
import '@fontsource/ibm-plex-mono/latin-600.css';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import { connect } from './api/client';
import './styles.css';
import { startingTheme } from './theme';

// Apply the theme before first paint to avoid a flash (the same choice as useTheme in App).
document.documentElement.dataset.theme = startingTheme();

// A tab left open across a UI update can ask for a page file that is no longer there; load the new
// build instead of showing a broken page. Once a minute at most, so a real outage can't loop.
window.addEventListener('vite:preloadError', (e) => {
  try {
    const last = Number(sessionStorage.getItem('nightjar.reloaded') || 0);
    if (Date.now() - last < 60_000) return;
    sessionStorage.setItem('nightjar.reloaded', String(Date.now()));
  } catch {
    return;
  }
  e.preventDefault();
  window.location.reload();
});

connect().then(() =>
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  ),
);
