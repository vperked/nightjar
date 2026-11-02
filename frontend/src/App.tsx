import { lazy, Suspense, useCallback, useEffect, useLayoutEffect, useRef, useState } from 'react';
import { flushSync } from 'react-dom';
import { BrowserRouter, Link, NavLink, Route, Routes, useLocation } from 'react-router';
import { api, onUnauthorized } from './api/client';
import { ConfigContext } from './api/ConfigContext';
import { NotificationsProvider } from './api/NotificationsContext';
import { ServicesProvider, useServices } from './api/ServicesContext';
import type { AppConfig } from './api/types';
import { UpdatesProvider, useUpdates } from './api/UpdatesContext';
import { CreateAdmin, SignIn } from './components/Account';
import { AccountMenu } from './components/AccountMenu';
import { Bell } from './components/Bell';
import { SectionOff, type SectionID } from './components/Sections';
import { Finder, SearchIcon, openFinder, shortcutLabel } from './components/Finder';
import { HealthInTab, useProblems } from './components/HealthInTab';
import { Skeleton } from './components/Skeleton';
import { ToastProvider } from './components/Toasts';
import { DemoBanner } from './components/ui';
import { formatAgo } from './format';
import { useNow } from './hooks/useNow';
import { Overview } from './pages/Overview';

// Pages other than the overview load on demand, then all of them once the browser is idle, so the
// first visit only waits for the overview and later tab switches are instant.
const pages = {
  apps: () => import('./pages/Apps'),
  plex: () => import('./pages/Plex'),
  logs: () => import('./pages/Logs'),
  network: () => import('./pages/Network'),
  notifications: () => import('./pages/Notifications'),
  qbittorrent: () => import('./pages/QBittorrent'),
  detail: () => import('./pages/ServiceDetail'),
  services: () => import('./pages/Services'),
  settings: () => import('./pages/Settings'),
  setup: () => import('./pages/Setup'),
  statusDesign: () => import('./pages/StatusDesigner'),
  status: () => import('./pages/StatusPage'),
  system: () => import('./pages/SystemServices'),
  updates: () => import('./pages/Updates'),
};
const PlexPage = lazy(() => pages.plex().then((m) => ({ default: m.PlexPage })));
const NetworkPage = lazy(() => pages.network().then((m) => ({ default: m.NetworkPage })));
const AppsPage = lazy(() => pages.apps().then((m) => ({ default: m.AppsPage })));
const LogsPage = lazy(() => pages.logs().then((m) => ({ default: m.LogsPage })));
const NotificationsPage = lazy(() => pages.notifications().then((m) => ({ default: m.NotificationsPage })));
const QBittorrentPage = lazy(() => pages.qbittorrent().then((m) => ({ default: m.QBittorrentPage })));
const ServiceDetail = lazy(() => pages.detail().then((m) => ({ default: m.ServiceDetail })));
const ServicesPage = lazy(() => pages.services().then((m) => ({ default: m.ServicesPage })));
const SettingsPage = lazy(() => pages.settings().then((m) => ({ default: m.SettingsPage })));
const SetupPage = lazy(() => pages.setup().then((m) => ({ default: m.SetupPage })));
const StatusDesignerPage = lazy(() => pages.statusDesign().then((m) => ({ default: m.StatusDesignerPage })));
const StatusPage = lazy(() => pages.status().then((m) => ({ default: m.StatusPage })));
const SystemServicesPage = lazy(() => pages.system().then((m) => ({ default: m.SystemServicesPage })));
const UpdatesPage = lazy(() => pages.updates().then((m) => ({ default: m.UpdatesPage })));

function usePrefetchPages(enabled: boolean) {
  useEffect(() => {
    if (!enabled) return;
    const load = () => Object.values(pages).forEach((p) => p().catch(() => undefined));
    if ('requestIdleCallback' in window) {
      const id = requestIdleCallback(load, { timeout: 4000 });
      return () => cancelIdleCallback(id);
    }
    const id = setTimeout(load, 2000);
    return () => clearTimeout(id);
  }, [enabled]);
}

const Loading = () => (
  <main>
    <Skeleton kind="dashboard" />
  </main>
);

type Theme = 'light' | 'dark';

