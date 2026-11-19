import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { HostUpdatesSummary, UpdatesResponse, UpdateTarget } from '../src/api/types';
import { UpdatesProvider, useWaitingUpdates } from '../src/api/UpdatesContext';
import { ToastProvider } from '../src/components/Toasts';
import { UpdatesPage, batchKey } from '../src/pages/Updates';

// Updates on agent servers: the machine picker, every action reaching the picked machine, the
// server's own settings, and what an agent too old for updates looks like.

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const t = (over: Partial<UpdateTarget>): UpdateTarget => ({ id: 'sonarr', name: 'Sonarr', kind: 'arr', auto: false, paused: false, available: false, can_update: true, checked: 1, ...over });
const page = (targets: UpdateTarget[], over: Partial<UpdatesResponse> = {}): UpdatesResponse => ({
  enabled: true,
  interval: 21600,
  window: { start: 4, end: 6, label: '04:00–06:00' },
  commands_editable: false,
  kinds: ['system', 'snap', 'apt', 'arr', 'git', 'release'],
  checking: false,
  last_check: 1,
  targets,
  ...over,
});
const summary = (over: Partial<HostUpdatesSummary> & Pick<HostUpdatesSummary, 'id' | 'name'>): HostUpdatesSummary => ({ available: 0, applying: '', checked: 1, error: '', ...over });

const LOCAL = page([t({ id: 'radarr', name: 'Radarr', installed: '5.1', latest: '5.2', available: true })]);
const HOST = page(
  [
    t({ id: 'os', name: 'OS packages (apt)', kind: 'system', available: true, pending: ['a', 'b'] }),
    t({ id: 'plex', name: 'Plex Media Server', kind: 'apt', installed: '1.41', latest: '1.42', available: true }),
  ],
  { window: { start: 240, end: 360, label: '04:00–06:00' }, notify: true, time_zone: { name: 'Europe/Paris', offset: 3600 } },
);
const HOSTS = [summary({ id: 'h1', name: 'USOVH-1', available: 3 }), summary({ id: 'h2', name: 'pi-dns' })];

type Handler = (url: string, init?: RequestInit) => Response | undefined;

// serve answers this machine's updates, the summary and USOVH-1's updates, unless extra answers first.
function serve(extra: Handler = () => undefined, hosts = HOSTS) {
  return vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
    const url = String(input);
    const own = extra(url, init);
    if (own) return own;
    if (url === '/api/updates') return json(LOCAL);
    if (url === '/api/hosts/updates') return json({ hosts });
    if (url === '/api/hosts/h1/updates' && (!init?.method || init.method === 'GET')) return json(HOST);
    if (/\/(check|update)$/.test(url)) return json({ queued: true }, 202);
    return json({ error: 'not found' }, 404);
  });
}

function Where() {
  const loc = useLocation();
  return <output aria-label="where">{loc.pathname + loc.search}</output>;
}

const wrap = (at = '/updates') =>
  render(
    <MemoryRouter initialEntries={[at]}>
      <ToastProvider>
        <UpdatesProvider enabled>
          <Routes>
            <Route path="/updates" element={<UpdatesPage />} />
          </Routes>
          <Where />
        </UpdatesProvider>
      </ToastProvider>
    </MemoryRouter>,
  );

const calls = (fetchMock: ReturnType<typeof serve>, method: string) => fetchMock.mock.calls.filter(([, i]) => (i?.method ?? 'GET') === method).map(([u]) => String(u));

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

