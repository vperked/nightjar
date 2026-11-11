import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter, useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ConfigContext } from '../src/api/ConfigContext';
import { NotificationsProvider } from '../src/api/NotificationsContext';
import { UpdatesProvider } from '../src/api/UpdatesContext';
import type { Notification, UpdateTarget, UpdatesResponse } from '../src/api/types';
import { Bell } from '../src/components/Bell';
import { ToastProvider } from '../src/components/Toasts';
import { NotificationsPage } from '../src/pages/Notifications';
import { UpdatesPage } from '../src/pages/Updates';

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

type Role = 'admin' | 'viewer';

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

// as wraps a page in a signed-in account, the admin or a read-only viewer, with the app's providers.
const as = (role: Role, page: ReactNode, at = '/') =>
  render(
    <ConfigContext.Provider value={{ auth_required: true, authorized: true, status_page_enabled: false, status_page_title: '', account: { configured: true, username: 'sam', role } }}>
      <MemoryRouter initialEntries={[at]}>
        <ToastProvider>
          <UpdatesProvider enabled>
            <NotificationsProvider enabled>{page}</NotificationsProvider>
          </UpdatesProvider>
        </ToastProvider>
      </MemoryRouter>
    </ConfigContext.Provider>,
  );

type FetchMock = ReturnType<typeof serveUpdates>;

// writes are the requests that would change something.
const writes = (fetchMock: FetchMock) => fetchMock.mock.calls.filter(([, init]) => (init as RequestInit | undefined)?.method && (init as RequestInit).method !== 'GET');

// ---------- updates ----------

const target = (over: Partial<UpdateTarget>): UpdateTarget => ({
  id: 'sonarr',
  name: 'Sonarr',
  kind: 'arr',
  auto: true,
  paused: false,
  available: true,
  can_update: true,
  checked: 1,
  installed: '4.0.14',
  latest: '4.0.15',
  ...over,
});
const updates = (): UpdatesResponse => ({
  enabled: true,
  interval: 21600,
  window: { start: 4, end: 6, label: '04:00–06:00' },
  commands_editable: false,
  kinds: ['arr'],
  checking: false,
  last_check: 1,
  targets: [target({}), target({ id: 'radarr', name: 'Radarr', auto: false })],
});

function serveUpdates() {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = String(input);
    if (url === '/api/notifications/summary') return reply({ counts: { unread: 0, alerts: 0, latest: 0 }, mutes: [], quiet: false, held: 0 });
    if (url.startsWith('/api/updates')) return reply(updates());
    return reply({});
  });
}