function useTheme(): [Theme, () => void] {
  const [theme, setTheme] = useState<Theme>(() => {
    try {
      const saved = localStorage.getItem('servermonitor.theme');
      if (saved === 'light' || saved === 'dark') return saved;
    } catch {
      /* storage unavailable */
    }
    return window.matchMedia?.('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
  });
  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try {
      localStorage.setItem('servermonitor.theme', theme);
    } catch {
      /* storage unavailable */
    }
  }, [theme]);
  // Switching cross-fades the whole page where the browser can (View Transitions), instantly elsewhere.
  const toggle = useCallback(() => {
    const next: Theme = theme === 'dark' ? 'light' : 'dark';
    const apply = () => {
      document.documentElement.dataset.theme = next;
      flushSync(() => setTheme(next));
    };
    const doc = document as Document & {
      startViewTransition?: (cb: () => void) => unknown;
    };
    if (doc.startViewTransition && !window.matchMedia?.('(prefers-reduced-motion: reduce)').matches) doc.startViewTransition(apply);
    else apply();
  }, [theme]);
  return [theme, toggle];
}

// useNavIndicator slides one highlight under the active tab instead of each tab lighting up on its own.
function useNavIndicator(deps: unknown[]) {
  const nav = useRef<HTMLElement>(null);
  const place = useCallback(() => {
    const el = nav.current;
    const active = el?.querySelector<HTMLElement>('a.active');
    if (!el) return;
    if (!active) {
      el.style.setProperty('--ind-o', '0');
      return;
    }
    el.style.setProperty('--ind-x', `${active.offsetLeft}px`);
    el.style.setProperty('--ind-w', `${active.offsetWidth}px`);
    el.style.setProperty('--ind-o', '1');
    // No glide on the first placement, only between tabs.
    requestAnimationFrame(() => el.classList.add('ind-ready'));
  }, []);
  // Re-placed whenever the caller's deps change (the page, the tabs on offer).
  useLayoutEffect(place, deps); // eslint-disable-line react-hooks/exhaustive-deps
  useEffect(() => {
    const el = nav.current;
    if (!el || typeof ResizeObserver === 'undefined') return;
    const ro = new ResizeObserver(place);
    ro.observe(el);
    return () => ro.disconnect();
  }, [place]);
  return nav;
}

// Tab icons: 24px grid, 1.8px strokes, drawn in currentColor.
const icon = (d: string) => () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d={d} />
  </svg>
);
// Five tabs; pages that belong together share one (Services, Media, Apps), with sub-tabs on the page.
// `also` lists the other paths a tab is active on.
const TABS: { to: string; label: string; end?: boolean; also?: string[]; media?: boolean; badge?: boolean; section?: SectionID; Icon: () => JSX.Element }[] = [
  {
    to: '/',
    label: 'Overview',
    end: true,
    Icon: icon('M4 4h7v7H4zM13 4h7v4h-7zM13 10h7v10h-7zM4 13h7v7H4z'),
  },
  {
    to: '/services',
    label: 'Services',
    also: ['/system'],
    Icon: icon('M3 12h4l3-8 4 16 3-8h4'),
  },
  {
    to: '/network',
    label: 'Network',
    section: 'network',
    Icon: icon('M7 20V4M3.5 7.5 7 4l3.5 3.5M17 4v16m-3.5-3.5L17 20l3.5-3.5'),
  },
  {
    to: '/plex',
    label: 'Media',
    media: true,
    section: 'media',
    also: ['/qbittorrent'],
    Icon: icon('M4 6h16v12H4zM10 9.5v5l4-2.5z'),
  },
  { to: '/apps', label: 'Apps', badge: true, section: 'apps', also: ['/updates'], Icon: icon('M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 17h6M17 14v6') },
];

const GearIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <circle cx="12" cy="12" r="3" />
    <path d="M19.4 15a1.7 1.7 0 0 0 .34 1.87l.06.06a2 2 0 1 1-2.83 2.83l-.06-.06a1.7 1.7 0 0 0-1.87-.34 1.7 1.7 0 0 0-1 1.55V21a2 2 0 1 1-4 0v-.09a1.7 1.7 0 0 0-1.11-1.55 1.7 1.7 0 0 0-1.87.34l-.06.06a2 2 0 1 1-2.83-2.83l.06-.06a1.7 1.7 0 0 0 .34-1.87 1.7 1.7 0 0 0-1.55-1H3a2 2 0 1 1 0-4h.09a1.7 1.7 0 0 0 1.55-1.11 1.7 1.7 0 0 0-.34-1.87l-.06-.06a2 2 0 1 1 2.83-2.83l.06.06a1.7 1.7 0 0 0 1.87.34H9a1.7 1.7 0 0 0 1-1.55V3a2 2 0 1 1 4 0v.09a1.7 1.7 0 0 0 1 1.55 1.7 1.7 0 0 0 1.87-.34l.06-.06a2 2 0 1 1 2.83 2.83l-.06.06a1.7 1.7 0 0 0-.34 1.87V9a1.7 1.7 0 0 0 1.55 1H21a2 2 0 1 1 0 4h-.09a1.7 1.7 0 0 0-1.55 1z" />
  </svg>
);

