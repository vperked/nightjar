import { fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, onUnauthorized, UnauthorizedError } from '../src/api/client';
import { ConfigContext } from '../src/api/ConfigContext';
import { NotificationsProvider } from '../src/api/NotificationsContext';
import { ServicesProvider, useServices } from '../src/api/ServicesContext';
import type { Notification, PlexStream, QBState, QBTorrent, Service, SettingField, SetupStatus, Unit, UpdateTarget, UpdatesResponse } from '../src/api/types';
import { UpdatesProvider } from '../src/api/UpdatesContext';
import { AccountSection, CreateAdmin, SignIn } from '../src/components/Account';
import { AccountMenu } from '../src/components/AccountMenu';
import { SectionsPanel } from '../src/components/Sections';
import { Bell } from '../src/components/Bell';
import { Finder } from '../src/components/Finder';
import { MuteButton } from '../src/components/MuteButton';
import { HealthInTab, useProblems } from '../src/components/HealthInTab';
import { Num } from '../src/components/Num';
import { NowPlaying } from '../src/components/PlexNow';
import { Select } from '../src/components/Select';
import { ToastProvider } from '../src/components/Toasts';
import { TrendChart } from '../src/components/TrendChart';
import { State, Ticks } from '../src/components/ui';
import { verdict as unitVerdict } from '../src/components/unitState';
import { formatBytes, formatClock, formatKbps, formatPercent } from '../src/format';
import { usePoll } from '../src/hooks/usePoll';
import { CheckForm } from '../src/pages/CheckForm';
import { NetworkPage } from '../src/pages/Network';
import { AppsPage } from '../src/pages/Apps';
import { LogsPage } from '../src/pages/Logs';
import { NotificationsPage } from '../src/pages/Notifications';
import { QBittorrentPage } from '../src/pages/QBittorrent';
import { group } from '../src/pages/qbittorrent/states';
import { ServiceRow, ServicesPage } from '../src/pages/Services';
import { SettingsPage } from '../src/pages/Settings';
import { SetupPage } from '../src/pages/Setup';
import { StatusDesignerPage } from '../src/pages/StatusDesigner';
import { StatusPage, StatusView } from '../src/pages/StatusPage';
import { SystemServicesPage } from '../src/pages/SystemServices';
import { UpdatesPage, verdict } from '../src/pages/Updates';

const svc = (over: Partial<Service>): Service => ({
  name: 'api',
  type: 'http',
  target: 'https://example.com',
  status: 'up',
  latency_ms: 120,
  detail: 'HTTP 200',
  last_check: 1000,
  last_change: null,
  uptime_24h: 99.5,
  uptime_7d: 99.9,
  uptime_30d: 99.95,
  recent: [],
  ...over,
});

afterEach(() => vi.restoreAllMocks());

describe('format', () => {
  it('formats bytes and percents', () => {
    expect(formatBytes(0)).toBe('0 B');
    expect(formatBytes(1536)).toBe('1.5 KB');
    expect(formatPercent(null)).toBe('--');
    expect(formatPercent(12.345)).toBe('12.3%');
  });
});

describe('Ticks', () => {
  it('pads to the slot count and marks failures', () => {
    const { container } = render(
      <Ticks
        beats={[
          { ts: 1, up: true },
          { ts: 2, up: false },
        ]}
        slots={10}
      />,
    );
    expect(container.querySelectorAll('.tick')).toHaveLength(10);
    expect(container.querySelectorAll('.tick.pad')).toHaveLength(8);
    expect(container.querySelectorAll('.tick.down')).toHaveLength(1);
    expect(screen.getByRole('img')).toHaveAccessibleName('Last 2 checks, 1 failed');
  });
});

describe('State', () => {
  it('always carries a word, not just a colour', () => {
    render(<State status="down" />);
    expect(screen.getByText('Down')).toBeInTheDocument();
  });
});

describe('ServiceRow', () => {
  it('renders names as text and missing uptime as --', () => {
    render(
      <MemoryRouter>
        <table>
          <tbody>
            <ServiceRow s={svc({ name: '<b>x</b>', uptime_24h: null })} now={1010} />
          </tbody>
        </table>
      </MemoryRouter>,
    );
    expect(screen.getByText('<b>x</b>')).toBeInTheDocument();
    expect(screen.getByText('--')).toBeInTheDocument();
    expect(screen.getByText('10s ago')).toBeInTheDocument();
  });
});

describe('TrendChart', () => {
  it('shows a placeholder until there are two points', () => {
    render(<TrendChart series={[{ label: 'x', color: 'red', points: [{ t: 1, v: 1 }] }]} format={String} />);
    expect(screen.getByText('Collecting data…')).toBeInTheDocument();
  });
});

describe('StatusPage', () => {
  it('shows the overall state from the public API', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ title: 'Acme status', overall: 'degraded', updated: 1, services: [{ name: 'web', status: 'down', uptime_24h: 90, uptime_30d: 97.5, recent: [] }] }), {
        status: 200,
      }),
    );
    render(<StatusPage />);
    await waitFor(() => expect(screen.getByText('Partial outage')).toBeInTheDocument());
    expect(screen.getByText('Acme status')).toBeInTheDocument();
    expect(screen.getByText('97.50%')).toBeInTheDocument();
  });
});

const stream = (over: Partial<PlexStream>): PlexStream => ({
  id: '1',
  user: 'sam',
  kind: 'movie',
  title: 'Heat',
  subtitle: '1995',
  player: 'Living Room',
  product: 'Plex for Roku',
  platform: 'Roku',
  state: 'playing',
  local: true,
  decision: 'direct play',
  hw_transcode: false,
  throttled: false,
  quality: '1080p',
  bitrate_kbps: 10000,
  bandwidth_kbps: 12000,
  duration_ms: 3_600_000,
  offset_ms: 1_800_000,
  progress: 50,
  started_at: 0,
  ...over,
});

describe('plex', () => {
  it('formats bandwidth and media positions', () => {
    expect(formatKbps(320)).toBe('320 kbps');
    expect(formatKbps(4500)).toBe('4.5 Mbps');
    expect(formatKbps(25000)).toBe('25 Mbps');
    expect(formatClock(65_000)).toBe('1:05');
    expect(formatClock(3_725_000)).toBe('1:02:05');
  });

  it('lists streams with how each is delivered', () => {
    render(<NowPlaying streams={[stream({}), stream({ id: '2', user: 'alex', decision: 'transcode', hw_transcode: true, state: 'paused', local: false })]} />);
    expect(screen.getByText('direct play')).toBeTruthy();
    expect(screen.getByText(/transcode/).textContent).toBe('transcode (hw)');
    expect(screen.getByText('paused')).toBeTruthy();
    expect(screen.getByText('remote')).toBeTruthy();
    expect(screen.getAllByText('12 Mbps')).toHaveLength(2);
  });

  it('says when nothing is playing', () => {
    render(<NowPlaying streams={[]} />);
    expect(screen.getByText('Nothing playing.')).toBeTruthy();
  });
});

describe('usePoll', () => {
  it('refetches when the fetcher changes and ignores replies to the old one', async () => {
    let resolveOld: (v: string) => void = () => {};
    const oldRange = () => new Promise<string>((r) => (resolveOld = r));
    const newRange = () => Promise.resolve('7d');
    const { result, rerender } = renderHook(({ f }) => usePoll(f, 60000), { initialProps: { f: oldRange } });
    rerender({ f: newRange });
    await waitFor(() => expect(result.current.data).toBe('7d'));
    resolveOld('24h');
    await new Promise((r) => setTimeout(r, 0));
    expect(result.current.data).toBe('7d');
  });
});

describe('api client', () => {
  it('reports any 401 to the registered handler', async () => {
    const handler = vi.fn();
    onUnauthorized(handler);
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('', { status: 401 }));
    await expect(api.services()).rejects.toBeInstanceOf(UnauthorizedError);
    expect(handler).toHaveBeenCalledOnce();
  });
});

