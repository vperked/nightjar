import './styles/fonts.css';
import { StrictMode } from 'react';
import { createRoot } from 'react-dom/client';
import App from './App';
import { connect } from './api/client';
import './styles.css';
import { startingTheme } from './theme';

// Apply the theme before first paint to avoid a flash (the same choice as useTheme in App).
document.documentElement.dataset.theme = startingTheme();

// A page file that fails to download (vite:preloadError) is left to App: Vite cannot tell an opened
// page from the idle prefetch, and only the first is worth a reload (see opened in App.tsx).

connect().then(() =>
  createRoot(document.getElementById('root')!).render(
    <StrictMode>
      <App />
    </StrictMode>,
  ),
);