describe('updates for a viewer', () => {
  it('shows every target and its state, with nothing that changes them', async () => {
    const fetchMock = serveUpdates();
    as('viewer', <UpdatesPage />);
    expect(await screen.findByText('Sonarr')).toBeInTheDocument();
    for (const name of ['Check now', 'Detect apps', 'Add', 'Update all (2)', 'Update']) expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
    // The auto switch still says whether it updates on its own, but cannot be turned.
    const auto = screen.getByRole('switch', { name: 'Update Sonarr automatically' });
    expect(auto).toBeChecked();
    expect(auto).toBeDisabled();
    expect(auto.closest('label')).toHaveAttribute('title', "Viewers can't change this");

    // A row still opens to its details, without Check now, Edit or Remove.
    fireEvent.click(screen.getByRole('button', { name: /Sonarr/ }));
    expect(await screen.findByText('History')).toBeInTheDocument();
    for (const name of ['Check now', 'Edit', 'Remove']) expect(screen.queryByRole('button', { name })).not.toBeInTheDocument();
    // The filters still work.
    fireEvent.click(within(screen.getByRole('group', { name: 'Filter' })).getByRole('button', { name: /Auto/ }));
    await waitFor(() => expect(screen.queryByText('Radarr')).not.toBeInTheDocument());
    expect(writes(fetchMock)).toEqual([]);
  });

  it('keeps every control for the admin', async () => {
    serveUpdates();
    as('admin', <UpdatesPage />);
    expect(await screen.findByText('Sonarr')).toBeInTheDocument();
    for (const name of ['Check now', 'Detect apps', 'Add', 'Update all (2)']) expect(screen.getByRole('button', { name })).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Update' })).toHaveLength(2);
    const auto = screen.getByRole('switch', { name: 'Update Sonarr automatically' });
    expect(auto).toBeEnabled();
    expect(auto.closest('label')).toHaveAttribute('title', 'Update Sonarr automatically');
    fireEvent.click(screen.getByRole('button', { name: /Sonarr/ }));
    expect(await screen.findByRole('button', { name: 'Edit' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove' })).toBeInTheDocument();
  });
});

// ---------- notifications ----------

const note = (over: Partial<Notification>): Notification => ({
  id: 2,
  ts: Date.now() / 1000 - 60,
  area: 'services',
  severity: 'critical',
  title: 'bazarr failed',
  body: 'Bazarr stopped and did not come back.',
  fields: [],
  subject: 'unit:bazarr.service',
  status: 'sent',
  channels: ['Discord'],
  read: false,
  ...over,
});
const counts = { unread: 1, alerts: 1, worst: 'critical', latest: 2 };

function serveNotifications() {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const url = String(input);
    if (url === '/api/notifications/summary') return reply({ counts, mutes: [], quiet: false, held: 0 });
    if (url.startsWith('/api/notifications/read')) return reply({ counts: { ...counts, unread: 0, alerts: 0 } });
    if (url.startsWith('/api/updates')) return reply(updates());
    if (url.startsWith('/api/notifications')) return reply({ items: [note({})], total: 1, counts, kept: true });
    return reply({});
  });
}

const markedRead = (fetchMock: FetchMock) => fetchMock.mock.calls.some(([u]) => String(u).startsWith('/api/notifications/read'));

const Where = () => <div data-testid="where">{useLocation().pathname + useLocation().search}</div>;

describe('notifications for a viewer', () => {
  it('the bell lists and opens alerts without marking them read', async () => {
    const fetchMock = serveNotifications();
    as(
      'viewer',
      <>
        <Bell />
        <Where />
      </>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Notifications, 1 unread alert' }));
    const panel = screen.getByRole('dialog', { name: 'Latest notifications' });
    expect(within(panel).queryByRole('button', { name: 'Mark all read' })).not.toBeInTheDocument();
    fireEvent.click(await within(panel).findByText('bazarr failed'));
    expect(screen.getByTestId('where')).toHaveTextContent('/system?open=bazarr.service');
    // Give a stray request the chance to go out.
    await new Promise((r) => setTimeout(r, 20));
    expect(markedRead(fetchMock)).toBe(false);
    expect(writes(fetchMock)).toEqual([]);
  });

  it('the inbox opens an alert without marking it read, muting or marking', async () => {
    const fetchMock = serveNotifications();
    as('viewer', <NotificationsPage />, '/notifications');
    fireEvent.click(await screen.findByRole('button', { name: /bazarr failed/ }));
    expect(screen.getByRole('link', { name: 'Open' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Mark (read|unread)$/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Mute/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Mark all/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('link', { name: 'Open' }));
    await new Promise((r) => setTimeout(r, 20));
    expect(markedRead(fetchMock)).toBe(false);
    expect(writes(fetchMock)).toEqual([]);
  });

  it('the admin still marks read from the bell and the inbox', async () => {
    const fetchMock = serveNotifications();
    as('admin', <Bell />);
    fireEvent.click(await screen.findByRole('button', { name: 'Notifications, 1 unread alert' }));
    const panel = screen.getByRole('dialog', { name: 'Latest notifications' });
    expect(within(panel).getByRole('button', { name: 'Mark all read' })).toBeInTheDocument();
    fireEvent.click(await within(panel).findByText('bazarr failed'));
    await waitFor(() => expect(markedRead(fetchMock)).toBe(true));
  });

  it('the admin inbox keeps Mark read, Mute and Mark all read', async () => {
    serveNotifications();
    as('admin', <NotificationsPage />, '/notifications');
    expect(await screen.findByRole('button', { name: 'Mark all 1 read' })).toBeInTheDocument();
    fireEvent.click(await screen.findByRole('button', { name: /bazarr failed/ }));
    expect(await screen.findByRole('button', { name: /^Mark (read|unread)$/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /Mute/ })).toBeInTheDocument();
  });
});