describe('CheckForm', () => {
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const fill = () => {
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'web' } });
    fireEvent.change(screen.getByLabelText('Target'), { target: { value: 'https://example.com' } });
  };

  it('tests the target, then posts a new check with the http fields', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async (input) => (String(input) === '/api/checks/test' ? json({ result: { up: true, latency_ms: 42, detail: 'HTTP 200' } }) : json({ check: { name: 'web' } }, 201)));
    const onSaved = vi.fn();
    render(<CheckForm onSaved={onSaved} onCancel={() => {}} />);
    fill();
    fireEvent.change(screen.getByLabelText('Expected status codes'), { target: { value: '200, 204' } });
    fireEvent.click(screen.getByText('Add check'));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(fetchMock.mock.calls.map(([u]) => u)).toEqual(['/api/checks/test', '/api/checks']);
    const [, init] = fetchMock.mock.calls[1];
    expect(init?.method).toBe('POST');
    expect(JSON.parse(init?.body as string)).toMatchObject({ name: 'web', type: 'http', expected_status: [200, 204], interval: 60 });
  });

  it('does not save a check whose target does not answer until asked twice', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async (input) =>
        String(input) === '/api/checks/test' ? json({ result: { up: false, latency_ms: null, detail: 'connection refused' } }) : json({ check: { name: 'web' } }, 201),
      );
    const onSaved = vi.fn();
    render(<CheckForm onSaved={onSaved} onCancel={() => {}} />);
    fill();
    fireEvent.click(screen.getByText('Add check'));
    expect(await screen.findByText('Not reachable')).toBeInTheDocument();
    expect(screen.getByText(/did not answer: connection refused/)).toBeInTheDocument();
    expect(onSaved).not.toHaveBeenCalled();

    fireEvent.click(screen.getByText('Save anyway'));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(fetchMock.mock.calls.map(([u]) => u)).toEqual(['/api/checks/test', '/api/checks']);

    // Changing the target makes the old result stale.
    fireEvent.change(screen.getByLabelText('Target'), { target: { value: 'https://example.org' } });
    expect(screen.queryByText('Not reachable')).not.toBeInTheDocument();
  });

  it('paused checks save without a test', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(json({ check: { name: 'web', paused: true } }, 201));
    const onSaved = vi.fn();
    render(<CheckForm onSaved={onSaved} onCancel={() => {}} />);
    fill();
    fireEvent.click(screen.getByLabelText(/paused/));
    fireEvent.click(screen.getByText('Add check'));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(fetchMock.mock.calls.map(([u]) => u)).toEqual(['/api/checks']);
  });
});

describe('ServicesPage quick edit', () => {
  it('opens the form for a row without leaving the list', async () => {
    const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) =>
      String(input) === '/api/checks'
        ? json({
            checks: [
              {
                name: 'api',
                type: 'http',
                target: 'https://example.com',
                interval: 60,
                timeout: 10,
                retries: 2,
                expected_status: null,
                keyword: '',
                verify_tls: true,
                tls_warn_days: 14,
                paused: false,
              },
            ],
            types: [],
          })
        : json({ services: [svc({})] }),
    );
    render(
      <MemoryRouter>
        <ToastProvider>
          <ServicesProvider enabled>
            <ServicesPage />
          </ServicesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Edit api' }));
    expect(await screen.findByText('Edit api')).toBeInTheDocument();
    expect(screen.getByLabelText('Target')).toHaveValue('https://example.com');
  });
});

describe('SettingsPage', () => {
  it('shows secrets only as set and saves just the changed values', async () => {
    const field = (over: Partial<SettingField>): SettingField => ({ key: 'K', group: 'G', label: 'L', kind: 'text', default: '', value: '', set: false, source: 'default', ...over });
    const settings = {
      groups: [
        {
          name: 'Resources',
          fields: [
            field({ key: 'CPU_THRESHOLD', label: 'CPU', kind: 'number', value: '80', default: '80', unit: '%' }),
            field({ key: 'PLEX_TOKEN', label: 'Token', kind: 'secret', set: true, source: 'environment' }),
            field({ key: 'WEB_PORT', label: 'Port', kind: 'int', value: '8080', startup: true }),
          ],
        },
      ],
    };
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockResolvedValueOnce(new Response(JSON.stringify(settings)))
      .mockResolvedValueOnce(new Response(JSON.stringify({ error: 'Some settings are invalid', fields: { CPU_THRESHOLD: 'must be at most 100' } }), { status: 400 }));
    render(
      <MemoryRouter>
        <SettingsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByPlaceholderText(/set — type to replace/)).toHaveValue('');
    expect(screen.getByText('set in .env', { exact: false })).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('CPU'), { target: { value: '150' } });
    fireEvent.click(screen.getByText('Save and restart'));
    expect(await screen.findByText('must be at most 100')).toBeInTheDocument();
    expect(JSON.parse(fetchMock.mock.calls[1][1]?.body as string)).toEqual({ values: { CPU_THRESHOLD: '150' } });
  });
});

describe('demo data', () => {
  it('answers the API and keeps edits', async () => {
    const { demoFetch } = await import('../src/api/demo');
    const get = async (path: string) => (await demoFetch(path)).json();
    const { services } = await get('/api/services');
    expect(services.length).toBeGreaterThan(3);
    expect(services.some((s: Service) => s.status === 'down')).toBe(true);
    const stats = await get('/api/stats');
    expect(stats.history).toHaveLength(360);
    const created = await demoFetch('/api/checks', { method: 'POST', body: JSON.stringify({ name: 'grafana', type: 'http', target: 'http://10.0.0.40:3000' }) });
    expect(created.status).toBe(201);
    expect((await get('/api/checks')).checks.map((c: { name: string }) => c.name)).toContain('grafana');
    const bad = await demoFetch('/api/settings', { method: 'PUT', body: JSON.stringify({ values: { CPU_THRESHOLD: '150' } }) });
    expect(bad.status).toBe(400);
  });
});

describe('SettingsPage on an older backend', () => {
  it('explains that the backend needs a restart', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ error: 'Not Found' }), { status: 404 }));
    render(
      <MemoryRouter>
        <SettingsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText(/older than this dashboard/)).toBeInTheDocument();
  });
});

describe('toasts', () => {
  function Refresh() {
    const { refresh } = useServices();
    return <button onClick={refresh}>refresh</button>;
  }

  it('announce a check going down while the dashboard is open, but not on first load', async () => {
    const reply = (status: string) => new Response(JSON.stringify({ services: [svc({ name: 'api', status: status as Service['status'], detail: 'timed out after 10s' })] }));
    vi.spyOn(globalThis, 'fetch').mockResolvedValueOnce(reply('down')).mockResolvedValueOnce(reply('up')).mockResolvedValueOnce(reply('down'));
    render(
      <ToastProvider>
        <ServicesProvider enabled>
          <Refresh />
        </ServicesProvider>
      </ToastProvider>,
    );
    await waitFor(() => expect(globalThis.fetch).toHaveBeenCalledTimes(1));
    expect(screen.queryByText('api is down')).not.toBeInTheDocument();
    fireEvent.click(screen.getByText('refresh'));
    expect(await screen.findByText('api is back up')).toBeInTheDocument();
    fireEvent.click(screen.getByText('refresh'));
    expect(await screen.findByText('api is down')).toBeInTheDocument();
    fireEvent.click(screen.getAllByLabelText('Dismiss')[1]);
    await waitFor(() => expect(screen.queryByText('api is down')).not.toBeInTheDocument());
  });
});

describe('TrendChart hover', () => {
  it('shows the value nearest the pointer', async () => {
    const points = Array.from({ length: 100 }, (_, i) => ({ t: 1000 + i * 60, v: i }));
    const { container } = render(<TrendChart series={[{ label: 'ms', color: 'red', points }]} format={(v) => `${v}ms`} />);
    const chart = container.querySelector('.chart') as HTMLElement;
    vi.spyOn(chart, 'getBoundingClientRect').mockReturnValue({ left: 0, width: 640, top: 0, height: 180, right: 640, bottom: 180, x: 0, y: 0, toJSON: () => ({}) });
    fireEvent.pointerMove(chart, { clientX: 640 });
    expect(await screen.findByText('99ms')).toBeInTheDocument();
  });
});