describe('updates on agent servers', () => {
  it('lists each server with its waiting updates, and switches the page to it', async () => {
    const fetchMock = serve();
    wrap();
    expect(await screen.findByText('Radarr')).toBeInTheDocument();
    fireEvent.click(await screen.findByRole('combobox', { name: 'Machine' }));
    const options = screen.getAllByRole('option').map((o) => o.textContent);
    expect(options).toEqual(['This machine · 1', 'USOVH-1 · 3', 'pi-dns']);
    fireEvent.mouseDown(screen.getByRole('option', { name: 'USOVH-1 · 3' }));
    // The page now shows the server's, asked of it, and the address keeps the choice.
    expect(await screen.findByText('Plex Media Server')).toBeInTheDocument();
    expect(screen.queryByText('Radarr')).not.toBeInTheDocument();
    expect(screen.getByLabelText('where')).toHaveTextContent('/updates?host=h1');
    expect(calls(fetchMock, 'GET')).toContain('/api/hosts/h1/updates');
  });

  it('offers no picker without agent servers', async () => {
    serve(undefined, []);
    wrap();
    expect(await screen.findByText('Radarr')).toBeInTheDocument();
    await waitFor(() => expect(calls(vi.mocked(fetch), 'GET')).toContain('/api/hosts/updates'));
    expect(screen.queryByRole('combobox', { name: 'Machine' })).not.toBeInTheDocument();
  });

  it('checks, updates and updates all on the picked server, keeping its own Update all', async () => {
    const fetchMock = serve();
    wrap('/updates?host=h1');
    expect(await screen.findByText('Plex Media Server')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Check now' }));
    await waitFor(() => expect(calls(fetchMock, 'POST')).toContain('/api/hosts/h1/updates/check'));

    fireEvent.click(screen.getAllByRole('button', { name: 'Update' })[1]);
    await waitFor(() => expect(calls(fetchMock, 'POST')).toContain('/api/hosts/h1/updates/plex/update'));

    fireEvent.click(screen.getByRole('button', { name: 'Update all (2)' }));
    fireEvent.click(within(screen.getByRole('dialog', { name: 'Update all' })).getByRole('button', { name: 'Update 2 items' }));
    await waitFor(() => expect(calls(fetchMock, 'POST')).toContain('/api/hosts/h1/updates/os/update'));
    // Nothing reached this machine's routes.
    expect(calls(fetchMock, 'POST').filter((u) => u.startsWith('/api/updates'))).toEqual([]);
    // The batch is the server's: this machine's stays free.
    expect(batchKey('h1')).toBe('nightjar.updateAll.h1');
    expect(JSON.parse(localStorage.getItem('nightjar.updateAll.h1')!).ids).toEqual(['os', 'plex']);
    expect(localStorage.getItem('nightjar.updateAll')).toBeNull();
  });

  it('follows only the picked machine’s Update all', async () => {
    localStorage.setItem('nightjar.updateAll', JSON.stringify({ started: 0, ids: ['radarr'], before: { radarr: 0 } }));
    serve();
    wrap('/updates?host=h1');
    expect(await screen.findByText('Plex Media Server')).toBeInTheDocument();
    expect(screen.queryByRole('region', { name: 'Update all' })).not.toBeInTheDocument();
  });

  it('saves the server’s own settings', async () => {
    let saved: unknown;
    const fetchMock = serve((url, init) => {
      if (url === '/api/hosts/h1/updates/settings' && init?.method === 'PUT') {
        saved = JSON.parse(String(init.body));
        return json({ ...HOST, interval: 43200 });
      }
    });
    wrap('/updates?host=h1');
    const strip = await screen.findByRole('form', { name: 'Update settings on USOVH-1' });
    // The window is on the server's clock, named.
    expect(within(strip).getByText(/Server time \(Europe\/Paris\)/)).toBeInTheDocument();
    expect(within(strip).getByRole('button', { name: 'Auto-update from' })).toHaveTextContent(/4:00|04:00/);
    fireEvent.change(within(strip).getByLabelText('Hours between checks'), { target: { value: '12' } });
    fireEvent.click(within(strip).getByRole('checkbox', { name: /Notify me/ }));
    fireEvent.click(within(strip).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(saved).toEqual({ enabled: true, interval: 43200, window: { start: 240, end: 360 }, notify: false }));
    expect(calls(fetchMock, 'PUT')).toEqual(['/api/hosts/h1/updates/settings']);
  });

  it('says so when the server’s agent is too old', async () => {
    const tooOld = "this server's agent is too old for updates; it updates itself shortly, or reinstall it";
    serve((url) => (url === '/api/hosts/h1/updates' ? json({ error: tooOld }, 404) : undefined));
    wrap('/updates?host=h1');
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent("USOVH-1's agent is too old for updates.");
    expect(within(alert).getByRole('link', { name: 'its server page' })).toHaveAttribute('href', '/servers/h1');
    // Nothing that would only fail is offered.
    expect(screen.queryByRole('button', { name: 'Check now' })).not.toBeInTheDocument();
  });

  it('says so when the agent refuses its settings for being too old', async () => {
    serve((url, init) => (url === '/api/hosts/h1/updates/settings' && init?.method === 'PUT' ? json({ error: "this server's agent is too old for updates" }, 404) : undefined));
    wrap('/updates?host=h1');
    const strip = await screen.findByRole('form', { name: 'Update settings on USOVH-1' });
    fireEvent.click(within(strip).getByRole('checkbox', { name: /Check for updates/ }));
    fireEvent.click(within(strip).getByRole('button', { name: 'Save' }));
    expect(await within(strip).findByRole('alert')).toHaveTextContent("USOVH-1's agent is too old for these settings.");
  });

  it('counts every machine’s waiting updates for the badge', async () => {
    serve();
    function Badge() {
      const w = useWaitingUpdates();
      return <span>{`${w.total} total, ${w.here} here`}</span>;
    }
    render(
      <UpdatesProvider enabled>
        <Badge />
      </UpdatesProvider>,
    );
    expect(await screen.findByText('4 total, 1 here')).toBeInTheDocument();
  });

  it('demo data updates a target on a server and saves its settings', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { demoFetch } = await import('../src/api/demo');
    const get = async (id: string) => (await (await demoFetch(`/api/hosts/${id}/updates`)).json()) as UpdatesResponse;
    const hosts = (await (await demoFetch('/api/hosts/updates')).json()).hosts as HostUpdatesSummary[];
    const nas = hosts.find((h) => h.name === 'nas')!;
    expect(nas.available).toBeGreaterThan(0);
    expect(hosts.find((h) => h.name === 'pi-dns')!.available).toBe(0);
    expect(hosts.find((h) => h.name === 'backup-box')!.error).not.toBe('');
    expect((await demoFetch(`/api/hosts/${hosts.find((h) => h.name === 'backup-box')!.id}/updates`)).status).toBe(422);

    expect((await demoFetch(`/api/hosts/${nas.id}/updates/plex/update`, { method: 'POST', body: '{}' })).status).toBe(202);
    await vi.advanceTimersByTimeAsync(1500);
    const midway = (await get(nas.id)).targets.find((x) => x.id === 'plex')!;
    expect(midway.busy).toBe('updating');
    expect(midway.live?.lines.length).toBeGreaterThan(0);
    await vi.advanceTimersByTimeAsync(10000);
    const after = (await get(nas.id)).targets.find((x) => x.id === 'plex')!;
    expect(after.available).toBe(false);
    expect(after.runs?.[0]).toMatchObject({ ok: true });

    const res = await demoFetch(`/api/hosts/${nas.id}/updates/settings`, { method: 'PUT', body: JSON.stringify({ enabled: false, interval: 7200, window: { start: 60, end: 120 }, notify: false }) });
    const body = (await res.json()) as UpdatesResponse;
    expect(body).toMatchObject({ enabled: false, interval: 7200, notify: false, window: { start: 60, end: 120, label: '01:00–02:00' } });
    vi.useRealTimers();
  });
});
