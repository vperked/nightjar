import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, describe, expect, it, onTestFinished, vi } from 'vitest';
import { NotificationsProvider } from '../src/api/NotificationsContext';
import { ServicesProvider } from '../src/api/ServicesContext';
import { UpdatesProvider } from '../src/api/UpdatesContext';
import { demoCleanup } from '../src/api/demoIncidents';
import type { CheckSuggestion, MaintenanceWindow, Notification, SetupStatus, Unit, UpdateTarget, UpdatesResponse } from '../src/api/types';
import { ToastProvider } from '../src/components/Toasts';
import { CleanupPage } from '../src/pages/Cleanup';
import { MaintenancePage } from '../src/pages/Maintenance';
import { NotificationsPage } from '../src/pages/Notifications';
import { ServerDetail, ServersPage } from '../src/pages/Servers';
import { SetupPage } from '../src/pages/Setup';
import { SystemServicesPage } from '../src/pages/SystemServices';
import { UpdatesPage, itemState } from '../src/pages/Updates';

// Regression tests for the admin pages: what they send, and what they show after a change.

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const sent = (init?: RequestInit) => JSON.parse(String(init?.body));

// later is a promise resolved by hand, for a reply that comes back while the user goes on.
function later<T>() {
  let resolve!: (v: T) => void;
  const promise = new Promise<T>((r) => (resolve = r));
  return { promise, resolve };
}

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

describe('setup', () => {
  const status = (over: Partial<SetupStatus> = {}): SetupStatus => ({
    alerts: { configured: true, channels: ['Discord'], webhook_set: true },
    plex: { enabled: false, url: 'http://nas:32400', token_set: true, detected: { running: false, url: '', token_found: false } },
    qbittorrent: {
      enabled: true,
      host: 'localhost',
      port: 8080,
      username: 'admin',
      password_set: true,
      detected: { found: false, host: '', port: 0, username: '', local_bypass: false, answering: false },
    },
    checks: { count: 0, suggestions: [] },
    updates: { enabled: true, targets: 3, window: '04:00–06:00' },
    ...over,
  });
  const wrap = () =>
    render(
      <MemoryRouter>
        <ToastProvider>
          <SetupPage />
        </ToastProvider>
      </MemoryRouter>,
    );

  it('keeps the saved Plex token when none is typed', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/setup') return json(status());
      if (url === '/api/settings/test') return json({ ok: true, detail: 'Connected' });
      if (init?.method === 'PUT') return json({ restarting: false, applied: true });
      return json({});
    });
    wrap();
    fireEvent.click(await screen.findByRole('button', { name: 'Watch Plex' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([, i]) => i?.method === 'PUT')).toBe(true));
    const test = fetchMock.mock.calls.find(([u]) => u === '/api/settings/test')!;
    expect(sent(test[1])).toEqual({ target: 'plex', values: { PLEX_URL: 'http://nas:32400' } });
    const save = fetchMock.mock.calls.find(([, i]) => i?.method === 'PUT')!;
    // An empty PLEX_TOKEN would delete the saved one.
    expect(sent(save[1])).toEqual({ values: { PLEX_URL: 'http://nas:32400', PLEX_MONITOR: 'true' } });
  });

  it('counts only the suggestions still listed and ticked', async () => {
    const sonarr: CheckSuggestion = { name: 'Sonarr', type: 'http', target: 'http://127.0.0.1:8989/ping' };
    const radarr: CheckSuggestion = { name: 'Radarr', type: 'http', target: 'http://127.0.0.1:7878/ping' };
    let added = false;
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/checks' && init?.method === 'POST') {
        added = true;
        return json({ check: {} }, 201);
      }
      if (url === '/api/setup') return json(status({ checks: added ? { count: 1, suggestions: [radarr] } : { count: 0, suggestions: [sonarr, radarr] } }));
      return json({});
    });
    wrap();
    fireEvent.click(await screen.findByRole('checkbox', { name: /Radarr/ }));
    fireEvent.click(screen.getByRole('button', { name: 'Add 1 check' }));
    // Sonarr is added and leaves the suggestions; Radarr was left unticked, so there is nothing to add.
    expect(await screen.findByText('1 watched')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add 0 checks' })).toBeDisabled();
  });
});