describe('updates', () => {
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
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

  it('names every state in words', () => {
    expect(verdict(t({ available: true }))).toEqual(['warn', 'Update available']);
    expect(verdict(t({ busy: 'updating' }))[1]).toBe('Updating…');
    expect(verdict(t({ error: 'refused' }))).toEqual(['crit', 'Check failed']);
    expect(verdict(t({ available: true, runs: [{ at: 2, ok: false, auto: true }] }))[1]).toBe('Update failed');
    expect(verdict(t({ checked: 0 }))[1]).toBe('Not checked');
    expect(verdict(t({}))).toEqual(['good', 'Up to date']);
  });

  it('lists targets, installs an update and turns on auto-update', async () => {
    const sonarr = t({ installed: '4.0.14', latest: '4.0.15', available: true });
    const qb = t({ id: 'qb', name: 'qBittorrent', kind: 'release', repo: 'qbittorrent/qBittorrent', can_update: false, installed: '5.1.4', latest: '5.1.4' });
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/updates' && !init?.method?.match(/POST/)) return json(page([sonarr, qb]));
      if (url === '/api/updates/sonarr/update') return json({ queued: true }, 202);
      if (url === '/api/updates/sonarr') return json({ target: { ...sonarr, auto: true } });
      return json({});
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <UpdatesProvider enabled>
            <UpdatesPage />
          </UpdatesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText('4.0.14 → 4.0.15')).toBeInTheDocument();
    expect(screen.getByText('Update available')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Update' })).toHaveLength(1); // qBittorrent is check-only
    expect(screen.getByRole('switch', { name: 'Update qBittorrent automatically' })).toBeDisabled();

    fireEvent.click(screen.getByRole('button', { name: 'Update' }));
    expect(await screen.findByText('Updating Sonarr')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('switch', { name: 'Update Sonarr automatically' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u, i]) => u === '/api/updates/sonarr' && i?.method === 'PUT')).toBe(true));
    const put = fetchMock.mock.calls.find(([u, i]) => u === '/api/updates/sonarr' && i?.method === 'PUT')!;
    const sent = JSON.parse(put[1]?.body as string);
    expect(sent).toMatchObject({ id: 'sonarr', auto: true });
    expect(sent).not.toHaveProperty('installed'); // only the definition is saved
  });

  it('demo data updates a target', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const { demoFetch } = await import('../src/api/demo');
    const get = async () => (await (await demoFetch('/api/updates')).json()) as UpdatesResponse;
    const adguard = (await get()).targets.find((x) => x.id === 'adguard-home')!;
    expect(adguard.available).toBe(true);
    expect((await demoFetch('/api/updates/adguard-home/update', { method: 'POST', body: '{}' })).status).toBe(202);
    await vi.advanceTimersByTimeAsync(4000);
    const after = (await get()).targets.find((x) => x.id === 'adguard-home')!;
    expect(after.available).toBe(false);
    expect(after.runs?.[0]).toMatchObject({ ok: true, to: 'v0.107.80' });
    vi.useRealTimers();
  });
});

describe('qBittorrent', () => {
  const torrent = (over: Partial<QBTorrent>): QBTorrent => ({
    hash: 'h',
    name: 'x',
    size: 1024 ** 3,
    progress: 1,
    state: 'uploading',
    dlspeed: 0,
    upspeed: 0,
    ratio: 1,
    eta: 8640000,
    num_seeds: 1,
    num_leechs: 0,
    added_on: 0,
    completion_on: 0,
    category: '',
    save_path: '/d',
    downloaded: 0,
    uploaded: 0,
    ...over,
  });
  const state = (over: Partial<QBState>): QBState => ({ connected: true, version: 'v5.1.4', url: 'http://localhost:10914', updated: 100, transfer: null, torrents: [], history: [], ...over });
  const serve = (q: QBState) => vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ enabled: true, qbittorrent: q }), { headers: { 'Content-Type': 'application/json' } }));
  const page = () =>
    render(
      <MemoryRouter>
        <QBittorrentPage />
      </MemoryRouter>,
    );

  it('groups states the way a person reads them', () => {
    expect(group('stalleddl')).toBe('stalled');
    expect(group('metadl')).toBe('downloading');
    expect(group('stalledup')).toBe('seeding');
    expect(group('stoppedup')).toBe('paused');
    expect(group('missingfiles')).toBe('error');
  });

  it('puts stalled torrents first, says for how long, and filters', async () => {
    serve(
      state({
        torrents: [
          torrent({ hash: '1', name: 'seeding.iso', state: 'uploading', upspeed: 2048 }),
          torrent({ hash: '2', name: 'stuck.mkv', state: 'stalleddl', progress: 0.5, stalled_for: 1260, recoveries: 1 }),
          torrent({ hash: '3', name: 'active.iso', state: 'downloading', progress: 0.25, dlspeed: 5 * 1024 ** 2, eta: 600 }),
        ],
      }),
    );
    page();
    expect(await screen.findByText('stuck.mkv')).toBeInTheDocument();
    const names = screen
      .getAllByRole('row')
      .slice(1)
      .map((r) => r.querySelector('.truncate')?.textContent);
    expect(names).toEqual(['stuck.mkv', 'active.iso', 'seeding.iso']);
    expect(screen.getByText(/stalled 21m 0s, 1 recovery try/)).toBeInTheDocument();
    expect(screen.getByText(/10m 0s left/)).toBeInTheDocument();

    fireEvent.click(within(screen.getByRole('group', { name: 'Filter torrents' })).getByRole('button', { name: /^seeding/ }));
    expect(screen.queryByText('stuck.mkv')).not.toBeInTheDocument();
    expect(screen.getByText('seeding.iso')).toBeInTheDocument();
  });

  const manage = (torrents: QBTorrent[]) => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = String(input);
      const body = url === '/api/qbittorrent/categories' ? { categories: ['movies', 'tv'] } : url === '/api/qbittorrent/add' ? { added: 1 } : { enabled: true, qbittorrent: state({ torrents }) };
      return new Response(JSON.stringify(body), { status: url.endsWith('/add') ? 201 : 200, headers: { 'Content-Type': 'application/json' } });
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <QBittorrentPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    const posted = (path: string) => fetchMock.mock.calls.filter(([u, i]) => u === path && i?.method === 'POST').map(([, i]) => JSON.parse(i?.body as string));
    return posted;
  };
  const h = (c: string) => c.repeat(40);

  it('stops a selection of torrents at once', async () => {
    const posted = manage([
      torrent({ hash: h('a'), name: 'one.iso', state: 'downloading' }),
      torrent({ hash: h('b'), name: 'two.iso', state: 'uploading' }),
      torrent({ hash: h('c'), name: 'three.iso' }),
    ]);
    fireEvent.click(await screen.findByLabelText('Select one.iso'));
    fireEvent.click(screen.getByLabelText('Select two.iso'));
    const all = screen.getByLabelText('Select all shown') as HTMLInputElement;
    expect(all.indeterminate).toBe(true); // two of three: partly selected
    expect(all.checked).toBe(false);
    const bar = screen.getByRole('toolbar', { name: 'Selected torrents' });
    expect(bar).toHaveTextContent('2 selected');
    fireEvent.click(within(bar).getByText('stop'));
    await waitFor(() => expect(posted('/api/qbittorrent/action')).toEqual([{ action: 'stop', hashes: [h('a'), h('b')], delete_files: false }]));
    expect(await screen.findByText('Stopped 2 torrents')).toBeInTheDocument();
  });

  it('asks before deleting, and whether to keep the files', async () => {
    const posted = manage([torrent({ hash: h('a'), name: 'old.iso', state: 'stoppedup' })]);
    fireEvent.click(await screen.findByRole('button', { name: 'old.iso' }));
    expect(screen.getByText(h('a'))).toBeInTheDocument(); // details show the hash
    fireEvent.click(screen.getByText('delete…'));
    expect(posted('/api/qbittorrent/action')).toEqual([]);
    expect(screen.getByText('Delete this torrent?')).toBeInTheDocument();
    fireEvent.click(screen.getByText('delete files too'));
    await waitFor(() => expect(posted('/api/qbittorrent/action')).toEqual([{ action: 'delete', hashes: [h('a')], delete_files: true }]));
  });

  it('adds magnet links into a category', async () => {
    const posted = manage([]);
    fireEvent.click(await screen.findByText('Add torrent'));
    fireEvent.change(screen.getByLabelText('Magnet links or URLs'), { target: { value: 'magnet:?xt=urn:btih:abc' } });
    fireEvent.click(screen.getByRole('combobox', { name: 'Category' }));
    fireEvent.mouseDown(await screen.findByRole('option', { name: 'tv' }));
    expect(screen.getByRole('combobox', { name: 'Category' })).toHaveTextContent('tv');
    fireEvent.click(screen.getByLabelText(/add stopped/));
    fireEvent.click(screen.getByRole('button', { name: 'Add' }));
    await waitFor(() => expect(posted('/api/qbittorrent/add')).toEqual([{ urls: 'magnet:?xt=urn:btih:abc', category: 'tv', stopped: true }]));
    expect(await screen.findByText('Added 1 torrent')).toBeInTheDocument();
  });

  it('demo data stops and deletes torrents', async () => {
    const { demoFetch } = await import('../src/api/demo');
    const list = async () => ((await (await demoFetch('/api/qbittorrent')).json()) as { qbittorrent: QBState }).qbittorrent.torrents;
    const first = (await list())[0];
    await demoFetch('/api/qbittorrent/action', { method: 'POST', body: JSON.stringify({ action: 'stop', hashes: [first.hash] }) });
    expect((await list()).find((x) => x.hash === first.hash)?.state).toBe('stoppeddl');
    await demoFetch('/api/qbittorrent/action', { method: 'POST', body: JSON.stringify({ action: 'delete', hashes: [first.hash] }) });
    expect((await list()).some((x) => x.hash === first.hash)).toBe(false);
  });

  it('says when qBittorrent stops answering but keeps the last list', async () => {
    serve(state({ connected: false, error: 'connection refused', torrents: [torrent({ name: 'kept.iso' })] }));
    page();
    expect(await screen.findByText(/qBittorrent is unreachable: connection refused/)).toBeInTheDocument();
    expect(screen.getByText('kept.iso')).toBeInTheDocument();
  });
});

