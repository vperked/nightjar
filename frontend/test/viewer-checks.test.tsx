import { act, render, screen, waitFor } from '@testing-library/react';
import type { ReactNode } from 'react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ConfigContext } from '../src/api/ConfigContext';
import { NotificationsProvider } from '../src/api/NotificationsContext';
import { ServicesProvider } from '../src/api/ServicesContext';
import type { MaintenanceWindow, Service } from '../src/api/types';
import { ToastProvider } from '../src/components/Toasts';
import { MaintenancePage } from '../src/pages/Maintenance';
import { ServiceDetail } from '../src/pages/ServiceDetail';
import { ServicesPage } from '../src/pages/Services';

afterEach(() => {
  vi.restoreAllMocks();
});

// as wraps a page in a signed-in account: the admin, or a read-only viewer.
const as = (role: 'admin' | 'viewer', page: ReactNode) => (
  <ConfigContext.Provider value={{ auth_required: true, authorized: true, status_page_enabled: false, status_page_title: '', account: { configured: true, username: 'sam', role } }}>
    <ToastProvider>{page}</ToastProvider>
  </ConfigContext.Provider>
);
const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const web = {
  name: 'web',
  type: 'http',
  target: 'https://example.com',
  status: 'up',
  detail: '200 OK',
  recent: [],
  latency_ms: 40,
  uptime_24h: 100,
  last_check: 0,
  repair: { action: 'restart', unit: 'nginx.service', max: 2, wait: 120 },
} as unknown as Service;

// serve answers the pages' reads; anything else is a 404.
function serve(routes: Record<string, unknown>) {
  vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
    const path = String(input).split('?')[0];
    return path in routes ? reply(routes[path]) : reply({}, 404);
  });
}

describe('viewer: check controls left out', () => {
  it('shows a viewer a check without editing, pausing, muting, deleting or repairing it', async () => {
    serve({
      '/api/services': { services: [web] },
      '/api/services/history': { name: 'web', hours: 24, points: [] },
      '/api/repairs': { repairs: [], dir: '', scripts: [] },
      '/api/notifications/summary': { counts: {}, mutes: [], quiet: false, held: 0 },
    });
    const page = (
      <MemoryRouter initialEntries={['/services/web']}>
        <ServicesProvider enabled>
          <NotificationsProvider enabled>
            <Routes>
              <Route path="/services/:name" element={<ServiceDetail />} />
            </Routes>
          </NotificationsProvider>
        </ServicesProvider>
      </MemoryRouter>
    );
    const controls = ['Edit', 'Pause', 'Delete', 'Mute web', 'Try it now'];
    const { unmount } = render(as('viewer', page));
    expect(await screen.findByText(/restart nginx\.service/)).toBeInTheDocument();
    // The mute shows once the bell's summary is in, so wait for it before looking.
    await waitFor(() => expect(vi.mocked(fetch).mock.calls.some(([u]) => String(u).startsWith('/api/notifications/summary'))).toBe(true));
    await act(async () => {});
    for (const name of controls) expect(screen.queryByRole('button', { name })).toBeNull();
    // Looking around stays: the time range and the way back.
    expect(screen.getByRole('link', { name: 'Services' })).toBeInTheDocument();
    expect(screen.getByText('24h')).toBeInTheDocument();
    unmount();

    render(as('admin', page));
    expect(await screen.findByText(/restart nginx\.service/)).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Mute web' })).toBeInTheDocument();
    for (const name of controls) expect(screen.getByRole('button', { name })).toBeInTheDocument();
  });

  it('gives a viewer no add or edit on the list, even by #add', async () => {
    serve({ '/api/services': { services: [web] }, '/api/hosts': { hosts: [] } });
    const page = (
      <MemoryRouter initialEntries={['/services#add']}>
        <ServicesProvider enabled>
          <ServicesPage />
        </ServicesProvider>
      </MemoryRouter>
    );
    const { unmount } = render(as('viewer', page));
    expect(await screen.findByRole('link', { name: 'web' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add check' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Edit web' })).toBeNull();
    expect(screen.queryByRole('form')).toBeNull();
    expect(document.querySelector('form')).toBeNull();
    // Filtering stays.
    expect(screen.getByLabelText('Filter services')).toBeInTheDocument();
    unmount();

    render(as('admin', page));
    expect(await screen.findByRole('link', { name: 'web' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Edit web' })).toBeInTheDocument();
    // #add opens the form for the admin.
    expect(document.querySelector('form')).not.toBeNull();
  });
});

describe('viewer: maintenance controls left out', () => {
  it('lists the windows for a viewer without planning, editing or removing them', async () => {
    const w: MaintenanceWindow = { id: 'm1', title: 'Disk swap', checks: [], start: 2000000000, end: 2000003600, public: false };
    serve({ '/api/maintenance': { windows: [w], upcoming: [{ window: w, start: 2000000000, end: 2000003600, on: false }] }, '/api/services': { services: [] } });
    const page = (
      <MemoryRouter initialEntries={['/incidents/maintenance']}>
        <ServicesProvider enabled>
          <MaintenancePage />
        </ServicesProvider>
      </MemoryRouter>
    );
    const controls = ['Plan maintenance', 'Edit', 'Remove'];
    const { unmount } = render(as('viewer', page));
    expect(await screen.findByText('Disk swap')).toBeInTheDocument();
    for (const name of controls) expect(screen.queryByRole('button', { name })).toBeNull();
    unmount();

    render(as('admin', page));
    expect(await screen.findByText('Disk swap')).toBeInTheDocument();
    for (const name of controls) expect(screen.getByRole('button', { name })).toBeInTheDocument();
  });
});