describe('maintenance', () => {
  it('edits the window whose Edit was clicked last', async () => {
    const win = (id: string, title: string, start: number): MaintenanceWindow => ({ id, title, checks: [], start, end: start + 3600, public: true, note: '' });
    const windows = [win('a', 'NAS update', 2_000_000_000), win('b', 'Router reboot', 2_000_100_000)];
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/services') return json({ services: [] });
      if (init?.method === 'PUT') return json({ window: sent(init) });
      return json({ windows, upcoming: [] });
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <ServicesProvider enabled>
            <MaintenancePage />
          </ServicesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );
    await screen.findByText('Router reboot');
    fireEvent.click(screen.getAllByRole('button', { name: 'Edit' })[0]);
    expect(screen.getByLabelText('Title')).toHaveValue('NAS update');
    fireEvent.click(screen.getAllByRole('button', { name: 'Edit' })[1]);
    expect(screen.getByText('Edit Router reboot')).toBeInTheDocument();
    expect(screen.getByLabelText('Title')).toHaveValue('Router reboot');
    fireEvent.click(screen.getByRole('button', { name: 'Save window' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([, i]) => i?.method === 'PUT')).toBe(true));
    const put = fetchMock.mock.calls.find(([, i]) => i?.method === 'PUT')!;
    expect(put[0]).toBe('/api/maintenance/b');
    expect(sent(put[1])).toMatchObject({ id: 'b', title: 'Router reboot' });
  });
});

describe('cleanup', () => {
  it('shows newer news from the server after a Check now', async () => {
    const full = { ...structuredClone(demoCleanup()), enabled: false };
    const without = (title: string) => ({ ...full, items: full.items.filter((i) => i.title !== title) });
    let gets = 0;
    let scans = 0;
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = String(input);
      if (url === '/api/cleanup/scan') return ++scans === 1 ? json(without('Cats')) : json({ error: 'Plex did not answer' }, 502);
      return json(++gets === 1 ? full : without('Waterworld'));
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <CleanupPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText('Cats (2019)')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Check now' }));
    await waitFor(() => expect(screen.queryByText('Cats (2019)')).not.toBeInTheDocument());
    // The next check fails and the page reloads: what it loads is shown, not the earlier check's reply.
    fireEvent.click(await screen.findByRole('button', { name: 'Check now' }));
    expect(await screen.findByText('The check failed')).toBeInTheDocument();
    expect(await screen.findByText('Cats (2019)')).toBeInTheDocument();
    expect(screen.queryByText('Waterworld (1995)')).not.toBeInTheDocument();
  });
});

describe('update all', () => {
  const t = (over: Partial<UpdateTarget>): UpdateTarget => ({ id: 'sonarr', name: 'Sonarr', kind: 'arr', auto: false, paused: false, available: false, can_update: true, checked: 1, ...over });
  const page = (targets: UpdateTarget[]): UpdatesResponse => ({
    enabled: true,
    interval: 21600,
    window: { start: 4, end: 6, label: '04:00–06:00' },
    commands_editable: false,
    kinds: ['arr'],
    checking: false,
    last_check: 1,
    targets,
  });
  const wrap = () =>
    render(
      <MemoryRouter>
        <ToastProvider>
          <UpdatesProvider enabled>
            <UpdatesPage />
          </UpdatesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );

  it('a run counts when it is newer than the last one before the batch', () => {
    expect(itemState(t({ available: true, runs: [{ at: 100, ok: true, auto: true }] }), 100)).toBe('waiting');
    expect(itemState(t({ runs: [{ at: 101, ok: true, auto: false }] }), 100)).toBe('updated');
  });

  it('follows a batch by the server’s clock, whatever this browser’s says', async () => {
    // This browser runs an hour ahead of the server.
    localStorage.setItem('nightjar.updateAll', JSON.stringify({ started: Date.now() / 1000 + 3600, ids: ['sonarr', 'radarr'], before: { sonarr: 100, radarr: 0 } }));
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      json(
        page([
          t({ installed: '4.0.15', latest: '4.0.15', runs: [{ at: 200, ok: true, auto: false, from: '4.0.14', to: '4.0.15' }] }),
          t({ id: 'radarr', name: 'Radarr', installed: '5.1', latest: '5.2', available: true, runs: [{ at: 300, ok: false, auto: false, detail: 'refused' }] }),
        ]),
      ),
    );
    wrap();
    const run = await screen.findByRole('region', { name: 'Update all' });
    expect(await within(run).findByText('Update all finished · 1 updated · 1 failed')).toBeInTheDocument();
  });

  it('records each target’s last run when the batch starts', async () => {
    const applied: string[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const m = String(input).match(/^\/api\/updates\/([a-z]+)\/update$/);
      if (m && init?.method === 'POST') {
        applied.push(m[1]);
        return json({ queued: true }, 202);
      }
      return json(
        page([
          t({ installed: '4.0.14', latest: '4.0.15', available: true, runs: [{ at: 1234, ok: false, auto: true }] }),
          t({ id: 'radarr', name: 'Radarr', installed: '5.1', latest: '5.2', available: true }),
        ]),
      );
    });
    wrap();
    fireEvent.click(await screen.findByRole('button', { name: 'Update all (2)' }));
    fireEvent.click(within(screen.getByRole('dialog', { name: 'Update all' })).getByRole('button', { name: 'Update 2 items' }));
    await waitFor(() => expect(applied).toEqual(['sonarr', 'radarr']));
    expect(JSON.parse(localStorage.getItem('nightjar.updateAll')!).before).toEqual({ sonarr: 1234, radarr: 0 });
    // Last night's failed run is not this batch's.
    expect(within(await screen.findByRole('region', { name: 'Update all' })).getByText(/Updating all · 0 of 2 done/)).toBeInTheDocument();
  });
});