// Brand is the wordmark. Its mark carries the server's health (green, or red while something is
// down), as the browser tab does.
function Brand({ enabled }: { enabled: boolean }) {
  const down = useProblems(enabled);
  const health = !enabled ? '' : down ? 'bad' : 'good';
  return (
    <>
      <Link to="/" className="wordmark" aria-label={enabled ? `servermonitor: ${down ? `${down} down` : 'all good'}` : 'servermonitor'}>
        <span className={`brand-mark ${health}`} aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2.2" strokeLinecap="round" strokeLinejoin="round">
            <path d="M3 12.5h3.5l2.5-6 4 12 3-8 1.5 2H21" />
          </svg>
        </span>
        <span>
          server<span className="wordmark-dim">monitor</span>
        </span>
      </Link>
      <HealthInTab down={down} />
    </>
  );
}

// UpdatesBadge counts updates waiting on the nav tab.
function UpdatesBadge() {
  const { data } = useUpdates();
  const n = data?.targets.filter((t) => t.available && !t.paused).length ?? 0;
  return n > 0 ? (
    <span className="nav-badge" aria-label={`${n} available`}>
      {n}
    </span>
  ) : null;
}

// SetupBanner points a new monitor at the setup guide until alerts and checks exist, or it is dismissed.
function SetupBanner() {
  const [hidden, setHidden] = useState(() => {
    try {
      return localStorage.getItem('servermonitor.setup.dismissed') === '1';
    } catch {
      return false;
    }
  });
  if (hidden) return null;
  const dismiss = () => {
    setHidden(true);
    try {
      localStorage.setItem('servermonitor.setup.dismissed', '1');
    } catch {
      /* storage unavailable */
    }
  };
  return (
    <div className="setup-banner" role="note">
      <span>
        <strong>Finish setting up.</strong> Add alerts and uptime checks, and connect Plex or qBittorrent if they run here.
      </span>
      <Link className="btn" to="/setup">
        Open the setup guide
      </Link>
      <button className="icon-btn" aria-label="Dismiss" onClick={dismiss}>
        ×
      </button>
    </div>
  );
}

function Freshness({ version }: { version?: string }) {
  const { data, error } = useServices();
  const now = useNow();
  const [lastOk, setLastOk] = useState<number>();
  useEffect(() => {
    if (data && !error) setLastOk(Date.now() / 1000);
  }, [data, error]);
  return (
    <footer className="footer">
      <span>server monitor {version ?? ''}</span>
      <span className="num">{error ? 'connection lost, retrying' : lastOk ? `updated ${formatAgo(lastOk, now)}` : ''}</span>
    </footer>
  );
}