describe('easy configuration', () => {
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const field = (over: Partial<SettingField>): SettingField => ({ key: 'K', group: 'G', label: 'L', kind: 'text', default: '', value: '', set: false, source: 'default', ...over });
  const settings = {
    groups: [
      {
        name: 'Resources',
        fields: [
          field({ key: 'CPU_THRESHOLD', label: 'CPU', kind: 'number', value: '80', live: true }),
          field({ key: 'PROCESS_CPU_THRESHOLD', label: 'Process CPU', kind: 'number', value: '50', live: true, advanced: true }),
        ],
      },
      { name: 'Plex', fields: [field({ key: 'PLEX_URL', label: 'Server URL', value: 'http://localhost:32400' })] },
    ],
  };
  afterEach(() => localStorage.clear());

  it('folds advanced settings away until asked or searched for', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(json(settings));
    render(
      <MemoryRouter>
        <SettingsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByLabelText('CPU')).toBeInTheDocument();
    expect(screen.queryByLabelText('Process CPU')).not.toBeInTheDocument();
    expect(screen.getByText(/advanced \(1 more\)/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Find a setting'), { target: { value: 'process' } });
    expect(screen.getByLabelText('Process CPU')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Find a setting'), { target: { value: '' } });
    fireEvent.click(screen.getByLabelText(/advanced/));
    expect(screen.getByLabelText('Process CPU')).toBeInTheDocument();
  });

  it('saves live settings without a restart and says which ones restart', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (_input, init) => (init?.method === 'PUT' ? json({ restarting: false, applied: true }) : json(settings)));
    render(
      <MemoryRouter>
        <ToastProvider>
          <SettingsPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText('CPU'), { target: { value: '90' } });
    expect(screen.getByText(/applies at once/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Server URL'), { target: { value: 'http://nas:32400' } });
    expect(screen.getByText(/saving restarts the monitor \(Server URL\)/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Server URL'), { target: { value: 'http://localhost:32400' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save and restart' }));
    expect(await screen.findByText('Settings saved')).toBeInTheDocument();
    expect(fetchMock.mock.calls.filter(([, i]) => i?.method === 'PUT')).toHaveLength(1);
    // The test button sends what is on screen, saved or not.
    fireEvent.change(screen.getByLabelText('Server URL'), { target: { value: 'http://nas:32400' } });
    fetchMock.mockImplementation(async () => json({ ok: true, detail: 'Connected to nas (Plex 1.43)' }));
    fireEvent.click(screen.getByRole('button', { name: 'Test connection' }));
    expect(await screen.findByText('Plex connection works')).toBeInTheDocument();
    const testCall = fetchMock.mock.calls.find(([u]) => u === '/api/settings/test')!;
    expect(JSON.parse(testCall[1]?.body as string)).toEqual({ target: 'plex', values: { PLEX_URL: 'http://nas:32400' } });
  });

  const status = (over: Partial<SetupStatus> = {}): SetupStatus => ({
    alerts: { configured: false, channels: null, webhook_set: false },
    plex: { enabled: false, url: 'http://localhost:32400', token_set: false, detected: { running: true, url: 'http://127.0.0.1:32400', version: '1.43.5', token_found: true } },
    qbittorrent: {
      enabled: false,
      host: 'localhost',
      port: 8080,
      username: 'admin',
      password_set: false,
      detected: { found: true, host: 'localhost', port: 10914, username: 'media', local_bypass: false, answering: true },
    },
    checks: { count: 0, suggestions: [{ name: 'Sonarr', type: 'http', target: 'http://127.0.0.1:8989/ping' }] },
    updates: { enabled: false, targets: 12, window: '04:00–06:00' },
    ...over,
  });

  it('guides setup with what it found, testing before saving', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/setup') return json(status());
      if (url === '/api/settings/test') return json({ ok: true, detail: 'Connected' });
      if (url === '/api/checks') return json({ check: {} }, 201);
      if (init?.method === 'PUT') return json({ restarting: false, applied: true });
      return json({});
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <SetupPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText(/Plex 1.43.5 is running here/)).toBeInTheDocument();
    expect(screen.getByLabelText('0 of 5 set up')).toBeInTheDocument();
    // qBittorrent's detected port and user are filled in.
    expect(screen.getByDisplayValue('10914')).toBeInTheDocument();
    expect(screen.getByDisplayValue('media')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Watch Plex' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([, i]) => i?.method === 'PUT')).toBe(true));
    const calls = fetchMock.mock.calls.map(([u, i]) => [u, i?.body ? JSON.parse(i.body as string) : undefined]);
    const testIdx = calls.findIndex(([u]) => u === '/api/settings/test');
    const saveIdx = calls.findIndex(([u, b]) => u === '/api/settings' && b);
    expect(testIdx).toBeGreaterThan(-1);
    expect(testIdx).toBeLessThan(saveIdx);
    expect(calls[saveIdx][1]).toEqual({ values: { PLEX_URL: 'http://127.0.0.1:32400', PLEX_TOKEN: '@detected', PLEX_MONITOR: 'true' } });

    fireEvent.click(screen.getByRole('button', { name: 'Add 1 check' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u, i]) => u === '/api/checks' && i?.method === 'POST')).toBe(true));
    const added = fetchMock.mock.calls.find(([u, i]) => u === '/api/checks' && i?.method === 'POST')!;
    expect(JSON.parse(added[1]?.body as string)).toMatchObject({ name: 'Sonarr', type: 'http', target: 'http://127.0.0.1:8989/ping' });
  });

  it('does not save a connection that fails its test', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async (input) => (String(input) === '/api/settings/test' ? json({ ok: false, detail: 'login failed (HTTP 200)' }) : json(status())));
    render(
      <MemoryRouter>
        <ToastProvider>
          <SetupPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText(/^Password/), { target: { value: 'wrong' } });
    fireEvent.click(screen.getByRole('button', { name: 'Watch qBittorrent' }));
    expect(await screen.findByText('Could not connect to qBittorrent')).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([, i]) => i?.method === 'PUT')).toBe(false);
  });
});