describe('notification inbox', () => {
  const note = (id: number, over: Partial<Notification> = {}): Notification => ({
    id,
    ts: Date.now() / 1000 - 60 * id,
    area: 'services',
    severity: 'critical',
    title: `problem ${id}`,
    body: '',
    fields: [],
    status: 'sent',
    channels: [],
    read: true,
    ...over,
  });
  const counts = { unread: 0, alerts: 0, latest: 9 };
  const wrap = () =>
    render(
      <MemoryRouter initialEntries={['/notifications']}>
        <ToastProvider>
          <NotificationsProvider enabled>
            <NotificationsPage />
          </NotificationsProvider>
        </ToastProvider>
      </MemoryRouter>,
    );

  it('drops an older page asked for under other filters', async () => {
    const older = later<Response>();
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = String(input);
      if (url === '/api/notifications/summary') return json({ counts, mutes: [] });
      const p = new URL(url, 'http://x').searchParams;
      if (p.get('before')) return older.promise;
      if (p.get('severity') === 'info') return json({ items: [note(5, { severity: 'info', title: 'backup done' })], total: 1, counts, kept: true });
      return json({ items: [note(9), note(8)], total: 4, counts, kept: true });
    });
    wrap();
    fireEvent.click(await screen.findByRole('button', { name: 'Show older' }));
    fireEvent.click(within(screen.getByRole('group', { name: 'Severity' })).getByRole('button', { name: 'Info' }));
    expect(await screen.findByText('backup done')).toBeInTheDocument();
    older.resolve(json({ items: [note(7), note(6)], total: 4, counts, kept: true }));
    await new Promise((r) => setTimeout(r, 20));
    expect(screen.queryByText('problem 7')).not.toBeInTheDocument();
    expect(screen.getByText('backup done')).toBeInTheDocument();
  });

  it('says when an older page cannot be loaded', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = String(input);
      if (url === '/api/notifications/summary') return json({ counts, mutes: [] });
      if (url.includes('before=')) return json({ error: 'database is locked' }, 500);
      return json({ items: [note(9), note(8)], total: 4, counts, kept: true });
    });
    wrap();
    fireEvent.click(await screen.findByRole('button', { name: 'Show older' }));
    expect(await screen.findByText('Could not load older notifications')).toBeInTheDocument();
    expect(screen.getByText('problem 9')).toBeInTheDocument();
  });
});

