import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { useRef, useState } from 'react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import App from '../src/App';
import { ConfigContext } from '../src/api/ConfigContext';
import { NotificationsProvider } from '../src/api/NotificationsContext';
import { AccountMenu } from '../src/components/AccountMenu';
import { MutedTag } from '../src/components/MuteButton';
import { ToastProvider, useToast } from '../src/components/Toasts';
import { useDialog } from '../src/hooks/useDialog';

afterEach(() => {
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const admin = { auth_required: true, authorized: true, status_page_enabled: true, status_page_title: '', account: { configured: true, username: 'jay', role: 'admin' as const } };

// A sheet opened from a button, as the dashboard's sheets are.
function Sheet() {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);
  useDialog(ref, open);
  return (
    <>
      <button onClick={() => setOpen(true)}>Open</button>
      {open && (
        <div ref={ref} role="dialog" aria-modal="true" aria-label="Sheet">
          <button onClick={() => setOpen(false)}>Close</button>
          <input aria-label="Name" />
          <button>Save</button>
        </div>
      )}
    </>
  );
}

describe('dialogs', () => {
  it('moves focus in, keeps Tab inside, stops the page scrolling, and gives focus back on closing', () => {
    render(<Sheet />);
    const opener = screen.getByRole('button', { name: 'Open' });
    opener.focus();
    fireEvent.click(opener);
    const close = screen.getByRole('button', { name: 'Close' });
    const save = screen.getByRole('button', { name: 'Save' });
    expect(document.activeElement).toBe(close);
    expect(document.body.style.overflow).toBe('hidden');
    // Tab from the last control wraps to the first, and Shift+Tab from the first to the last.
    save.focus();
    fireEvent.keyDown(save, { key: 'Tab' });
    expect(document.activeElement).toBe(close);
    fireEvent.keyDown(close, { key: 'Tab', shiftKey: true });
    expect(document.activeElement).toBe(save);
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(document.activeElement).toBe(opener);
    expect(document.body.style.overflow).toBe('');
  });
});

describe('menus', () => {
  const menu = () =>
    render(
      <MemoryRouter>
        <ConfigContext.Provider value={admin}>
          <AccountMenu theme="light" onToggleTheme={() => undefined} onSignOut={() => undefined} />
        </ConfigContext.Provider>
      </MemoryRouter>,
    );

  it('moves between items with the arrow keys, Home and End, and Escape returns to the button', () => {
    menu();
    const button = screen.getByRole('button', { name: 'Account: jay' });
    fireEvent.click(button);
    const items = screen.getAllByRole('menuitem');
    expect(document.activeElement).toBe(items[0]);
    fireEvent.keyDown(items[0], { key: 'ArrowDown' });
    expect(document.activeElement).toBe(items[1]);
    fireEvent.keyDown(items[1], { key: 'ArrowUp' });
    fireEvent.keyDown(items[0], { key: 'ArrowUp' });
    expect(document.activeElement).toBe(items[items.length - 1]);
    fireEvent.keyDown(document.activeElement!, { key: 'Home' });
    expect(document.activeElement).toBe(items[0]);
    fireEvent.keyDown(items[0], { key: 'End' });
    expect(document.activeElement).toBe(items[items.length - 1]);
    fireEvent.keyDown(document.activeElement!, { key: 'Escape' });
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(document.activeElement).toBe(button);
  });

  it('closes when focus moves somewhere else, and returns to the button after choosing', () => {
    render(
      <>
        <MemoryRouter>
          <ConfigContext.Provider value={admin}>
            <AccountMenu theme="light" onToggleTheme={() => undefined} onSignOut={() => undefined} />
          </ConfigContext.Provider>
        </MemoryRouter>
        <button>Elsewhere</button>
      </>,
    );
    const button = screen.getByRole('button', { name: 'Account: jay' });
    fireEvent.click(button);
    fireEvent.blur(screen.getAllByRole('menuitem')[0], { relatedTarget: screen.getByRole('button', { name: 'Elsewhere' }) });
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    fireEvent.click(button);
    fireEvent.click(screen.getByRole('menuitem', { name: 'Monitor log' }));
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
    expect(document.activeElement).toBe(button);
  });
});

describe('muted tag', () => {
  it('has no button of its own when it sits inside one', async () => {
    const until = Date.now() / 1000 + 3600;
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      if (String(input) === '/api/notifications/summary') return json({ counts: { unread: 0, alerts: 0, latest: 0 }, mutes: [{ subject: 'unit:nginx.service', label: 'nginx', until }] });
      return json({});
    });
    render(
      <ConfigContext.Provider value={admin}>
        <ToastProvider>
          <NotificationsProvider enabled>
            <button>
              nginx
              <MutedTag subject="unit:nginx.service" label="nginx" compact readOnly />
            </button>
          </NotificationsProvider>
        </ToastProvider>
      </ConfigContext.Provider>,
    );
    expect(await screen.findByText(/muted,/)).toBeInTheDocument();
    expect(screen.getAllByRole('button')).toHaveLength(1);
  });
});

describe('toasts', () => {
  it('wait while keyboard focus is on them', () => {
    vi.useFakeTimers();
    function Fire() {
      const notify = useToast();
      return <button onClick={() => notify({ tone: 'ok', title: 'Saved' })}>Save</button>;
    }
    render(
      <ToastProvider>
        <Fire />
      </ToastProvider>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    const dismiss = screen.getByRole('button', { name: 'Dismiss' });
    act(() => {
      dismiss.focus();
    });
    act(() => {
      vi.advanceTimersByTime(10_000);
    });
    expect(screen.getByText('Saved')).toBeInTheDocument();
    act(() => {
      dismiss.blur();
    });
    act(() => {
      vi.advanceTimersByTime(5_000);
    });
    expect(screen.queryByText('Saved')).not.toBeInTheDocument();
  });
});

describe('addresses', () => {
  it('says there is no page at an unknown address, instead of showing the overview', async () => {
    window.history.pushState({}, '', '/no-such-page');
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      if (String(input) === '/api/config') return json(admin);
      return json({}, 404);
    });
    render(<App />);
    expect(await screen.findByRole('heading', { name: 'Page not found' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Back to the overview' })).toHaveAttribute('href', '/');
    window.history.pushState({}, '', '/');
  });

  it('names the waiting updates on the Apps tab, and marks a tab current on the pages it gathers', async () => {
    window.history.pushState({}, '', '/system');
    const target = { id: 'a', name: 'A', kind: 'arr', auto: true, paused: false, available: true, can_update: true, checked: 1 };
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      if (String(input) === '/api/config') return json(admin);
      if (String(input) === '/api/updates') return json({ enabled: true, targets: [target, { ...target, id: 'b' }] });
      return json({}, 404);
    });
    render(<App />);
    const pages = await screen.findByRole('navigation', { name: 'Pages' });
    expect(await within(pages).findByRole('link', { name: 'Apps, 2 updates' })).toBeInTheDocument();
    expect(within(pages).getByRole('link', { name: 'Services' })).toHaveAttribute('aria-current', 'page');
    expect(within(pages).getByRole('link', { name: 'Overview' })).not.toHaveAttribute('aria-current');
    window.history.pushState({}, '', '/');
  });
});