describe('system services', () => {
  const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  const u = (name: string, over: Partial<Unit> = {}): Unit => ({ name: name + '.service', description: name + ' daemon', active: 'active', sub: 'running', enabled: 'enabled', restarts: 0, ...over });

  it('names every state in words', () => {
    expect(unitVerdict(u('a', { active: 'failed', sub: 'failed' }))).toEqual(['crit', 'Failed']);
    expect(unitVerdict(u('a', { active: 'activating', sub: 'auto-restart' }))[1]).toBe('Restarting');
    expect(unitVerdict(u('a', { active: 'failed', ignored: true }))[1]).toBe('Failed (ignored)');
    expect(unitVerdict(u('a', { flapping: 4 }))).toEqual(['warn', 'Unstable']);
    expect(unitVerdict(u('a', { sub: 'exited' }))[1]).toBe('Done');
    expect(unitVerdict(u('a', { active: 'inactive', sub: 'dead' }))[1]).toBe('Stopped');
  });

  it('shows logs, restarts a failed service, and asks before stopping', async () => {
    const units = [u('bazarr', { active: 'failed', sub: 'failed', result: 'exit-code', exit_status: 1 }), u('sonarr'), u('server-monitor')];
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/logs')) return json({ lines: ['bazarr[1]: database disk image is malformed'] });
      if (init?.method === 'POST') return json({ unit: u(url.split('/')[3]) });
      return json({ enabled: true, self: 'server-monitor.service', units });
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <SystemServicesPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText('Failed')).toBeInTheDocument();
    expect(screen.getByText(/exit-code \(exit 1\)/)).toBeInTheDocument();
    fireEvent.click(within(screen.getByRole('group', { name: 'Filter services' })).getByRole('button', { name: /problems/ }));
    expect(screen.queryByText('sonarr')).not.toBeInTheDocument();
    fireEvent.click(within(screen.getByRole('group', { name: 'Filter services' })).getByRole('button', { name: /all/ }));

    fireEvent.click(screen.getByText('bazarr'));
    expect(await screen.findByText(/database disk image is malformed/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Start' }));
    expect(await screen.findByText('bazarr: started')).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url, i]) => url === '/api/units/bazarr.service/start' && i?.method === 'POST')).toBe(true);

    fireEvent.click(screen.getByText('sonarr'));
    fireEvent.click(await screen.findByText('stop…'));
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/stop'))).toBe(false);
    fireEvent.click(screen.getByRole('button', { name: 'stop' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, i]) => url === '/api/units/sonarr.service/stop' && i?.method === 'POST')).toBe(true));

    fireEvent.click(screen.getByText('server-monitor'));
    await screen.findAllByText(/Recent log/);
    expect(screen.queryByText('stop…')).not.toBeInTheDocument(); // the monitor cannot stop itself from here
  });

  it('opens the service a link points at', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) =>
      String(input).includes('/logs')
        ? json({ lines: ['tautulli: ModuleNotFoundError'] })
        : json({ enabled: true, units: [u('sonarr'), u('tautulli', { active: 'activating', sub: 'auto-restart', restarts: 5 })] }),
    );
    render(
      <MemoryRouter initialEntries={['/system?open=tautulli.service']}>
        <ToastProvider>
          <SystemServicesPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText(/ModuleNotFoundError/)).toBeInTheDocument();
    expect(screen.getAllByRole('button', { expanded: true })[0]).toHaveTextContent('tautulli');
  });

  it('demo data has services and drives', async () => {
    const { demoFetch } = await import('../src/api/demo');
    const units = await (await demoFetch('/api/units')).json();
    expect(units.units.some((x: Unit) => x.active === 'failed' && !x.ignored)).toBe(true);
    const drives = await (await demoFetch('/api/drives')).json();
    expect(drives.drives.map((d: { health: string }) => d.health)).toEqual(['warning', 'good']);
    const restarted = await (await demoFetch('/api/units/bazarr.service/restart', { method: 'POST', body: '{}' })).json();
    expect(restarted.unit.active).toBe('active');
  });
});

describe('Select', () => {
  it('works with the keyboard and the mouse', () => {
    const onChange = vi.fn();
    const options = ['INFO', 'WARNING', 'CRITICAL'].map((v) => ({ value: v, label: v }));
    render(<Select label="Severity" value="INFO" options={options} onChange={onChange} />);
    const box = screen.getByRole('combobox', { name: 'Severity' });
    expect(box).toHaveTextContent('INFO');
    expect(box).toHaveAttribute('aria-expanded', 'false');
    fireEvent.keyDown(box, { key: 'ArrowDown' });
    expect(box).toHaveAttribute('aria-expanded', 'true');
    expect(screen.getByRole('option', { name: 'INFO' })).toHaveAttribute('aria-selected', 'true');
    fireEvent.keyDown(box, { key: 'ArrowDown' });
    fireEvent.keyDown(box, { key: 'Enter' });
    expect(onChange).toHaveBeenLastCalledWith('WARNING');
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
    // Typing a letter jumps without opening; Escape closes without choosing.
    fireEvent.keyDown(box, { key: 'c' });
    expect(onChange).toHaveBeenLastCalledWith('CRITICAL');
    fireEvent.click(box);
    fireEvent.keyDown(box, { key: 'End' });
    fireEvent.keyDown(box, { key: 'Escape' });
    expect(onChange).toHaveBeenCalledTimes(2);
    fireEvent.click(box);
    fireEvent.mouseDown(screen.getByRole('option', { name: 'WARNING' }));
    expect(onChange).toHaveBeenLastCalledWith('WARNING');
    // A click elsewhere closes it.
    fireEvent.click(box);
    fireEvent.mouseDown(document.body);
    expect(screen.queryByRole('listbox')).not.toBeInTheDocument();
  });
});

describe('easy to use', () => {
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  afterEach(() => sessionStorage.clear());

  function Where() {
    const loc = useLocation();
    return <div data-testid="where">{loc.pathname + loc.search}</div>;
  }

  it('the finder jumps to checks, services and settings from the keyboard', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = String(input);
      if (url === '/api/services') return json({ services: [svc({ name: 'website' })] });
      if (url === '/api/units') return json({ enabled: true, units: [{ name: 'sonarr.service', description: 'Sonarr Daemon', active: 'active', sub: 'running', restarts: 0 }] });
      if (url === '/api/settings')
        return json({
          groups: [{ name: 'Resources', fields: [{ key: 'CPU_THRESHOLD', group: 'Resources', label: 'CPU', kind: 'number', default: '80', value: '80', set: true, source: 'default' }] }],
        });
      return json({});
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <ServicesProvider enabled>
            <Finder onToggleTheme={() => {}} />
            <Routes>
              <Route path="*" element={<Where />} />
            </Routes>
          </ServicesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );
    fireEvent.keyDown(window, { key: 'k', ctrlKey: true });
    const input = await screen.findByRole('combobox', { name: /Find a page/ });
    fireEvent.change(input, { target: { value: 'sonarr' } });
    expect(await screen.findByText('Sonarr Daemon', { exact: false })).toBeInTheDocument();
    fireEvent.keyDown(input, { key: 'Enter' });
    expect(screen.getByTestId('where')).toHaveTextContent('/system?open=sonarr.service');
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();

    // "/" opens it too; a setting opens Settings searched for it.
    fireEvent.keyDown(window, { key: '/' });
    fireEvent.change(await screen.findByRole('combobox', { name: /Find a page/ }), { target: { value: 'cpu' } });
    fireEvent.click(await screen.findByText('CPU'));
    expect(screen.getByTestId('where')).toHaveTextContent('/settings?find=CPU_THRESHOLD');
    fireEvent.keyDown(window, { key: 'k', metaKey: true });
    fireEvent.keyDown(await screen.findByRole('combobox', { name: /Find a page/ }), { key: 'Escape' });
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('keeps unsaved settings when leaving the page, but never secrets', async () => {
    const field = (over: Partial<SettingField>): SettingField => ({ key: 'K', group: 'G', label: 'L', kind: 'text', default: '', value: '', set: false, source: 'default', live: true, ...over });
    const settings = {
      groups: [{ name: 'Resources', fields: [field({ key: 'CPU_THRESHOLD', label: 'CPU', kind: 'number', value: '80' }), field({ key: 'PLEX_TOKEN', label: 'Token', kind: 'secret' })] }],
    };
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => json(settings));
    const page = () =>
      render(
        <MemoryRouter>
          <SettingsPage />
        </MemoryRouter>,
      );
    const first = page();
    fireEvent.change(await screen.findByLabelText('CPU'), { target: { value: '93' } });
    fireEvent.change(screen.getByLabelText('Token'), { target: { value: 'secret-value' } });
    first.unmount();
    page();
    expect(await screen.findByLabelText('CPU')).toHaveValue(93);
    expect(screen.getByLabelText('Token')).toHaveValue('');
    expect(sessionStorage.getItem('servermonitor.settings.draft')).not.toContain('secret-value');
    expect(screen.getByRole('region', { name: 'Unsaved changes' })).toHaveTextContent('1 unsaved change');
  });

  it('updates everything available at once, the monitor last', async () => {
    const t = (id: string, over: Partial<UpdateTarget> = {}): UpdateTarget => ({
      id,
      name: id,
      kind: 'release',
      auto: false,
      paused: false,
      available: true,
      can_update: true,
      checked: 1,
      installed: '1',
      latest: '2',
      ...over,
    });
    const targets = [t('servermonitor', { kind: 'git', self: true }), t('sonarr'), t('qbittorrent', { can_update: false })];
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async (_input, init) =>
        init?.method === 'POST'
          ? json({ queued: true }, 202)
          : json({ enabled: true, interval: 1, window: { start: 4, end: 6, label: '04:00–06:00' }, commands_editable: false, kinds: [], checking: false, last_check: 1, targets }),
      );
    render(
      <MemoryRouter>
        <ToastProvider>
          <UpdatesProvider enabled>
            <UpdatesPage />
          </UpdatesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByText('update all (2)'));
    expect(screen.getByText(/the monitor restarts last/)).toBeInTheDocument();
    fireEvent.click(screen.getByText('yes, update all'));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([, i]) => i?.method === 'POST')).toHaveLength(2));
    expect(fetchMock.mock.calls.filter(([, i]) => i?.method === 'POST').map(([u]) => u)).toEqual(['/api/updates/sonarr/update', '/api/updates/servermonitor/update']);
  });

  it('Escape closes a form', () => {
    const onCancel = vi.fn();
    render(<CheckForm onSaved={() => {}} onCancel={onCancel} />);
    fireEvent.keyDown(screen.getByLabelText('Name'), { key: 'Escape' });
    expect(onCancel).toHaveBeenCalled();
  });
});