describe('servers', () => {
  it('keeps the user and port typed while the keys load', async () => {
    const key = { path: '/srv/nightjar/ssh/laptop', name: 'laptop', type: 'ED25519', fingerprint: 'SHA256:x', public: 'ssh-ed25519 AAAA laptop', own: true };
    const keys = later<Response>();
    let added: unknown;
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/hosts' && init?.method === 'POST') {
        added = sent(init);
        return json({ host: { id: 'v1', name: 'vps', address: 'vps', added: 0, kind: 'ssh', status: 'online', readings: null, history: [], problems: [] } });
      }
      if (url === '/api/hosts') return json({ hosts: [], port: 9101 });
      if (url === '/api/hosts/token') return json({ token: 'f'.repeat(48), port: 9101 });
      if (url === '/api/ssh/keys') return keys.promise;
      return json({}, 404);
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <ServersPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Add a server' }));
    const sheet = screen.getByRole('dialog', { name: 'Add a server' });
    fireEvent.click(within(sheet).getByLabelText(/SSH only/));
    fireEvent.change(within(sheet).getByLabelText('Address'), { target: { value: 'vps.example.net' } });
    expect(within(sheet).getByText('Looking for keys…')).toBeInTheDocument();
    fireEvent.change(within(sheet).getByLabelText('User'), { target: { value: 'admin' } });
    fireEvent.change(within(sheet).getByLabelText('Port'), { target: { value: '2222' } });
    keys.resolve(json({ keys: [key] }));
    expect(await within(sheet).findByText('Is laptop on the server already?')).toBeInTheDocument();
    expect(within(sheet).getByLabelText('User')).toHaveValue('admin');
    expect(within(sheet).getByLabelText('Port')).toHaveValue('2222');
    fireEvent.click(within(sheet).getByRole('button', { name: 'Add server' }));
    await waitFor(() => expect(added).toEqual({ kind: 'ssh', name: '', ssh: { key: key.path, user: 'admin', port: 2222, host: 'vps.example.net' } }));
  });

  it('adds firewall rules to every selected server with an agent, Secure web or only Nightjar on the agent port', async () => {
    const host = (id: string, kind: 'agent' | 'ssh') => ({ id, name: id, address: id, added: 0, kind, status: 'online', readings: null, history: [], problems: [] });
    const added: { id: string; rule: unknown }[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/hosts') return json({ hosts: [host('nas', 'agent'), host('web1', 'agent'), host('vps', 'ssh')], port: 9101 });
      const m = url.match(/^\/api\/hosts\/(\w+)\/firewall(\/rules)?$/);
      if (m && init?.method === 'POST') {
        added.push({ id: m[1], rule: sent(init) });
        return json({ firewall: { tool: 'iptables', rules: [], listening: [] } });
      }
      if (m) return json({ firewall: { tool: 'iptables', rules: [], listening: [], open: [{ port: 9101, what: 'this agent' }] }, dashboard: '198.51.100.10' });
      return json({}, 404);
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <ServersPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Select' }));
    for (const name of ['nas', 'web1', 'vps']) fireEvent.click(screen.getByRole('checkbox', { name: new RegExp(`^${name}:`) }));
    // The SSH-only server has no firewall to manage: the button says how many it reaches.
    fireEvent.click(screen.getByRole('button', { name: 'Firewall (2)…' }));
    const panel = within(screen.getByRole('region', { name: 'Add firewall rules to the selected servers' }));
    fireEvent.click(panel.getByRole('button', { name: 'Add to 2 servers' }));
    await waitFor(() => expect(added).toHaveLength(6));
    expect(added.filter((a) => a.id === 'nas').map((a) => (a.rule as { port: string }).port)).toEqual(['80,443', '443', '3306,5432,6379,9200,11211,27017']);
    expect(added.some((a) => a.id === 'vps')).toBe(false);
    expect(await screen.findByRole('region', { name: 'Secure web results' })).toBeInTheDocument();

    // Only Nightjar on the agent's port: each server's own address for Nightjar, then a block.
    added.length = 0;
    fireEvent.click(screen.getByRole('button', { name: 'Firewall (2)…' }));
    const again = within(screen.getByRole('region', { name: 'Add firewall rules to the selected servers' }));
    fireEvent.click(again.getByRole('radio', { name: /Only Nightjar reaches the agent/ }));
    fireEvent.click(again.getByRole('button', { name: 'Add to 2 servers' }));
    await waitFor(() => expect(added).toHaveLength(4));
    expect(added.filter((a) => a.id === 'web1').map((a) => a.rule)).toEqual([
      { action: 'allow', source: '198.51.100.10', port: '9101', proto: 'tcp', note: 'Nightjar only' },
      { action: 'block', port: '9101', proto: 'tcp', note: 'everyone but Nightjar' },
    ]);
  });

  it('hides a new agent token as it is typed', async () => {
    const host = { id: 'h1', name: 'nas', address: 'https://nas:9101', added: 0, kind: 'agent', status: 'online', readings: null, history: [], problems: [] };
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => (String(input) === '/api/hosts/h1' ? json({ host }) : json({}, 404)));
    render(
      <MemoryRouter initialEntries={['/servers/h1']}>
        <ToastProvider>
          <ServicesProvider enabled>
            <Routes>
              <Route path="/servers/:id" element={<ServerDetail />} />
            </Routes>
          </ServicesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );
    const token = await screen.findByLabelText('New token');
    expect(token).toHaveAttribute('type', 'password');
    expect(token).toHaveAttribute('autocomplete', 'new-password');
  });
});