function Shell() {
  const [theme, toggleTheme] = useTheme();
  const [config, setConfig] = useState<AppConfig>();
  const [signedOut, setSignedOut] = useState(false);
  const [authKey, setAuthKey] = useState(0);
  const [configKey, setConfigKey] = useState(0);
  const location = useLocation();
  const navRef = useNavIndicator([location.pathname, signedOut, config]);

  useEffect(() => {
    api
      .config()
      .then((c) => {
        setConfig(c);
        setSignedOut(c.auth_required && !c.authorized);
      })
      .catch(() => undefined);
  }, [authKey, configKey]);
  // Settings that change what the dashboard shows (sections) ask for the config again.
  useEffect(() => {
    const reload = () => setConfigKey((k) => k + 1);
    window.addEventListener('config-changed', reload);
    return () => window.removeEventListener('config-changed', reload);
  }, []);

  useEffect(() => onUnauthorized(() => setSignedOut(true)), []);
  // Signing in or out reloads the config and restarts every poll.
  const signedIn = () => setAuthKey((k) => k + 1);
  const signOut = async () => {
    await api.logout().catch(() => undefined);
    setAuthKey((k) => k + 1);
  };

  // The public status page needs none of the dashboard's pages.
  usePrefetchPages(location.pathname !== '/status');
  if (location.pathname === '/status')
    return (
      <Suspense fallback={null}>
        <StatusPage />
      </Suspense>
    );

  // Media opens on Plex, or on qBittorrent when Plex is not watched; it hides when neither is.
  const shown = (id: SectionID) => !config?.hidden?.includes(id);
  const tabs = TABS.filter((t) => (!t.section || shown(t.section)) && (!t.media || config?.plex_enabled || config?.qbittorrent_enabled)).map((t) =>
    t.media && !config?.plex_enabled ? { ...t, to: '/qbittorrent', also: ['/plex'] } : t,
  );
  // A fresh install shows nothing until the admin account exists; after that, nothing until signed in.
  const gate = config?.account && !config.account.configured ? <CreateAdmin onDone={signedIn} /> : <SignIn onDone={signedIn} />;
  const gated = (el: JSX.Element) => (signedOut ? gate : el);
  // A page whose section is off says so, with a way to turn it back on.
  const section = (id: SectionID, el: JSX.Element) => gated(shown(id) ? el : <SectionOff id={id} />);
  return (
    <ConfigContext.Provider value={config}>
      <ServicesProvider enabled={!signedOut} key={authKey}>
        <UpdatesProvider enabled={!signedOut}>
          <NotificationsProvider enabled={!signedOut}>
            <header className="masthead">
              <div className="masthead-inner">
                <Brand enabled={!signedOut} />
                {!signedOut && (
                  <nav className="nav" aria-label="Pages" ref={navRef}>
                    <span className="nav-indicator" aria-hidden="true" />
                    {tabs.map((t) => (
                      <NavLink
                        key={t.to}
                        to={t.to}
                        end={t.end}
                        aria-label={t.label}
                        title={t.label}
                        className={({ isActive }) => (isActive || t.also?.some((p) => location.pathname.startsWith(p)) ? 'active' : '')}
                      >
                        <t.Icon />
                        <span>{t.label}</span>
                        {t.badge && <UpdatesBadge />}
                      </NavLink>
                    ))}
                  </nav>
                )}
                <div className="nav-utils">
                  {!signedOut && (
                    <button className="icon-btn finder-btn" onClick={openFinder} aria-label="Find (Ctrl+K)" title="Find a page, check, service or setting (Ctrl+K or /)">
                      <SearchIcon />
                      <span className="finder-btn-label">Find</span>
                      <kbd>{shortcutLabel}</kbd>
                    </button>
                  )}
                  {!signedOut && shown('notifications') && <Bell />}
                  {!signedOut && (
                    <NavLink
                      to="/settings"
                      className={({ isActive }) => `icon-btn${isActive || location.pathname === '/setup' || location.pathname === '/status-page' ? ' active' : ''}`}
                      aria-label="Settings"
                      title="Settings"
                    >
                      <GearIcon />
                    </NavLink>
                  )}
                  <AccountMenu theme={theme} onToggleTheme={toggleTheme} onSignOut={signOut} />
                </div>
              </div>
            </header>
            {!signedOut && <Finder onToggleTheme={toggleTheme} />}
            <DemoBanner />
            {!signedOut && config?.needs_setup && location.pathname === '/' && <SetupBanner />}
            <div className="app">
              <Suspense fallback={<Loading />}>
                {/* Keyed by page, so each page arrives with a short entrance. */}
                <div className="page" key={location.pathname}>
                  <Routes>
                    <Route path="/services" element={gated(<ServicesPage />)} />
                    <Route path="/system" element={gated(<SystemServicesPage />)} />
                    <Route path="/plex" element={section('media', <PlexPage />)} />
                    <Route path="/network" element={section('network', <NetworkPage />)} />
                    <Route path="/qbittorrent" element={section('media', <QBittorrentPage />)} />
                    <Route path="/updates" element={section('apps', <UpdatesPage />)} />
                    <Route path="/apps" element={section('apps', <AppsPage />)} />
                    <Route path="/logs" element={gated(<LogsPage />)} />
                    <Route path="/notifications" element={gated(<NotificationsPage />)} />
                    <Route path="/notifications/rules" element={gated(<NotificationsPage />)} />
                    <Route path="/settings" element={gated(<SettingsPage />)} />
                    <Route path="/setup" element={gated(<SetupPage />)} />
                    <Route path="/status-page" element={gated(<StatusDesignerPage />)} />
                    <Route path="/services/:name" element={gated(<ServiceDetail />)} />
                    <Route path="*" element={gated(<Overview />)} />
                  </Routes>
                </div>
              </Suspense>
              {!signedOut && <Freshness version={config?.version} />}
            </div>
          </NotificationsProvider>
        </UpdatesProvider>
      </ServicesProvider>
    </ConfigContext.Provider>
  );
}

export default function App() {
  return (
    <BrowserRouter>
      <ToastProvider>
        <Shell />
      </ToastProvider>
    </BrowserRouter>
  );
}