describe('network', () => {
  it('shows live rates, link use, and who is using it, asking only for new samples', async () => {
    const nic = (name: string, over = {}) => ({
      name,
      up: true,
      default: false,
      virtual: false,
      rx_bps: 0,
      tx_bps: 0,
      rx_pps: 0,
      tx_pps: 0,
      rx_peak: 0,
      tx_peak: 0,
      rx_avg: 0,
      tx_avg: 0,
      rx_total: 0,
      tx_total: 0,
      errors: 0,
      drops: 0,
      ...over,
    });
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(
      async () =>
        new Response(
          JSON.stringify({
            enabled: true,
            history: [],
            network: {
              interfaces: [nic('eth0', { default: true, speed_mbps: 1000, rx_bps: 12_500_000, tx_bps: 1_250_000, rx_total: 5 * 1024 ** 3 }), nic('wg0', { virtual: true, rx_bps: 2048 })],
              samples: {
                eth0: [
                  { ts: 100, rx: 1, tx: 2 },
                  { ts: 101, rx: 3, tx: 4 },
                ],
                wg0: [{ ts: 101, rx: 5, tx: 6 }],
              },
              processes: [{ name: 'qBittorrent', unit: 'qbittorrent@media', connections: 40, rx_bps: 9_000_000, tx_bps: 500_000, source: 'qbittorrent' }],
              hosts: [{ peer: '198.51.100.7', process: 'qbittorrent-nox', connections: 1, rx_bps: 1000, tx_bps: 10 }],
              connections: 41,
              since: 90,
            },
          }),
          { headers: { 'Content-Type': 'application/json' } },
        ),
    );
    render(
      <MemoryRouter>
        <NetworkPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText('100 Mbit/s')).toBeInTheDocument(); // 12.5 MB/s down
    expect(screen.getAllByText('10% of link')).toHaveLength(1);
    expect(screen.getByText(/speeds from qBittorrent \(includes uTP\)/)).toBeInTheDocument();
    expect(screen.getByText('198.51.100.7')).toBeInTheDocument();
    await waitFor(() => expect(fetchMock.mock.calls.length).toBeGreaterThan(1), { timeout: 2500 });
    expect(String(fetchMock.mock.calls[1][0])).toBe('/api/network?since=101');
    fireEvent.click(screen.getByRole('button', { name: /^wg0/ }));
    expect(screen.getByRole('button', { name: /^wg0/ })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByText(/wg0/, { selector: '.page-sub span' })).toBeInTheDocument();
  });
});

describe('status page design', () => {
  const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });

  it('shows the announcement, group headings and only what the design allows', () => {
    render(
      <StatusView
        data={{
          title: 'Perked status',
          overall: 'degraded',
          updated: 1000,
          description: 'Media and home services.',
          show_uptime: false,
          show_history: false,
          announcement: { text: 'Plex maintenance tonight', tone: 'warning' },
          groups: [{ name: 'Media', services: [{ name: 'Plex', status: 'down', uptime_24h: null, uptime_30d: null, recent: [] }] }],
          services: [{ name: 'Plex', status: 'down', uptime_24h: null, uptime_30d: null, recent: [] }],
        }}
      />,
    );
    expect(screen.getByRole('note')).toHaveTextContent('Plex maintenance tonight');
    expect(screen.getByRole('heading', { name: 'Media' })).toBeInTheDocument();
    expect(screen.getByText('Partial outage')).toBeInTheDocument();
    expect(screen.queryByText('30d')).not.toBeInTheDocument();
    expect(screen.queryByRole('img')).not.toBeInTheDocument(); // no recent-check bar
  });

  it('designs the page with a live preview and saves it', async () => {
    const design = { title: 'Status', description: '', announcement: '', tone: 'info', show_uptime: true, show_history: true, footer: '', groups: [{ name: '', items: [{ check: 'website' }] }] };
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/statuspage' && init?.method === 'PUT') return json({ design: JSON.parse(init.body as string) });
      if (url === '/api/statuspage') return json({ enabled: true, designed: false, design, checks: ['website', 'plex', 'secret-db'] });
      if (url === '/api/services') return json({ services: [svc({ name: 'website' }), svc({ name: 'plex', status: 'down' }), svc({ name: 'secret-db' })] });
      return json({});
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <ServicesProvider enabled>
            <StatusDesignerPage />
          </ServicesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText('Public name for website'), { target: { value: 'Our website' } });
    const preview = screen.getByLabelText('Preview');
    await waitFor(() => expect(within(preview).getByText('Our website')).toBeInTheDocument());
    expect(within(preview).queryByText('secret-db')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('combobox', { name: 'Add a check to group 1' }));
    fireEvent.mouseDown(screen.getByRole('option', { name: 'plex' }));
    expect(within(preview).getByText('Partial outage')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Take website off the page' }));
    expect(within(preview).queryByText('Our website')).not.toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(/^Message/), { target: { value: 'Back soon' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u, i]) => u === '/api/statuspage' && i?.method === 'PUT')).toBe(true));
    const sent = JSON.parse(fetchMock.mock.calls.find(([u, i]) => u === '/api/statuspage' && i?.method === 'PUT')![1]?.body as string);
    expect(sent.groups[0].items).toEqual([{ check: 'plex' }]);
    expect(sent.announcement).toBe('Back soon');
    expect(await screen.findByText('Status page saved')).toBeInTheDocument();
  });
});

describe('finish', () => {
  it('numbers glide to their new value', async () => {
    const { rerender } = render(<Num value={10} format={(n) => n.toFixed(0)} duration={60} />);
    expect(screen.getByText('10')).toBeInTheDocument();
    rerender(<Num value={50} format={(n) => n.toFixed(0)} duration={60} />);
    expect(await screen.findByText('50')).toBeInTheDocument();
  });

  it('puts problems in the browser tab', async () => {
    const Health = () => <HealthInTab down={useProblems(true)} />;
    vi.spyOn(globalThis, 'fetch').mockImplementation(
      async (input) =>
        new Response(
          JSON.stringify(
            String(input) === '/api/units'
              ? { enabled: true, units: [{ name: 'bazarr.service', description: '', active: 'failed', sub: 'failed', restarts: 0 }] }
              : { services: [svc({ name: 'web', status: 'down' }), svc({ name: 'dns' })] },
          ),
          { headers: { 'Content-Type': 'application/json' } },
        ),
    );
    render(
      <MemoryRouter initialEntries={['/network']}>
        <ServicesProvider enabled>
          <Health />
        </ServicesProvider>
      </MemoryRouter>,
    );
    await waitFor(() => expect(document.title).toBe('2 down · Network · servermonitor'));
    expect(document.querySelector<HTMLLinkElement>('link[rel="icon"]')?.href).toContain('d03b3b');
  });
});