describe('system services', () => {
  const u = (name: string, over: Partial<Unit> = {}): Unit => ({ name: name + '.service', description: name + ' daemon', active: 'active', sub: 'running', enabled: 'enabled', restarts: 0, ...over });

  it('asks before restarting the monitor or a service much depends on', async () => {
    const units = [u('docker'), u('nightjar'), u('sonarr')];
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (init?.method === 'POST') return json({ unit: u(url.split('/')[3].replace(/\.service$/, '')) });
      return json({ enabled: true, self: 'nightjar.service', units });
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <SystemServicesPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    const row = async (name: string) => (await screen.findByText(name)).closest('li') as HTMLElement;
    const posts = () => fetchMock.mock.calls.filter(([, i]) => i?.method === 'POST').map(([url]) => url);

    // An ordinary service restarts at once.
    fireEvent.click(within(await row('sonarr')).getByRole('button', { name: 'Restart' }));
    expect(await screen.findByText('sonarr: restarted')).toBeInTheDocument();
    expect(posts()).toEqual(['/api/units/sonarr.service/restart']);

    for (const name of ['docker', 'nightjar']) {
      const r = await row(name);
      fireEvent.click(within(r).getByRole('button', { name: 'Restart' }));
      expect(within(r).getByRole('button', { name: 'Keep running' })).toBeInTheDocument();
      fireEvent.click(within(r).getByRole('button', { name: 'Keep running' }));
    }
    expect(posts()).toEqual(['/api/units/sonarr.service/restart']);

    const docker = await row('docker');
    fireEvent.click(within(docker).getByRole('button', { name: 'Restart' }));
    expect(within(docker).getByText(/Restart docker\?/)).toBeInTheDocument();
    fireEvent.click(within(docker).getByRole('button', { name: 'Restart' }));
    expect(await screen.findByText('docker: restarted')).toBeInTheDocument();
    expect(posts()).toEqual(['/api/units/sonarr.service/restart', '/api/units/docker.service/restart']);
  });

  // restartSelf restarts the monitor's own row with the given answer to the POST.
  const restartSelf = async (answer: () => Response) => {
    const reload = vi.fn();
    const real = window.location;
    Object.defineProperty(window, 'location', { configurable: true, value: { ...real, reload } });
    onTestFinished(() => {
      Object.defineProperty(window, 'location', { configurable: true, value: real });
    });
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      if (init?.method === 'POST') return answer();
      if (String(input) === '/api/config') return json({});
      return json({ enabled: true, self: 'nightjar.service', units: [u('nightjar')] });
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <SystemServicesPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    const r = (await screen.findByText('nightjar')).closest('li') as HTMLElement;
    fireEvent.click(within(r).getByRole('button', { name: 'Restart' }));
    fireEvent.click(within(r).getByRole('button', { name: 'Restart' }));
    return reload;
  };

  it('waits for the monitor to come back when its own restart cuts the answer short', async () => {
    // The restart kills the systemctl the monitor ran, which still gets its error out.
    const reload = await restartSelf(() => json({ error: 'systemctl restart: signal: terminated' }, 422));
    expect(await screen.findByText('Restarting nightjar')).toBeInTheDocument();
    expect(screen.queryByText('Could not restart nightjar')).not.toBeInTheDocument();
    await waitFor(() => expect(reload).toHaveBeenCalled(), { timeout: 4000 });
  });

  it("takes a proxy's bad gateway during the monitor's restart as the restart", async () => {
    const reload = await restartSelf(() => new Response('<html>502 Bad Gateway</html>', { status: 502 }));
    expect(await screen.findByText('Restarting nightjar')).toBeInTheDocument();
    await waitFor(() => expect(reload).toHaveBeenCalled(), { timeout: 4000 });
  });

  it('shows a refused restart of the monitor', async () => {
    const reload = await restartSelf(() => json({ error: 'systemctl restart: Access denied' }, 422));
    expect(await screen.findByText('Could not restart nightjar')).toBeInTheDocument();
    expect(screen.queryByText('Restarting nightjar')).not.toBeInTheDocument();
    expect(reload).not.toHaveBeenCalled();
  });
});
