import { useEffect, useRef, useState, type ReactNode } from 'react';
import { useNavigate } from 'react-router';
import { useConfig } from '../api/ConfigContext';
import { useMenu } from '../hooks/useMenu';

// AccountMenu gathers the header's occasional controls behind one button: who is signed in, the
// theme, the monitor's log, the public status page and signing out. It works as a menu from the
// keyboard too (useMenu): arrows between items, Escape back to the button.

const UserIcon = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <circle cx="12" cy="8.5" r="3.5" />
    <path d="M5 20a7 7 0 0 1 14 0" />
  </svg>
);

const icon = (d: string) => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d={d} />
  </svg>
);
const MOON = 'M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z';
const SUN = 'M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8zM12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4';
const GLOBE = 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM3 12h18M12 3c2.5 2.7 3.8 5.7 3.8 9s-1.3 6.3-3.8 9c-2.5-2.7-3.8-5.7-3.8-9S9.5 5.7 12 3z';
const LOG = 'M4 5h16M4 10h16M4 15h10M4 20h7';
const HELP = 'M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18zM9.5 9.2a2.6 2.6 0 0 1 5 .8c0 1.8-2.5 2.2-2.5 4M12 17.2h.01';
const USER = 'M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8zM4 20a8 8 0 0 1 16 0';
const EXIT = 'M15 4h3a2 2 0 0 1 2 2v12a2 2 0 0 1-2 2h-3M10 16l-4-4 4-4M6 12h10';

// Items are reached with the arrow keys, not Tab, as in any menu.
function Item({ children, onClick, href }: { children: ReactNode; onClick?: () => void; href?: string }) {
  if (href)
    return (
      <a role="menuitem" tabIndex={-1} className="account-item" href={href} target="_blank" rel="noreferrer" onClick={onClick}>
        {children}
      </a>
    );
  return (
    <button type="button" role="menuitem" tabIndex={-1} className="account-item" onClick={onClick}>
      {children}
    </button>
  );
}

export function AccountMenu({ theme, onToggleTheme, onSignOut }: { theme: 'light' | 'dark'; onToggleTheme: () => void; onSignOut: () => void }) {
  const config = useConfig();
  const [open, setOpen] = useState(false);
  const root = useRef<HTMLDivElement>(null);
  const button = useRef<HTMLButtonElement>(null);
  const menu = useRef<HTMLDivElement>(null);
  const keys = useMenu(menu, button, open, setOpen);
  const username = config?.account?.username;
  const viewer = config?.account?.role === 'viewer';
  const navigate = useNavigate();

  useEffect(() => {
    if (!open) return;
    const close = (e: MouseEvent) => !root.current?.contains(e.target as Node) && setOpen(false);
    const esc = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false);
    document.addEventListener('mousedown', close);
    document.addEventListener('keydown', esc);
    return () => {
      document.removeEventListener('mousedown', close);
      document.removeEventListener('keydown', esc);
    };
  }, [open]);

  // Choosing an item closes the menu back onto its button, then does what it says.
  const run = (fn: () => void) => () => {
    keys.done();
    fn();
  };
  return (
    <div className={`account ${open ? 'open' : ''}`} ref={root} onBlur={keys.onBlur}>
      <button
        ref={button}
        type="button"
        className="account-btn"
        aria-haspopup="menu"
        aria-expanded={open}
        aria-label={username ? `Account: ${username}` : 'Menu'}
        title={username ? `Signed in as ${username}` : 'Menu'}
        onClick={() => setOpen((o) => !o)}
      >
        {username ? <span className="avatar">{username.charAt(0).toUpperCase()}</span> : <UserIcon />}
      </button>
      {open && (
        <div className="account-menu" role="menu" aria-label="Account" ref={menu} onKeyDown={keys.onKeyDown}>
          {username && (
            <div className="account-who">
              <span className="avatar large">{username.charAt(0).toUpperCase()}</span>
              <span className="min0">
                <strong className="truncate">{username}</strong>
                <span>{viewer ? 'viewer, read-only' : 'admin'}</span>
              </span>
            </div>
          )}
          {viewer && (
            <Item onClick={run(() => navigate('/settings'))}>
              {icon(USER)}
              <span>Your password</span>
            </Item>
          )}
          <Item onClick={onToggleTheme}>
            {icon(theme === 'dark' ? SUN : MOON)}
            <span>{theme === 'dark' ? 'Light theme' : 'Dark theme'}</span>
          </Item>
          <Item onClick={run(() => navigate('/logs'))}>
            {icon(LOG)}
            <span>Monitor log</span>
          </Item>
          <Item onClick={run(() => navigate('/docs'))}>
            {icon(HELP)}
            <span>Help and docs</span>
          </Item>
          {config?.status_page_enabled && (
            <Item href={config.status_page_path || '/status'} onClick={keys.done}>
              {icon(GLOBE)}
              <span>Public status page</span>
              <span className="account-hint">↗</span>
            </Item>
          )}
          {username && (
            <>
              <div className="account-sep" role="separator" />
              <Item onClick={run(onSignOut)}>
                {icon(EXIT)}
                <span>Sign out</span>
              </Item>
            </>
          )}
        </div>
      )}
    </div>
  );
}