describe('notifications', () => {
  const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  const note = (over: Partial<Notification>): Notification => ({
    id: 1,
    ts: Date.now() / 1000 - 60,
    area: 'services',
    severity: 'critical',
    title: 'bazarr failed',
    body: 'Bazarr stopped and did not come back.',
    fields: [{ name: 'State', value: 'failed (exit-code)' }],
    subject: 'unit:bazarr.service',
    status: 'sent',
    channels: ['Discord'],
    read: false,
    ...over,
  });
  const counts = { unread: 2, alerts: 1, worst: 'critical', latest: 2 };
  const items = [note({ id: 2 }), note({ id: 1, severity: 'ok', title: 'Download Complete', area: 'torrents', subject: undefined, status: 'muted', channels: [] })];
  const summary = { counts, mutes: [], quiet: true, held: 3 };

  const mockApi = (extra?: (url: string, init?: RequestInit) => Response | undefined) =>
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      const hit = extra?.(url, init);
      if (hit) return hit;
      if (url === '/api/notifications/summary') return json(summary);
      if (url.startsWith('/api/notifications/read')) return json({ counts: { ...counts, unread: 0, alerts: 0, worst: undefined } });
      if (url.startsWith('/api/notifications?') || url === '/api/notifications') {
        const q = new URL(url, 'http://x').searchParams.get('q');
        const list = q ? items.filter((n) => n.title.toLowerCase().includes(q)) : items;
        return json({ items: list, total: list.length, counts, kept: true });
      }
      return json({});
    });

  const Where = () => <div data-testid="where">{useLocation().pathname + useLocation().search}</div>;
  const wrap = (el: JSX.Element, at = '/') =>
    render(
      <MemoryRouter initialEntries={[at]}>
        <ToastProvider>
          <NotificationsProvider enabled>
            {el}
            <Where />
          </NotificationsProvider>
        </ToastProvider>
      </MemoryRouter>,
    );

  it('the bell counts unread alerts and opens the latest, marking it read', async () => {
    const fetchMock = mockApi();
    wrap(<Bell />);
    const bell = await screen.findByRole('button', { name: 'Notifications, 1 unread alert' });
    expect(within(bell).getByText('1')).toBeInTheDocument();
    fireEvent.click(bell);
    const panel = screen.getByRole('dialog', { name: 'Latest notifications' });
    expect(within(panel).getByText(/3 alerts held for the morning summary/)).toBeInTheDocument();
    fireEvent.click(await within(panel).findByText('bazarr failed'));
    expect(screen.getByTestId('where')).toHaveTextContent('/system?open=bazarr.service');
    await waitFor(() => expect(fetchMock.mock.calls.some(([u, i]) => u === '/api/notifications/read' && JSON.parse(i?.body as string).ids[0] === 2)).toBe(true));
  });

  it('the inbox searches, says where each alert went, and opens its details', async () => {
    mockApi();
    wrap(<NotificationsPage />, '/notifications');
    expect(await screen.findByText('Download Complete')).toBeInTheDocument();
    expect(screen.getByText('muted: sent nowhere')).toBeInTheDocument();
    expect(screen.getByText('sent to Discord')).toBeInTheDocument();
    expect(screen.getByText(/Quiet hours are on/)).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Search notifications'), { target: { value: 'bazarr' } });
    await waitFor(() => expect(screen.queryByText('Download Complete')).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: /bazarr failed/ }));
    expect(screen.getByText('failed (exit-code)')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Open' })).toHaveAttribute('href', '/system?open=bazarr.service');
  });

  it('routes areas to channels and keeps quiet hours', async () => {
    const rules = {
      areas: [
        { id: 'uptime', label: 'Uptime checks' },
        { id: 'torrents', label: 'qBittorrent' },
      ],
      channels: ['Discord', 'ntfy'],
      rules: { uptime: { Discord: 'all', ntfy: 'warning' }, torrents: { Discord: 'off', ntfy: 'warning' } },
      defaults: { uptime: { Discord: 'all', ntfy: 'warning' }, torrents: { Discord: 'off', ntfy: 'warning' } },
      quiet: { enabled: false, start: '23:00', end: '07:00', critical: true },
      quiet_active: false,
      held: 0,
      mutes: [],
    };
    const fetchMock = mockApi((url, init) => {
      if (url === '/api/notifications/rules' && init?.method === 'PUT') return json({ ...rules, ...JSON.parse(init.body as string) });
      if (url === '/api/notifications/rules') return json(rules);
    });
    wrap(<NotificationsPage />, '/notifications/rules');
    fireEvent.click(await screen.findByRole('combobox', { name: 'qBittorrent on ntfy' }));
    fireEvent.mouseDown(screen.getByRole('option', { name: 'Off' }));
    fireEvent.click(screen.getByLabelText('keep quiet'));
    fireEvent.change(screen.getByLabelText('Quiet hours start'), { target: { value: '22:30' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u, i]) => u === '/api/notifications/rules' && i?.method === 'PUT')).toBe(true));
    const sent = JSON.parse(fetchMock.mock.calls.find(([u, i]) => u === '/api/notifications/rules' && i?.method === 'PUT')![1]?.body as string);
    expect(sent.rules.torrents.ntfy).toBe('off');
    expect(sent.quiet).toEqual({ enabled: true, start: '22:30', end: '07:00', critical: true });
    expect(await screen.findByText('Notification rules saved')).toBeInTheDocument();
  });

  it('mutes a service for a while, then shows how long is left', async () => {
    let mutes: { subject: string; label: string; until: number }[] = [];
    const fetchMock = mockApi((url, init) => {
      if (url === '/api/notifications/mute') {
        const b = JSON.parse(init?.body as string);
        mutes = [{ subject: b.subject, label: b.label, until: Date.now() / 1000 + b.minutes * 60 }];
        return json({ mutes });
      }
      if (url === '/api/notifications/summary') return json({ ...summary, mutes });
    });
    wrap(<MuteButton subject="unit:sonarr.service" label="sonarr" />);
    fireEvent.click(await screen.findByRole('button', { name: 'Mute sonarr' }));
    fireEvent.click(screen.getByRole('menuitem', { name: '4 hours' }));
    expect(await screen.findByRole('button', { name: 'Unmute sonarr' })).toHaveTextContent('muted · 4h left');
    const sent = JSON.parse(fetchMock.mock.calls.find(([u]) => u === '/api/notifications/mute')![1]?.body as string);
    expect(sent).toEqual({ subject: 'unit:sonarr.service', label: 'sonarr', minutes: 240 });
    expect(screen.getByText('sonarr muted for 4 hours')).toBeInTheDocument();
  });
});

describe('admin account', () => {
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const config = (account: { configured: boolean; username?: string }) => ({
    auth_required: true,
    authorized: false,
    status_page_enabled: false,
    status_page_title: '',
    account: { ...account, service: 'server-monitor' },
  });

  it('creates the account with the setup code, checking the password first', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(json({ username: 'jay' }));
    const done = vi.fn();
    render(
      <ConfigContext.Provider value={config({ configured: false })}>
        <CreateAdmin onDone={done} />
      </ConfigContext.Provider>,
    );
    expect(screen.getByText('journalctl -u server-monitor | grep setup_code | tail -1')).toBeInTheDocument();
    const submit = screen.getByRole('button', { name: 'Create account and sign in' });
    fireEvent.change(screen.getByLabelText('Username'), { target: { value: 'jay' } });
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'short' } });
    expect(screen.getByRole('alert')).toHaveTextContent('Use at least 10 characters.');
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'correct horse battery' } });
    fireEvent.change(screen.getByLabelText('Confirm password'), { target: { value: 'correct horse batter' } });
    expect(screen.getByRole('alert')).toHaveTextContent('do not match');
    fireEvent.change(screen.getByLabelText('Confirm password'), { target: { value: 'correct horse battery' } });
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Setup code'), { target: { value: 'ABCDE-12345' } });
    fireEvent.click(submit);
    await waitFor(() => expect(done).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/auth/setup');
    expect(JSON.parse(init?.body as string)).toEqual({ username: 'jay', password: 'correct horse battery', setup_code: 'ABCDE-12345' });
  });

  it('shows why a sign-in failed instead of treating it as signed out', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(json({ error: 'wrong username or password' }, 401));
    const signedOut = vi.fn();
    onUnauthorized(signedOut);
    render(
      <ConfigContext.Provider value={config({ configured: true, username: 'jay' })}>
        <SignIn onDone={() => undefined} />
      </ConfigContext.Provider>,
    );
    expect(screen.getByLabelText('Username')).toHaveValue('jay');
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'nope nope nope' } });
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Wrong username or password.');
    expect(screen.getByLabelText('Password')).toHaveValue('');
    expect(signedOut).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Forgot the password?' }));
    expect(screen.getByText('servermonitor -reset-admin')).toBeInTheDocument();
  });

  it('keeps theme, status page and signing out in one menu', () => {
    const toggle = vi.fn();
    const signOut = vi.fn();
    render(
      <MemoryRouter>
        <ConfigContext.Provider value={{ ...config({ configured: true, username: 'jay' }), authorized: true, status_page_enabled: true }}>
          <AccountMenu theme="light" onToggleTheme={toggle} onSignOut={signOut} />
        </ConfigContext.Provider>
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Account: jay' }));
    fireEvent.click(screen.getByRole('menuitem', { name: 'Dark theme' }));
    expect(toggle).toHaveBeenCalled();
    expect(screen.getByRole('menuitem', { name: /Public status page/ })).toHaveAttribute('href', '/status');
    fireEvent.click(screen.getByRole('menuitem', { name: 'Sign out' }));
    expect(signOut).toHaveBeenCalled();
    expect(screen.queryByRole('menu')).not.toBeInTheDocument();
  });

  it('changes the password from Settings', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(json({ changed: true }));
    render(
      <ToastProvider>
        <ConfigContext.Provider value={{ ...config({ configured: true, username: 'jay' }), authorized: true }}>
          <AccountSection />
        </ConfigContext.Provider>
      </ToastProvider>,
    );
    expect(screen.getByText('jay')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Current password'), { target: { value: 'correct horse battery' } });
    fireEvent.change(screen.getByLabelText('New password'), { target: { value: 'an even better one' } });
    fireEvent.change(screen.getByLabelText('Confirm new password'), { target: { value: 'an even better one' } });
    fireEvent.click(screen.getByRole('button', { name: 'Change password' }));
    expect(await screen.findByText('Password changed')).toBeInTheDocument();
    expect(JSON.parse(fetchMock.mock.calls[0][1]?.body as string)).toEqual({ current: 'correct horse battery', new: 'an even better one' });
    expect(screen.getByLabelText('New password')).toHaveValue('');
  });
});

describe('monitor log', () => {
  it('shows the log, filters it, and follows new entries', async () => {
    const entry = (seq: number, level: 'info' | 'warn' | 'error', message: string, component = 'web') => ({
      seq,
      time: Date.now() - (10 - seq) * 1000,
      level,
      component,
      message,
      attrs: [{ key: 'ip', value: '203.0.113.9' }],
    });
    let calls = 0;
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = new URL(String(input), 'http://x');
      calls++;
      const after = Number(url.searchParams.get('after') ?? 0);
      const all = [entry(1, 'info', 'Web dashboard listening'), entry(2, 'warn', 'Failed sign-in'), ...(calls > 1 ? [entry(3, 'error', 'Listener died')] : [])];
      const level = url.searchParams.get('level');
      const rank = { info: 1, warn: 2, error: 3 };
      const entries = all.filter((e) => e.seq > after && (!level || rank[e.level] >= rank[level as 'warn']));
      return new Response(JSON.stringify({ entries, latest: all.length, components: ['disk', 'web'], level: 'INFO', file: 'server_monitor.log', kept: 3000 }), {
        headers: { 'Content-Type': 'application/json' },
      });
    });
    render(
      <MemoryRouter>
        <LogsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText('Failed sign-in')).toBeInTheDocument();
    expect(screen.getAllByText('203.0.113.9')).toHaveLength(2);
    expect(screen.getByText('server_monitor.log')).toBeInTheDocument();

    // Following asks only for what is newer.
    expect(await screen.findByText('Listener died', {}, { timeout: 3000 })).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([u]) => String(u).includes('after=2'))).toBe(true);

    fireEvent.click(screen.getByRole('button', { name: /^Errors/ }));
    await waitFor(() => expect(screen.queryByText('Failed sign-in')).not.toBeInTheDocument());
    expect(screen.getByText('Listener died')).toBeInTheDocument();
  });
});

describe('app store', () => {
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const app = (id: string, name: string, over = {}) => ({
    id,
    name,
    category: 'Automation',
    summary: `${name} does things.`,
    homepage: 'https://example.com',
    port: 8686,
    color: '#00a65b',
    installed: false,
    running: false,
    available: true,
    ...over,
  });

  it('shows what is installed and installs an app with a live log', async () => {
    let polls = 0;
    let done = false;
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/apps')
        return json({
          apps: [
            app('sonarr', 'Sonarr', { installed: true, running: true, by: 'swizzin', port: 8989 }),
            app('lidarr', 'Lidarr', done ? { installed: true, running: true, by: 'store', port: 18686 } : {}),
          ],
          job: null,
          user: 'perked',
        });
      if (url === '/api/apps/lidarr/install')
        return json({ job: { id: 1, app: 'lidarr', action: 'install', step: 'Downloading Lidarr 3.1', started: 1, ok: false, lines: ['::step:: Downloading Lidarr 3.1'] } }, 202);
      if (url.startsWith('/api/apps/job')) {
        polls++;
        done = true;
        return json({ job: { id: 1, app: 'lidarr', action: 'install', step: 'Installed', started: 1, finished: 2, ok: true, lines: ['::step:: Installed'] } });
      }
      return json({}, init?.method ? 200 : 404);
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <AppsPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    const sonarr = (await screen.findAllByRole('article')).find((a) => a.textContent?.includes('Sonarr'))!;
    expect(within(sonarr).getByText(/installed by swizzin/)).toBeInTheDocument();
    expect(within(sonarr).queryByRole('button', { name: 'Install' })).not.toBeInTheDocument();

    const lidarr = screen.getAllByRole('article').find((a) => a.textContent?.includes('Lidarr'))!;
    fireEvent.click(within(lidarr).getByRole('button', { name: 'Install' }));
    const sheet = screen.getByRole('dialog', { name: 'Lidarr' });
    expect(within(sheet).getByText('perked')).toBeInTheDocument();
    fireEvent.change(within(sheet).getByLabelText('Web port'), { target: { value: '18686' } });
    fireEvent.click(within(sheet).getByRole('button', { name: 'Install Lidarr' }));
    expect(await within(sheet).findByText('Downloading Lidarr 3.1', { selector: '.install-step' })).toBeInTheDocument();
    expect(JSON.parse(fetchMock.mock.calls.find(([u]) => u === '/api/apps/lidarr/install')![1]?.body as string)).toEqual({ port: 18686, check: true });
    expect(await within(sheet).findByText('Lidarr is installed and running', {}, { timeout: 3000 })).toBeInTheDocument();
    expect(polls).toBeGreaterThan(0);

    // Reopened later, the sheet manages the app instead of showing the finished install again.
    fireEvent.click(within(sheet).getByRole('button', { name: 'Close' }));
    const installed = (await screen.findAllByRole('article')).find((a) => a.textContent?.includes('Lidarr'))!;
    fireEvent.click(await within(installed).findByRole('button', { name: 'manage' }));
    const again = screen.getByRole('dialog', { name: 'Lidarr' });
    expect(within(again).getByRole('button', { name: 'reinstall latest' })).toBeInTheDocument();
    expect(within(again).getByRole('button', { name: 'remove…' })).toBeInTheDocument();
  });
});

describe('sections', () => {
  it('turns parts of the dashboard off and on', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ restarting: false, applied: true }), { headers: { 'Content-Type': 'application/json' } }));
    const reloaded = vi.fn();
    window.addEventListener('config-changed', reloaded);
    render(
      <ToastProvider>
        <ConfigContext.Provider value={{ auth_required: true, authorized: true, status_page_enabled: false, status_page_title: '', hidden: ['drives'] }}>
          <SectionsPanel />
        </ConfigContext.Provider>
      </ToastProvider>,
    );
    expect(screen.getByRole('switch', { name: /Drive health/ })).not.toBeChecked();
    fireEvent.click(screen.getByRole('switch', { name: /^Network/ }));
    await waitFor(() => expect(reloaded).toHaveBeenCalled());
    expect(JSON.parse(fetchMock.mock.calls[0][1]?.body as string)).toEqual({ values: { DASHBOARD_HIDDEN: 'drives,network' } });
    expect(await screen.findByText('Network hidden')).toBeInTheDocument();
    window.removeEventListener('config-changed', reloaded);
  });
});
