import { fireEvent, render, renderHook, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router';
import { afterEach, describe, expect, it, onTestFinished, vi } from 'vitest';
import { api, onUnauthorized, UnauthorizedError } from '../src/api/client';
import { ConfigContext } from '../src/api/ConfigContext';
import { NotificationsProvider } from '../src/api/NotificationsContext';
import { ServicesProvider, useServices } from '../src/api/ServicesContext';
import type { Notification, PlexStream, QBState, QBTorrent, Service, SettingField, SetupStatus, Unit, UpdateTarget, UpdatesResponse } from '../src/api/types';
import { UpdatesProvider } from '../src/api/UpdatesContext';
import { buildIn, useNewBuild } from '../src/freshness';
import { AccountSection, CreateAdmin, SignIn } from '../src/components/Account';
import { AccountMenu } from '../src/components/AccountMenu';
import { SectionsPanel } from '../src/components/Sections';
import { AppearancePanel } from '../src/components/AppearancePanel';
import { useAppearance } from '../src/appearance';
import { Bell } from '../src/components/Bell';
import { Finder } from '../src/components/Finder';
import { MuteButton } from '../src/components/MuteButton';
import { HealthInTab, useProblems } from '../src/components/HealthInTab';
import { Num } from '../src/components/Num';
import { NowPlaying } from '../src/components/PlexNow';
import { PlexStreamSheet } from '../src/components/PlexStreamSheet';
import { Diagnosis } from '../src/components/Diagnosis';
import { Select } from '../src/components/Select';
import { AttackWatch } from '../src/components/Attacks';
import { FirewallManager, within as inNetwork, type FirewallTarget } from '../src/components/Firewall';
import type { FirewallRule, FirewallStatus } from '../src/api/types';
import { ToastProvider } from '../src/components/Toasts';
import { TrendChart, withGaps } from '../src/components/TrendChart';
import { State, Ticks } from '../src/components/ui';
import { verdict as unitVerdict } from '../src/components/unitState';
import { formatBytes, formatClock, formatKbps, formatPercent } from '../src/format';
import { usePoll } from '../src/hooks/usePoll';
import { CheckForm } from '../src/pages/CheckForm';
import { IncidentsPage } from '../src/pages/Incidents';
import { MaintenancePage } from '../src/pages/Maintenance';
import { CleanupPage } from '../src/pages/Cleanup';
import { BackupPanel } from '../src/components/BackupPanel';
import { WeeklyReportPage } from '../src/pages/WeeklyReport';
import { demoCleanup, demoIncidents } from '../src/api/demoIncidents';
import { NetworkPage } from '../src/pages/Network';
import { AppsPage } from '../src/pages/Apps';
import { LogsPage } from '../src/pages/Logs';
import { NotificationsPage } from '../src/pages/Notifications';
import { ServerDetail } from '../src/pages/Servers';
import { QBittorrentPage } from '../src/pages/QBittorrent';
import { group } from '../src/pages/qbittorrent/states';
import { ServiceRow, ServicesPage } from '../src/pages/Services';
import { AnalyticsPage, fullIn } from '../src/pages/Analytics';
import { DocsPage, docBody, searchDocs } from '../src/pages/Docs';
import { DOCS } from '../src/docs';
import { Markdown, plain, slug } from '../src/components/Markdown';
import { ServersPage } from '../src/pages/Servers';
import { SettingsPage } from '../src/pages/Settings';
import { SetupPage } from '../src/pages/Setup';
import { StatusDesignerPage } from '../src/pages/StatusDesigner';
import { PluginsPage } from '../src/pages/Plugins';
import { PluginCardBody } from '../src/components/PluginCards';
import { StatusPage, StatusView } from '../src/pages/StatusPage';
import { sanitizeHTML, scopeCSS } from '../src/customCode';
import { SystemServicesPage } from '../src/pages/SystemServices';
import { UpdatesPage, isMajor, itemState, verdict } from '../src/pages/Updates';

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
  const day = (i: number, uptime: number | null, down_s = 0) => ({ t: 1_700_000_000 + i * 86400, uptime, down_s });
  it('shows the overall state, each service and its 90 days', async () => {
    const days = Array.from({ length: 90 }, (_, i) => (i === 89 ? day(i, 97.2, 2400) : i < 10 ? day(i, null) : day(i, 100)));
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(
        JSON.stringify({
          title: 'Acme status',
          overall: 'degraded',
          updated: Date.now() / 1000,
          groups: [{ name: 'Web', services: [{ name: 'web', status: 'down', uptime_24h: 90, uptime_30d: 97.5, uptime_90d: 99.21, recent: [], days }] }],
          services: [{ name: 'web', status: 'down', uptime_24h: 90, uptime_30d: 97.5, uptime_90d: 99.21, recent: [], days }],
        }),
        { status: 200 },
      ),
    );
    render(<StatusPage />);
    await waitFor(() => expect(screen.getByText('Partial outage')).toBeInTheDocument());
    expect(screen.getByText('Acme status')).toBeInTheDocument();
    // The title is set once the page has its data, which can come a moment after its text.
    await waitFor(() => expect(document.title).toBe('Partial outage · Acme status'));
    expect(screen.getByText('0 of 1 operational')).toBeInTheDocument();
    expect(screen.getByText('99.21% uptime')).toBeInTheDocument();
    expect(screen.getByText('Updated just now')).toBeInTheDocument();
    const bars = screen.getByRole('img', { name: 'web, the last 90 days: 1 day with downtime' });
    expect(bars.querySelectorAll('.day')).toHaveLength(90);
    expect(bars.querySelectorAll('.day.none')).toHaveLength(10);
    // Hovering today says how it went.
    fireEvent.pointerEnter(bars.querySelectorAll('.day')[89]);
    const tip = screen.getByRole('tooltip');
    expect(tip).toHaveTextContent('97.2% up');
    expect(tip).toHaveTextContent('about 40m 0s down');
  });

  it('falls back to the 30-day figure from an older monitor without days', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(
      new Response(JSON.stringify({ title: 'Acme status', overall: 'operational', updated: 1, services: [{ name: 'web', status: 'up', uptime_24h: 100, uptime_30d: 97.5, recent: [] }] }), {
        status: 200,
      }),
    );
    render(<StatusPage />);
    await waitFor(() => expect(screen.getByText('All systems operational')).toBeInTheDocument());
    expect(screen.getByText('97.5% uptime')).toBeInTheDocument();
    expect(screen.queryByRole('img')).not.toBeInTheDocument();
    await waitFor(() => expect(document.title).toBe('Acme status'));
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

  it('stops a stream with a message, after asking once more', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ enabled: true, plex: null })));
    const onStopped = vi.fn();
    const onClose = vi.fn();
    render(
      <ToastProvider>
        <PlexStreamSheet stream={stream({ address: '203.0.113.58', local: false })} onClose={onClose} onStopped={onStopped} />
      </ToastProvider>,
    );
    expect(screen.getByText(/203\.0\.113\.58/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('radio', { name: 'Server maintenance, back soon.' }));
    fireEvent.click(screen.getByRole('button', { name: 'Stop stream' }));
    expect(fetchMock).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Yes, stop it' }));
    await waitFor(() => expect(onStopped).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/plex/streams/1/stop');
    expect(JSON.parse(init?.body as string)).toEqual({ message: 'Server maintenance, back soon.' });
    expect(onClose).toHaveBeenCalled();
  });

  it('says why Plex would not stop a stream, and when it has ended', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ error: 'Plex refused to stop the stream' }), { status: 502 }));
    const s = stream({});
    const { rerender } = render(<PlexStreamSheet stream={s} onClose={() => {}} onStopped={() => {}} />);
    fireEvent.click(screen.getByRole('button', { name: 'Stop stream' }));
    fireEvent.click(screen.getByRole('button', { name: 'Yes, stop it' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Plex refused to stop the stream');
    rerender(<PlexStreamSheet stream={undefined} onClose={() => {}} onStopped={() => {}} />);
    expect(screen.getByText('This stream has ended.')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Stop stream' })).not.toBeInTheDocument();
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
  // The form also asks for the servers a check can belong to; these tests are about the check.
  const sent = (m: { mock: { calls: Parameters<typeof fetch>[] } }) => m.mock.calls.filter(([u]) => String(u) !== '/api/hosts');
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
    expect(sent(fetchMock).map(([u]) => u)).toEqual(['/api/checks/test', '/api/checks']);
    const [, init] = sent(fetchMock)[1];
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
    expect(sent(fetchMock).map(([u]) => u)).toEqual(['/api/checks/test', '/api/checks']);

    // Changing the target makes the old result stale.
    fireEvent.change(screen.getByLabelText('Target'), { target: { value: 'https://example.org' } });
    expect(screen.queryByText('Not reachable')).not.toBeInTheDocument();
  });

  it('adds a heartbeat with its schedule and a repair, with nothing to test', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async (input) => (String(input) === '/api/units' ? json({ enabled: true, units: [] }) : json({ check: { name: 'backup' } }, 201)));
    const onSaved = vi.fn();
    render(<CheckForm onSaved={onSaved} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'backup' } });
    fireEvent.click(screen.getByRole('button', { name: 'heartbeat' }));
    expect(screen.queryByLabelText('Target')).not.toBeInTheDocument();
    expect(screen.queryByText('Test')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'nightly at 3:00' }));
    expect(screen.getByLabelText('Schedule')).toHaveValue('0 3 * * *');
    fireEvent.click(screen.getByRole('button', { name: 'Restart a service' }));
    // Without a list of services, the name is typed.
    fireEvent.change(await screen.findByLabelText('Service to restart'), { target: { value: 'backup.service' } });
    fireEvent.click(screen.getByText('Add check'));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    const post = fetchMock.mock.calls.find(([u, i]) => String(u) === '/api/checks' && i?.method === 'POST');
    expect(JSON.parse(post?.[1]?.body as string)).toMatchObject({
      name: 'backup',
      type: 'heartbeat',
      schedule: '0 3 * * *',
      grace: 600,
      repair: { action: 'restart', unit: 'backup.service', max: 2, wait: 120 },
    });
    expect(fetchMock.mock.calls.some(([u]) => String(u) === '/api/checks/test')).toBe(false);
  });

  it('paused checks save without a test', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => json({ check: { name: 'web', paused: true } }, 201));
    const onSaved = vi.fn();
    render(<CheckForm onSaved={onSaved} onCancel={() => {}} />);
    fill();
    fireEvent.click(screen.getByLabelText(/paused/i));
    fireEvent.click(screen.getByText('Add check'));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    expect(sent(fetchMock).map(([u]) => u)).toEqual(['/api/checks']);
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
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      if (String(input).startsWith('/api/activity')) return new Response(JSON.stringify({ activity: [] }));
      if (init?.method === 'PUT') return new Response(JSON.stringify({ error: 'Some settings are invalid', fields: { CPU_THRESHOLD: 'must be at most 100' } }), { status: 400 });
      return new Response(JSON.stringify(settings));
    });
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
    const put = fetchMock.mock.calls.find(([, i]) => i?.method === 'PUT')!;
    expect(JSON.parse(put[1]?.body as string)).toEqual({ values: { CPU_THRESHOLD: '150' } });
  });
});

describe('demo data', () => {
  it('answers the API and keeps edits', async () => {
    // At midday: from 03:00 to 03:30 the demo's nightly backup window puts nas-backup in maintenance.
    vi.useFakeTimers({ toFake: ['Date'] });
    vi.setSystemTime(new Date('2026-11-05T12:00:00'));
    onTestFinished(() => {
      vi.useRealTimers();
    });
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
    fireEvent.click(within(screen.getByText('api is down').closest('.toast') as HTMLElement).getByLabelText('Dismiss'));
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

  it('tells where each update of an Update all is, and which are major versions', () => {
    const started = 100;
    expect(itemState(t({ busy: 'queued' }), started)).toBe('waiting');
    expect(itemState(t({ busy: 'updating' }), started)).toBe('running');
    expect(itemState(t({ runs: [{ at: 150, ok: true, auto: false }] }), started)).toBe('updated');
    expect(itemState(t({ available: true, runs: [{ at: 150, ok: false, auto: false }] }), started)).toBe('failed');
    expect(itemState(t({ available: true, runs: [{ at: 50, ok: true, auto: false }] }), started)).toBe('waiting'); // an older run is not this one
    expect(itemState(t({ available: false }), started)).toBe('skipped');
    expect(itemState(undefined, started)).toBe('skipped');
    expect(isMajor(t({ installed: '2.3.7.5365', latest: '3.0.0.5737' }))).toBe(true);
    expect(isMajor(t({ installed: '4.0.14', latest: 'v4.0.15' }))).toBe(false);
    expect(isMajor(t({ kind: 'git', installed: 'v1.2', latest: 'v2.0' }))).toBe(false);
  });

  it('lets you choose what Update all installs, then follows it', async () => {
    localStorage.removeItem('nightjar.updateAll');
    const prowlarr = t({ id: 'prowlarr', name: 'Prowlarr', installed: '2.3.7.5365', latest: '3.0.0.5737', available: true });
    const adguard = t({ id: 'adguard', name: 'AdGuard Home', kind: 'release', installed: '0.107.79', latest: '0.107.80', available: true });
    const applied: string[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      const m = url.match(/^\/api\/updates\/([a-z]+)\/update$/);
      if (m && init?.method === 'POST') {
        applied.push(m[1]);
        return json({ queued: true }, 202);
      }
      const busy = applied.includes('prowlarr');
      return json(
        page([
          { ...prowlarr, busy: busy ? 'updating' : undefined, live: busy ? { started: Date.now() / 1000, lines: ['Asking Prowlarr to update itself to 3.0.0.5737 (a major update)'] } : undefined },
          adguard,
        ]),
      );
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
    fireEvent.click(await screen.findByRole('button', { name: 'Update all (2)' }));
    const sheet = screen.getByRole('dialog', { name: 'Update all' });
    // A major version is marked, and anything can be left out.
    expect(within(sheet).getByText('Major version')).toBeInTheDocument();
    fireEvent.click(within(sheet).getAllByRole('checkbox')[1]);
    fireEvent.click(within(sheet).getByRole('button', { name: 'Update 1 item' }));
    await waitFor(() => expect(applied).toEqual(['prowlarr']));
    // The page follows it: how far it is, and the running one's log, shown once.
    const run = await screen.findByRole('region', { name: 'Update all' });
    expect(within(run).getByText(/Updating all · 0 of 1 done/)).toBeInTheDocument();
    expect(await within(run).findByText(/\(a major update\)/)).toBeInTheDocument();
    expect(screen.getAllByText(/\(a major update\)/)).toHaveLength(1);
    expect(JSON.parse(localStorage.getItem('nightjar.updateAll')!).ids).toEqual(['prowlarr']);
    localStorage.removeItem('nightjar.updateAll');
  });

  it('shows what an update is doing as it does it, and keeps its log', async () => {
    const started = Date.now() / 1000 - 75;
    const running = t({
      installed: '4.0.14',
      latest: '4.0.15',
      available: true,
      busy: 'updating',
      live: { started, lines: ['Updating Sonarr: 4.0.14 → 4.0.15', 'Asking Sonarr to update itself to 4.0.15', 'It stopped answering: restarting'] },
    });
    const done = t({
      id: 'os',
      name: 'OS packages (apt)',
      kind: 'system',
      runs: [
        {
          at: 2,
          ok: false,
          auto: true,
          detail: 'apt-get failed (exit 100)',
          log: ['Updating OS packages (apt): 3 packages', '$ apt-get -y upgrade', 'E: Could not get lock /var/lib/dpkg/lock-frontend', 'Failed: apt-get failed (exit 100)'],
        },
      ],
    });
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => json(page([running, done])));
    render(
      <MemoryRouter>
        <ToastProvider>
          <UpdatesProvider enabled>
            <UpdatesPage />
          </UpdatesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );
    // While it runs, its log shows under it without opening anything, with how long it has taken.
    expect(await screen.findByText('Asking Sonarr to update itself to 4.0.15')).toBeInTheDocument();
    expect(screen.getByText('It stopped answering: restarting')).toBeInTheDocument();
    expect(screen.getByText(/1m 1\ds so far/)).toBeInTheDocument();
    // A finished run keeps its log, which says why it failed.
    fireEvent.click(screen.getByText('OS packages (apt)'));
    fireEvent.click(await screen.findByText('Log'));
    expect(screen.getByText('E: Could not get lock /var/lib/dpkg/lock-frontend')).toBeInTheDocument();
    expect(screen.getByText('$ apt-get -y upgrade')).toHaveClass('log-cmd');
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
    // Its log fills a line at a time while it runs.
    await vi.advanceTimersByTimeAsync(1500);
    const midway = (await get()).targets.find((x) => x.id === 'adguard-home')!;
    expect(midway.busy).toBe('updating');
    expect(midway.live?.lines[0]).toMatch(/^Updating AdGuard Home/);
    await vi.advanceTimersByTimeAsync(10000);
    const after = (await get()).targets.find((x) => x.id === 'adguard-home')!;
    expect(after.available).toBe(false);
    expect(after.runs?.[0]).toMatchObject({ ok: true, to: 'v0.107.80' });
    expect(after.runs?.[0].log?.at(-1)).toBe('Done');
    expect(after.live).toBeUndefined();
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

    fireEvent.click(within(screen.getByRole('group', { name: 'Filter torrents' })).getByRole('button', { name: /^seeding/i }));
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
    fireEvent.click(within(bar).getByText('Stop'));
    await waitFor(() => expect(posted('/api/qbittorrent/action')).toEqual([{ action: 'stop', hashes: [h('a'), h('b')], delete_files: false }]));
    expect(await screen.findByText('Stopped 2 torrents')).toBeInTheDocument();
  });

  it('asks before deleting, and whether to keep the files', async () => {
    const posted = manage([torrent({ hash: h('a'), name: 'old.iso', state: 'stoppedup' })]);
    fireEvent.click(await screen.findByRole('button', { name: 'old.iso' }));
    expect(screen.getByText(h('a'))).toBeInTheDocument(); // details show the hash
    fireEvent.click(screen.getByText('Delete…'));
    expect(posted('/api/qbittorrent/action')).toEqual([]);
    expect(screen.getByText('Delete this torrent?')).toBeInTheDocument();
    fireEvent.click(screen.getByText('Delete files too'));
    await waitFor(() => expect(posted('/api/qbittorrent/action')).toEqual([{ action: 'delete', hashes: [h('a')], delete_files: true }]));
  });

  it('adds magnet links into a category', async () => {
    const posted = manage([]);
    fireEvent.click(await screen.findByText('Add torrent'));
    fireEvent.change(screen.getByLabelText('Magnet links or URLs'), { target: { value: 'magnet:?xt=urn:btih:abc' } });
    fireEvent.click(screen.getByRole('combobox', { name: 'Category' }));
    fireEvent.mouseDown(await screen.findByRole('option', { name: 'tv' }));
    expect(screen.getByRole('combobox', { name: 'Category' })).toHaveTextContent('tv');
    fireEvent.click(screen.getByLabelText(/add stopped/i));
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
    expect(screen.getByText(/applies at once/i)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Server URL'), { target: { value: 'http://nas:32400' } });
    expect(screen.getByText(/restarts the monitor \(Server URL\)/i)).toBeInTheDocument();
    // Put back as it was, the address is no change, so saving needs no restart.
    fireEvent.change(screen.getByLabelText('Server URL'), { target: { value: 'http://localhost:32400' } });
    expect(screen.queryByRole('button', { name: 'Save and restart' })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('Settings saved')).toBeInTheDocument();
    const puts = fetchMock.mock.calls.filter(([, i]) => i?.method === 'PUT');
    expect(puts).toHaveLength(1);
    expect(Object.keys(JSON.parse(puts[0][1]?.body as string).values)).toEqual(['CPU_THRESHOLD']);
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
    const units = [u('bazarr', { active: 'failed', sub: 'failed', result: 'exit-code', exit_status: 1 }), u('sonarr'), u('nightjar')];
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/logs')) return json({ lines: ['bazarr[1]: database disk image is malformed'] });
      if (init?.method === 'POST') return json({ unit: u(url.split('/')[3]) });
      return json({ enabled: true, self: 'nightjar.service', units });
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
    fireEvent.click(within(screen.getByRole('group', { name: 'Filter services' })).getByRole('button', { name: /problems/i }));
    expect(screen.queryByText('sonarr')).not.toBeInTheDocument();
    fireEvent.click(within(screen.getByRole('group', { name: 'Filter services' })).getByRole('button', { name: /all/i }));

    fireEvent.click(screen.getByText('bazarr'));
    expect(await screen.findByText(/database disk image is malformed/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Start' }));
    expect(await screen.findByText('bazarr: started')).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([url, i]) => url === '/api/units/bazarr.service/start' && i?.method === 'POST')).toBe(true);

    fireEvent.click(screen.getByText('sonarr'));
    fireEvent.click(await screen.findByText('Stop…'));
    expect(fetchMock.mock.calls.some(([url]) => String(url).endsWith('/stop'))).toBe(false);
    fireEvent.click(screen.getByRole('button', { name: 'Stop' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([url, i]) => url === '/api/units/sonarr.service/stop' && i?.method === 'POST')).toBe(true));

    fireEvent.click(screen.getByText('nightjar'));
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
    expect(sessionStorage.getItem('nightjar.settings.draft')).not.toContain('secret-value');
    expect(screen.getByRole('region', { name: 'Unsaved changes' })).toHaveTextContent('1 unsaved change');
    // Put back as it was, it is no change, and there is nothing to save.
    fireEvent.change(screen.getByLabelText('CPU'), { target: { value: '80' } });
    expect(screen.queryByRole('region', { name: 'Unsaved changes' })).not.toBeInTheDocument();
    sessionStorage.clear();
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
    const targets = [t('nightjar', { kind: 'git', self: true }), t('sonarr'), t('qbittorrent', { can_update: false })];
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
    localStorage.removeItem('nightjar.updateAll');
    fireEvent.click(await screen.findByText('Update all (2)'));
    expect(screen.getByText(/restarts the monitor, so goes last/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Update 2 items' }));
    await waitFor(() => expect(fetchMock.mock.calls.filter(([, i]) => i?.method === 'POST')).toHaveLength(2));
    expect(fetchMock.mock.calls.filter(([, i]) => i?.method === 'POST').map(([u]) => u)).toEqual(['/api/updates/sonarr/update', '/api/updates/nightjar/update']);
    localStorage.removeItem('nightjar.updateAll');
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
    // The page also asks for the attack watch; the meter's own polls ask only for new samples.
    const meter = () => fetchMock.mock.calls.map(([u]) => String(u)).filter((u) => u.startsWith('/api/network'));
    await waitFor(() => expect(meter().length).toBeGreaterThan(1), { timeout: 2500 });
    expect(meter()[1]).toBe('/api/network?since=101');
    fireEvent.click(screen.getByRole('button', { name: /^wg0/ }));
    expect(screen.getByRole('button', { name: /^wg0/ })).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByText(/wg0/, { selector: '.page-sub span' })).toBeInTheDocument();
  });

  it('shows the page when a list comes back null, as a monitor before the fix sent', async () => {
    const nic = {
      name: 'eth0',
      up: true,
      default: true,
      virtual: false,
      speed_mbps: 1000,
      rx_bps: 12_500_000,
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
    };
    vi.spyOn(globalThis, 'fetch').mockImplementation(
      async () =>
        new Response(JSON.stringify({ enabled: true, history: [], network: { interfaces: [nic], samples: { eth0: null }, processes: null, hosts: null, connections: 0, since: 90 } }), {
          headers: { 'Content-Type': 'application/json' },
        }),
    );
    render(
      <MemoryRouter>
        <NetworkPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText('100 Mbit/s')).toBeInTheDocument();
    expect(screen.queryByText(/Can't reach the monitor/)).not.toBeInTheDocument();
  });

  it('rides out a dropped poll, and says so only once the monitor stays out of reach', async () => {
    let calls = 0;
    const answer = () =>
      new Response(
        JSON.stringify({
          enabled: true,
          history: [],
          network: {
            interfaces: [
              {
                name: 'eth0',
                up: true,
                default: true,
                virtual: false,
                rx_bps: 1000,
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
              },
            ],
            samples: { eth0: [{ ts: 100, rx: 1, tx: 2 }] },
            processes: [],
            hosts: [],
            connections: 0,
            since: 90,
          },
        }),
        { headers: { 'Content-Type': 'application/json' } },
      );
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => {
      calls++;
      if (calls === 1 || calls === 3) return answer();
      throw new TypeError('Failed to fetch');
    });
    render(
      <MemoryRouter>
        <NetworkPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText('eth0', { selector: '.page-sub span' })).toBeInTheDocument();
    // One dropped poll, then an answer: no alarm.
    await waitFor(() => expect(calls).toBeGreaterThanOrEqual(3), { timeout: 4000 });
    expect(screen.queryByText(/Can't reach the monitor/)).not.toBeInTheDocument();
    // Three in a row: now it says so, and the last reading stays.
    expect(await screen.findByText(/Can't reach the monitor/, {}, { timeout: 5000 })).toBeInTheDocument();
    expect(calls).toBeGreaterThanOrEqual(6);
    expect(screen.getByText('eth0', { selector: '.page-sub span' })).toBeInTheDocument();
  }, 15000);
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

    // Code typed and deleted again leaves nothing to save.
    const unsaved = () => screen.queryByRole('region', { name: 'Unsaved changes' });
    const before = !!unsaved();
    fireEvent.change(screen.getByLabelText('CSS'), { target: { value: 'body { color: red }' } });
    expect(unsaved()).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('CSS'), { target: { value: '' } });
    expect(!!unsaved()).toBe(before);

    fireEvent.change(screen.getByLabelText(/^Message/), { target: { value: 'Back soon' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u, i]) => u === '/api/statuspage' && i?.method === 'PUT')).toBe(true));
    const sent = JSON.parse(fetchMock.mock.calls.find(([u, i]) => u === '/api/statuspage' && i?.method === 'PUT')![1]?.body as string);
    expect(sent.groups[0].items).toEqual([{ check: 'plex' }]);
    expect(sent.announcement).toBe('Back soon');
    expect(await screen.findByText('Status page saved')).toBeInTheDocument();
  });

  it("shows the owner's HTML with anything that runs code taken out", () => {
    const html = sanitizeHTML(
      '<a href="https://example.com" target="_blank" onclick="steal()">Home</a><img src="javascript:alert(1)" alt="x"><script>steal()</script><iframe src="https://evil.example"></iframe><custom-tag><b>kept</b></custom-tag><a href=" java\tscript:alert(1)">bad</a>',
    );
    expect(html).not.toMatch(/onclick|script|iframe|custom-tag|javascript/i);
    expect(html).toContain('<a href="https://example.com" target="_blank" rel="noopener noreferrer">Home</a>');
    expect(html).toContain('<b>kept</b>');
    expect(html).toContain('<img alt="x">');
  });

  it("keeps the owner's CSS inside the preview", () => {
    const css = scopeCSS(
      '@import url("https://fonts.example/a.css");\nbody { background: #111 }\n:root{--x:1}\n.status-row { color: red; content: "}" }\n@keyframes glow { to { opacity: 1 } }\n@media (max-width: 600px) { html .status-top { display: none } }',
      '.sd-frame',
    );
    // An imported sheet would style the whole dashboard, so the preview goes without it; its own
    // animations are renamed so they cannot replace the dashboard's.
    expect(css.startsWith('@keyframes nj-preview-glow')).toBe(true);
    expect(css).not.toContain('@import');
    expect(css).toContain('@scope (.sd-frame) {\n:scope { background: #111 }\n:scope{--x:1}\n.status-row { color: red; content: "}" }\n@media (max-width: 600px) { :scope .status-top');
  });

  it("puts the owner's code on the page", () => {
    const { container } = render(
      <StatusView
        data={{
          title: 'Status',
          overall: 'operational',
          updated: Date.now() / 1000,
          css: '.status-row { color: red }',
          header_html: '<img src="https://example.com/logo.png" alt="Perked logo"><script>bad()</script>',
          footer_html: '<p>Email <a href="mailto:a@example.com">us</a></p>',
          services: [],
        }}
      />,
    );
    expect(screen.getByAltText('Perked logo')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'us' })).toHaveAttribute('href', 'mailto:a@example.com');
    expect(container.querySelector('script')).toBeNull();
    expect(container.querySelector('style')?.textContent).toBe('.status-row { color: red }');
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
    await waitFor(() => expect(document.title).toBe('2 down · Network · Nightjar'));
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
    // The list empties while the search loads, so wait until the rest has gone and the match is back.
    const result = await waitFor(() => {
      expect(screen.queryByText('Download Complete')).not.toBeInTheDocument();
      return screen.getByRole('button', { name: /bazarr failed/ });
    });
    fireEvent.click(result);
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
    fireEvent.click(screen.getByLabelText('Keep quiet'));
    // 23:00, an hour back with the arrow keys, then 30 minutes from the panel.
    const start = screen.getByRole('button', { name: 'Quiet hours start' });
    for (let i = 0; i < 4; i++) fireEvent.keyDown(start, { key: 'ArrowDown' });
    fireEvent.click(start);
    fireEvent.click(within(screen.getByRole('listbox', { name: 'Min' })).getByRole('option', { name: '30' }));
    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
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
    // The menu lives outside the button, so pressing an item must not count as a click away.
    const item = screen.getByRole('menuitem', { name: '4 hours' });
    fireEvent.mouseDown(item);
    fireEvent.click(item);
    expect(await screen.findByRole('button', { name: 'Unmute sonarr' })).toHaveTextContent('muted · 4h left');
    const sent = JSON.parse(fetchMock.mock.calls.find(([u]) => u === '/api/notifications/mute')![1]?.body as string);
    expect(sent).toEqual({ subject: 'unit:sonarr.service', label: 'sonarr', minutes: 240 });
    expect(screen.getByText('sonarr muted for 4 hours')).toBeInTheDocument();
  });

  it('keeps the mute menu on screen when its button is at the left edge, outside any clipping list', async () => {
    mockApi((url) => {
      if (url === '/api/notifications/summary') return json(summary);
    });
    const width = vi.spyOn(HTMLElement.prototype, 'offsetWidth', 'get').mockReturnValue(160);
    const height = vi.spyOn(HTMLElement.prototype, 'offsetHeight', 'get').mockReturnValue(180);
    wrap(
      <ul style={{ overflow: 'hidden' }}>
        <li>
          <MuteButton subject="unit:apt-daily.service" label="apt-daily" />
        </li>
      </ul>,
    );
    const button = await screen.findByRole('button', { name: 'Mute apt-daily' });
    button.getBoundingClientRect = () => ({ left: 20, right: 90, top: 300, bottom: 330, width: 70, height: 30, x: 20, y: 300, toJSON: () => ({}) });
    fireEvent.click(button);
    const menu = screen.getByRole('menu', { name: 'Mute apt-daily for' });
    expect(menu.parentElement).toBe(document.body);
    // Lined up with the button's left edge, since lining up its right edge would run off the window.
    expect(menu.style.left).toBe('20px');
    expect(menu.style.top).toBe('336px');
    expect(document.activeElement).toBe(screen.getByRole('menuitem', { name: '1 hour' }));
    width.mockRestore();
    height.mockRestore();
  });
});

describe('admin account', () => {
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const config = (account: { configured: boolean; username?: string }) => ({
    auth_required: true,
    authorized: false,
    status_page_enabled: false,
    status_page_title: '',
    account: { ...account, service: 'nightjar' },
  });

  it('creates the account with the setup code, checking the password first', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(json({ username: 'jay' }));
    const done = vi.fn();
    render(
      <MemoryRouter>
        <ConfigContext.Provider value={config({ configured: false })}>
          <CreateAdmin onDone={done} />
        </ConfigContext.Provider>
      </MemoryRouter>,
    );
    expect(screen.getByText('journalctl -u nightjar | grep setup_code | tail -1')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Read the getting started guide.' })).toHaveAttribute('href', '/docs/getting-started#create-the-admin-account');
    expect(screen.getByText(/Confused\?/)).toBeInTheDocument();
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
      <MemoryRouter>
        <ConfigContext.Provider value={config({ configured: true, username: 'jay' })}>
          <SignIn onDone={() => undefined} />
        </ConfigContext.Provider>
      </MemoryRouter>,
    );
    expect(screen.getByLabelText('Username')).toHaveValue('jay');
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'nope nope nope' } });
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Wrong username or password.');
    expect(screen.getByLabelText('Password')).toHaveValue('');
    expect(signedOut).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Forgot the password?' }));
    expect(screen.getByText('nightjar -reset-admin')).toBeInTheDocument();
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
      return new Response(JSON.stringify({ entries, latest: all.length, components: ['disk', 'web'], level: 'INFO', file: 'nightjar.log', kept: 3000 }), {
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
    expect(screen.getByText('nightjar.log')).toBeInTheDocument();

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
    fireEvent.click(await within(installed).findByRole('button', { name: 'Manage' }));
    const again = screen.getByRole('dialog', { name: 'Lidarr' });
    expect(within(again).getByRole('button', { name: 'Reinstall latest' })).toBeInTheDocument();
    expect(within(again).getByRole('button', { name: 'Remove…' })).toBeInTheDocument();
  });
});

describe('servers', () => {
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const readings = (temp: number | null) => ({
    version: 1,
    agent: 'v1',
    time: 1,
    system: { hostname: 'nas', platform: 'Linux', platform_release: '6.1', platform_version: 'debian 12', architecture: 'amd64', boot_time: 0, uptime_seconds: 3600, uptime_formatted: '', users: 0 },
    cpu: { percent: 12, cores: [10, 14], count: 2, load: [0.2, 0.1, 0.1], freq_mhz: 3000 },
    memory: { total: 8e9, used: 3e9, free: 5e9, available: 5e9, percent: 37.5 },
    swap: { total: 0, used: 0, free: 0, available: 0, percent: 0 },
    temperature: temp,
    fans: [],
    gpu: null,
    disks: [{ device: '/dev/sda1', mountpoint: '/', fstype: 'ext4', total: 1e11, used: 9.3e10, free: 7e9, percent: 93, is_nvme: false }],
    network: { bytes_recv_per_sec: 1000, bytes_sent_per_sec: 500 },
    disk_io: { read_per_sec: 0, write_per_sec: 0 },
    drives: [],
  });

  it('shows each server and adds one with the install commands', async () => {
    const hosts = [
      { id: 'a1', name: 'nas', address: 'http://192.168.0.20:9101', added: 1, status: 'online', readings: readings(84), history: [], problems: ['CPU at 84°C', '/ 93% full'] },
      {
        id: 'b2',
        name: 'backup',
        address: 'http://192.168.0.44:9101',
        added: 1,
        status: 'offline',
        error: 'no answer: timed out',
        last_seen: 1,
        readings: null,
        history: [],
        problems: ['Offline: timed out'],
      },
    ];
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/hosts' && (init?.method ?? 'GET') === 'GET')
        return json({
          hosts,
          port: 9101,
          agents: [
            { arch: 'amd64', file: 'nightjar-agent-linux-amd64', size: 7_700_000 },
            { arch: 'arm64', file: 'nightjar-agent-linux-arm64', size: 7_200_000 },
          ],
        });
      if (url === '/api/hosts/token') return json({ token: 'f'.repeat(48), port: 9101 });
      if (url === '/api/hosts' && init?.method === 'POST') return json({ error: 'no answer from http://192.168.0.50:9200: connection refused (is the agent running, on that port?)' }, 400);
      return json({}, 404);
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <ServersPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    const nas = await screen.findByRole('link', { name: 'nas: Online' });
    expect(within(nas).getByText('CPU at 84°C')).toBeInTheDocument();
    expect(within(nas).getByText('84')).toBeInTheDocument();
    const backup = screen.getByRole('link', { name: 'backup: Offline' });
    expect(within(backup).getByText(/timed out/)).toBeInTheDocument();
    // The offline chip would repeat the error right above it.
    expect(within(backup).queryByText(/^Offline:/)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Add a server' }));
    const sheet = screen.getByRole('dialog', { name: 'Add a server' });
    fireEvent.change(within(sheet).getByLabelText('Address'), { target: { value: '192.168.0.50:9200' } });
    // The agent comes from the dashboard itself; it is dropped on the server and installed from there.
    expect(within(sheet).getByRole('link', { name: /Download nightjar-agent-linux-amd64/ })).toHaveAttribute('href', '/api/agent/download?arch=amd64');
    expect(within(sheet).queryByText(/\b(scp|ssh) /)).not.toBeInTheDocument();
    expect(await within(sheet).findByText(`chmod +x nightjar-agent-linux-amd64 && sudo ./nightjar-agent-linux-amd64 install -token ${'f'.repeat(48)} -listen :9200`)).toBeInTheDocument();
    fireEvent.click(within(sheet).getByRole('button', { name: 'ARM64' }));
    expect(within(sheet).getByText(/sudo \.\/nightjar-agent-linux-arm64 install/)).toBeInTheDocument();
    expect(within(sheet).getByRole('link', { name: /Download nightjar-agent-linux-arm64/ })).toHaveAttribute('href', '/api/agent/download?arch=arm64');

    fireEvent.click(within(sheet).getByRole('button', { name: 'Connect' }));
    expect(await within(sheet).findByText(/connection refused/)).toBeInTheDocument();
    // An address without a scheme gets the one the agent serves: HTTPS unless that is turned off.
    expect(JSON.parse(fetchMock.mock.calls.find(([u, i]) => u === '/api/hosts' && i?.method === 'POST')![1]?.body as string)).toEqual({
      kind: 'agent',
      name: '',
      address: 'https://192.168.0.50:9200',
      token: 'f'.repeat(48),
    });
    fireEvent.click(within(sheet).getByLabelText('Encrypt with HTTPS'));
    expect(within(sheet).getByText(/install -token f+ -listen :9200 -tls=false$/)).toBeInTheDocument();
  });

  it('sends something with nothing installed to uptime checks, and lists the checks on a server', async () => {
    const host = { id: 'a1', name: 'nas', address: 'https://192.168.0.20:9101', added: 1, kind: 'agent', status: 'online', readings: null, history: [], problems: [] };
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const url = String(input);
      if (url === '/api/hosts') return json({ hosts: [host], port: 9101 });
      if (url === '/api/hosts/a1') return json({ host });
      if (url === '/api/hosts/token') return json({ token: 'f'.repeat(48), port: 9101 });
      if (url === '/api/services')
        return json({
          services: [svc({ name: 'rsync', target: 'nas.local:873', server: 'a1', latency_ms: 4 }), svc({ name: 'website', target: 'https://example.com' })],
        });
      return json({}, 404);
    });
    const { unmount } = render(
      <MemoryRouter>
        <ToastProvider>
          <ServersPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Add a server' }));
    const sheet = screen.getByRole('dialog', { name: 'Add a server' });
    expect(within(sheet).getByRole('link', { name: 'uptime check' })).toHaveAttribute('href', '/services');
    expect(within(sheet).queryByText(/Ping only/)).not.toBeInTheDocument();
    unmount();

    render(
      <MemoryRouter initialEntries={['/servers/a1']}>
        <ToastProvider>
          <ServicesProvider enabled>
            <Routes>
              <Route path="/servers/:id" element={<ServerDetail />} />
            </Routes>
          </ServicesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );
    const on = (await screen.findByText('Services on it')).closest('section') as HTMLElement;
    expect(await within(on).findByRole('link', { name: 'rsync' })).toHaveAttribute('href', '/services/rsync');
    expect(within(on).queryByText('website')).not.toBeInTheDocument();
    expect(within(on).getByRole('link', { name: /add a check/ })).toHaveAttribute('href', '/services?server=a1#add');
  });

  it('explains how to start when there are none', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(json({ hosts: [], port: 9101 }));
    render(
      <MemoryRouter>
        <ToastProvider>
          <ServersPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText('No other servers yet')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add a server' })).toBeInTheDocument();
  });
});

describe('analytics', () => {
  const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  const point = (t: number, over = {}) => ({
    t,
    up: 1,
    cpu: 20,
    cpu_max: 40,
    mem: 50,
    mem_max: 60,
    temp: 55,
    temp_max: 60,
    disk: 80,
    rx: 1000,
    tx: 500,
    latency: null,
    latency_max: null,
    minutes: 5,
    ...over,
  });
  const summary = (over = {}) => ({
    coverage: 1,
    uptime: 100,
    downtime_s: 0,
    incidents: 0,
    cpu_avg: 20,
    cpu_peak: 40,
    mem_avg: 50,
    mem_peak: 52,
    temp_avg: 55,
    temp_peak: 60,
    disk_now: 80,
    disk_change: 1,
    disk_full_days: null,
    rx_bytes: 1e9,
    tx_bytes: 5e8,
    latency_avg: null,
    latency_peak: null,
    ...over,
  });

  it('summarises every server over the chosen range', async () => {
    const hosts = [
      { id: 'local', name: 'home', kind: 'local', status: 'online', points: [point(1000), point(1300)], summary: summary({ uptime: null }), incidents: [] },
      {
        id: 'a1',
        name: 'nas',
        kind: 'agent',
        status: 'online',
        points: [point(1000, { temp: 81, temp_max: 88 }), point(1300)],
        summary: summary({ uptime: 98.5, incidents: 1, downtime_s: 1300, temp_avg: 70, temp_peak: 88, disk_now: 93, disk_full_days: 9.4 }),
        incidents: [{ start: 1700000000, end: 1700001300 }],
      },
    ];
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => {
      const range = new URL(String(input), 'http://x').searchParams.get('range');
      return json({ range: { name: range, label: range }, ranges: [], step: range === '7d' ? 1800 : 300, hosts, kept: true });
    });
    render(
      <MemoryRouter>
        <AnalyticsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText('Hottest')).toBeInTheDocument();
    expect(screen.getByText('88°C')).toBeInTheDocument();
    expect(screen.getByText('nas, 70°C on average')).toBeInTheDocument();
    expect(screen.getByText('full in ~9 days')).toBeInTheDocument();
    // This server has no uptime of its own: nothing is recorded while it is down.
    const home = screen.getAllByRole('row').find((r) => r.textContent?.startsWith('home'))!;
    expect(within(home).getAllByText('—').length).toBeGreaterThan(0);
    expect(screen.getByText('down 21m 40s')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: '7 days' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u) === '/api/analytics?range=7d')).toBe(true));
  });

  it('breaks lines where readings are missing and says when a disk fills', () => {
    const pts = withGaps(
      [
        { t: 0, v: 1 },
        { t: 300, v: 2 },
        { t: 1800, v: 3 },
      ],
      300,
    );
    expect(pts.map((p) => p.v)).toEqual([1, 2, null, 3]);
    expect(fullIn(0.4)).toBe('full within a day');
    expect(fullIn(12.4)).toBe('full in ~12 days');
    expect(fullIn(900)).toBe('full in over a year');
  });
});

describe('docs', () => {
  it('renders the Markdown the docs use, and nothing that could run', () => {
    const { container } = render(
      <MemoryRouter>
        <Markdown
          source={[
            '# Title',
            '',
            'Some **bold [link](/settings)** and `code`, press <kbd>Ctrl</kbd> <kbd>K</kbd>.',
            '',
            'An _italic phrase_, but snake_case_words and `A_B_C` stay as they are.',
            '',
            "## When it won't connect",
            '',
            '1. First step.',
            '2. Run this:',
            '',
            '   ```bash',
            '   make agent',
            '   ```',
            '',
            '3. Last.',
            '',
            '- a [web link](https://example.com)',
            '- a [bad link](javascript:alert(1))',
            '',
            '| A | B |',
            '| --- | --- |',
            '| `x` | y |',
            '',
            '> **Tip:** a note.',
            '',
            '<script>alert(1)</script> stays text.',
          ].join('\n')}
        />
      </MemoryRouter>,
    );
    expect(container.querySelector('h2#when-it-won-t-connect')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'link' })).toHaveAttribute('href', '/settings');
    expect(container.querySelectorAll('kbd')).toHaveLength(2);
    expect([...container.querySelectorAll('em')].map((e) => e.textContent)).toEqual(['italic phrase']);
    expect(screen.getByText(/snake_case_words/)).toBeInTheDocument();
    expect(plain('_Held_ means quiet hours')).toBe('Held means quiet hours');
    const ol = container.querySelector('ol')!;
    expect(ol.querySelectorAll(':scope > li')).toHaveLength(3);
    expect(ol.querySelector('pre code')?.textContent).toBe('make agent');
    expect(screen.getByRole('link', { name: 'web link' })).toHaveAttribute('target', '_blank');
    expect(screen.queryByRole('link', { name: 'bad link' })).not.toBeInTheDocument();
    expect(screen.getByText(/a bad link/)).toBeInTheDocument(); // its text stays, as plain text
    expect(container.querySelector('table td code')?.textContent).toBe('x');
    // A quote that opens with **Tip:** becomes a tip, labelled and with its own icon.
    expect(container.querySelector('aside.doc-note.tip .doc-note-label')?.textContent).toBe('Tip');
    expect(container.querySelector('aside.doc-note.tip')).toHaveTextContent('a note.');
    // Code blocks say their language and colour the command and its flags, as text.
    expect(container.querySelector('.doc-code-lang')?.textContent).toBe('Shell');
    expect(container.querySelector('.doc-code .tok-cmd')?.textContent).toBe('make');
    expect(container.querySelector('script')).toBeNull();
    expect(screen.getByText(/<script>alert\(1\)<\/script> stays text\./)).toBeInTheDocument();
  });

  it('has a page for every entry, and every link goes somewhere real', () => {
    const routes = [
      '/',
      '/services',
      '/incidents',
      '/plex/cleanup',
      '/incidents/maintenance',
      '/incidents/report',
      '/system',
      '/servers',
      '/servers/analytics',
      '/network',
      '/network/firewall',
      '/plex',
      '/qbittorrent',
      '/apps',
      '/updates',
      '/logs',
      '/notifications',
      '/notifications/rules',
      '/settings',
      '/setup',
      '/status-page',
      '/plugins',
      '/docs',
    ];
    const anchors = (s: string) =>
      new Set(
        (docBody(s) ?? '')
          .split('\n')
          .filter((l) => /^#{2,3}\s/.test(l))
          .map((l) => slug(plain(l.replace(/^#+\s+/, '')))),
      );
    const problems: string[] = [];
    for (const d of DOCS) {
      const body = docBody(d.slug);
      if (!body) {
        problems.push(`${d.slug}: no file`);
        continue;
      }
      if (body.split('\n')[0] !== `# ${d.title}`) problems.push(`${d.slug}: its first line should be "# ${d.title}"`);
      for (const [, href] of body.matchAll(/\]\(([^)]+)\)/g)) {
        if (/^https?:/.test(href)) continue;
        const [path, hash] = href.split('#');
        const doc = path.match(/^\/docs\/(.+)$/)?.[1];
        if (doc) {
          if (!DOCS.some((x) => x.slug === doc)) problems.push(`${d.slug}: links to a missing page ${href}`);
          else if (hash && !anchors(doc).has(hash)) problems.push(`${d.slug}: links to a missing section ${href}`);
        } else if (!routes.includes(path)) problems.push(`${d.slug}: links to an unknown page ${href}`);
        else if (path === '/settings' && hash && !['sections', 'appearance'].includes(hash)) problems.push(`${d.slug}: links to an unknown part of settings ${href}`);
      }
    }
    const files = Object.keys(import.meta.glob('../src/docs/*.md')).map((f) => f.replace(/^.*\/(.*)\.md$/, '$1'));
    for (const f of files) if (!DOCS.some((d) => d.slug === f)) problems.push(`${f}.md is not listed in docs/index.ts`);
    expect(problems).toEqual([]);
  });

  it('opens on a home of every guide, and copies code', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    render(
      <MemoryRouter initialEntries={['/docs']}>
        <Routes>
          <Route path="/docs" element={<DocsPage />} />
          <Route path="/docs/:slug" element={<DocsPage />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { level: 1, name: /Everything Nightjar does/ })).toBeInTheDocument();
    for (const d of DOCS) expect(screen.getAllByRole('link', { name: new RegExp(d.title.replace(/[()*]/g, '.')) }).length).toBeGreaterThan(0);
    fireEvent.click(screen.getAllByRole('link', { name: /Getting started.*Install Nightjar, create/ })[0]);
    expect(screen.getByRole('heading', { level: 1, name: 'Getting started' })).toBeInTheDocument();
    fireEvent.click(screen.getAllByRole('button', { name: 'Copy to clipboard' })[0]);
    await waitFor(() => expect(writeText).toHaveBeenCalledWith('cp env.recommended .env\nmake\n./bin/nightjar'));
    expect(await screen.findByRole('button', { name: 'Copied' })).toBeInTheDocument();
  });

  it('searches every page and opens the section', async () => {
    expect(searchDocs('setup code')[0].section.doc.slug).toBe('getting-started');
    expect(searchDocs('authorized_keys')[0].section.doc.slug).toBe('servers');
    render(
      <MemoryRouter initialEntries={['/docs/servers']}>
        <Routes>
          <Route path="/docs/:slug" element={<DocsPage />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(screen.getByRole('heading', { level: 1, name: 'Watching other servers' })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /Next →\s*Attacks/ })).toHaveAttribute('href', '/docs/attacks');
    expect(screen.getByText(/\d+ min read/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Search the docs'), { target: { value: 'quiet hours' } });
    const hit = await screen.findAllByRole('link', { name: /Quiet hours/ });
    expect(hit[0]).toHaveAttribute('href', '/docs/notifications#quiet-hours');
    fireEvent.change(screen.getByLabelText('Search the docs'), { target: { value: 'zzqqxx' } });
    expect(screen.getByText(/Nothing matches/)).toBeInTheDocument();
  });
});

describe('incidents', () => {
  it('leads with the likely cause, and opens the newest timeline', async () => {
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ incidents: demoIncidents(30), days: 7 }), { headers: { 'Content-Type': 'application/json' } }));
    render(
      <MemoryRouter>
        <IncidentsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText(/the kernel stopped rsync to free some/)).toBeInTheDocument();
    expect(screen.getByText('Ongoing')).toBeInTheDocument();
    expect(screen.getAllByText('Fixed by Nightjar').length).toBeGreaterThan(1);
    // The newest incident's timeline is open, with its cause marked; the others open on request.
    expect(screen.getByText('the likely cause')).toBeInTheDocument();
    expect(screen.queryByText('Repair of nas-backup failed')).not.toBeInTheDocument();
    fireEvent.click(screen.getAllByRole('button', { name: /Show the timeline/ })[0]);
    expect(screen.getByText('Repair of nas-backup failed')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /nas-backup/ })).toHaveAttribute('href', '/services/nas-backup');
  });
});

describe('teaching and telling', () => {
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

  it('takes feedback on a cause, and publishes a notice drafted for visitors', async () => {
    const list = demoIncidents(30);
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url.includes('/feedback')) return json({ feedback: JSON.parse(init?.body as string) });
      if (url.includes('/notice')) return json({ notice: JSON.parse(init?.body as string) });
      return json({ incidents: list, days: 7 });
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <IncidentsPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    await screen.findByText(/the kernel stopped rsync/);
    const memory = screen.getByText(/the kernel stopped rsync/).closest('article')!;
    fireEvent.click(within(memory).getByRole('button', { name: 'No' }));
    fireEvent.change(within(memory).getByLabelText('A note'), { target: { value: 'it was the backup job' } });
    fireEvent.click(within(memory).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u).endsWith(`/api/incidents/${list[1].id}/feedback`))).toBe(true));
    const fb = fetchMock.mock.calls.find(([u]) => String(u).includes('/feedback'))!;
    expect(JSON.parse(fb[1]?.body as string)).toEqual({ verdict: 'wrong', actual: '', note: 'it was the backup job' });

    const plex = screen.getByText(/so programs could not write/).closest('article')!;
    fireEvent.click(within(plex).getByRole('button', { name: 'Tell visitors on the status page' }));
    expect(within(plex).getByLabelText('Notice title')).toHaveValue('Movies & TV outage');
    expect((within(plex).getByLabelText('Notice text') as HTMLTextAreaElement).value).toMatch(/fixed automatically/);
    fireEvent.click(within(plex).getByRole('button', { name: 'Publish' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u).includes('/notice'))).toBe(true));
    const notice = fetchMock.mock.calls.find(([u]) => String(u).includes('/notice'))!;
    expect(JSON.parse(notice[1]?.body as string)).toMatchObject({ title: 'Movies & TV outage', published: true });
  });

  it('plans a weekly maintenance window over chosen checks', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/services') return json({ services: [{ name: 'nas', status: 'up', recent: [] }] });
      if (init?.method === 'POST') return json({ window: { ...JSON.parse(init.body as string), id: 'abc' } }, 201);
      return json({ windows: [], upcoming: [] });
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
    expect(await screen.findByText('No maintenance planned')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Plan maintenance' }));
    fireEvent.change(screen.getByLabelText('Title'), { target: { value: 'Nightly backups' } });
    fireEvent.click(screen.getByRole('button', { name: 'Every week' }));
    fireEvent.click(screen.getByRole('button', { name: 'Mon' }));
    fireEvent.change(screen.getByLabelText('From'), { target: { value: '03:00' } });
    fireEvent.change(screen.getByLabelText('To'), { target: { value: '03:30' } });
    fireEvent.click(await screen.findByRole('button', { name: 'nas' }));
    fireEvent.click(screen.getByRole('button', { name: 'Add window' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([, i]) => i?.method === 'POST')).toBe(true));
    const post = fetchMock.mock.calls.find(([, i]) => i?.method === 'POST')!;
    expect(JSON.parse(post[1]?.body as string)).toMatchObject({ title: 'Nightly backups', days: [0, 1], from: '03:00', to: '03:30', checks: ['nas'], public: true });
  });

  it('shows the week as the report sums it up, and sends it on request', async () => {
    const report = {
      from: 0,
      to: 0,
      checks: [{ name: 'nas', uptime: 97.5 }],
      overall: 97.5,
      incidents: 2,
      open: 0,
      down: 900,
      causes: [{ name: 'disk full', n: 2 }],
      top: [{ id: 1, title: 'plex is down', cause: 'The disk was full', started: 1, length: 600, open: false }],
      repairs: 2,
      fixed: 1,
      alerts: 9,
      noisiest: [{ name: 'tautulli', n: 6 }],
      forecasts: ['The disk is 91% full and growing 1.0% a day: full in about 9 days'],
    };
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async (input) => (String(input) === '/api/report/send' ? json({ sent: true }) : json({ report, enabled: true, at: 'Mon 09:00', next: 1 })));
    render(
      <MemoryRouter>
        <ToastProvider>
          <WeeklyReportPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText('97.50%', { selector: '.readout-value' })).toBeInTheDocument();
    expect(screen.getByText(/full in about 9 days/)).toBeInTheDocument();
    expect(screen.getByText('The disk was full.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Send it now' }));
    expect(await screen.findByText('Report sent')).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([u, i]) => String(u) === '/api/report/send' && i?.method === 'POST')).toBe(true);
  });
});

describe('media cleanup', () => {
  it('lists what nobody watched, says when it leaves, and keeps one on request', async () => {
    const view = { ...structuredClone(demoCleanup()), enabled: false };
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (_input, init) => {
      const body = init?.method === 'PUT' ? { ...view, items: view.items.filter((i) => i.key !== 'k2'), kept: [...view.kept, { key: 'k2', title: 'Cats', at: 1 }] } : view;
      return new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <CleanupPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText('Waterworld (1995)')).toBeInTheDocument();
    expect(screen.getByText(/This is a preview of what would be listed; nothing is removed/)).toBeInTheDocument();
    expect(screen.getByText(/seeding to ratio 1.00: at 0.64/)).toBeInTheDocument();
    const cats = screen.getByText('Cats (2019)').closest('tr')!;
    fireEvent.click(within(cats).getByRole('button', { name: 'Keep' }));
    expect(await screen.findByText('Cats is kept')).toBeInTheDocument();
    expect(fetchMock.mock.calls.some(([u, i]) => String(u) === '/api/cleanup/k2/keep' && i?.method === 'PUT')).toBe(true);
    expect(screen.queryByText('Cats (2019)')).not.toBeInTheDocument();
  });
});

describe('two-factor and backups', () => {
  const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  const account = { auth_required: true, authorized: true, status_page_enabled: false, status_page_title: '', account: { configured: true, username: 'jay' } };

  it('asks for the code after the password when two-factor sign-in is on', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (_input, init) => {
      const body = JSON.parse(init?.body as string);
      if (!body.code) return json({ error: 'Enter the code from your authenticator app', need_code: true, fields: { code: 'needed' } }, 401);
      return body.code === '123456' ? json({ username: 'jay' }) : json({ error: 'that code is not right', fields: { code: 'x' } }, 401);
    });
    const done = vi.fn();
    render(
      <MemoryRouter>
        <ConfigContext.Provider value={account}>
          <SignIn onDone={done} />
        </ConfigContext.Provider>
      </MemoryRouter>,
    );
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'correct horse battery' } });
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    const code = await screen.findByLabelText('Code from your authenticator app');
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    fireEvent.change(code, { target: { value: '123456' } });
    fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
    await waitFor(() => expect(done).toHaveBeenCalled());
    expect(JSON.parse(fetchMock.mock.calls[1][1]?.body as string)).toMatchObject({ username: 'jay', password: 'correct horse battery', code: '123456' });
  });

  it('sets up two-factor sign-in and shows the recovery codes once', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async (input) =>
        String(input).endsWith('/begin') ? json({ secret: 'JBSWY3DPEHPK3PXP', link: 'otpauth://totp/Nightjar:jay?secret=JBSWY3DPEHPK3PXP' }) : json({ recovery: ['aaaaa-bbbbb', 'ccccc-ddddd'] }),
      );
    render(
      <ToastProvider>
        <ConfigContext.Provider value={account}>
          <AccountSection />
        </ConfigContext.Provider>
      </ToastProvider>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Set it up' }));
    expect(await screen.findByText('JBSW Y3DP EHPK 3PXP')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Code from the app'), { target: { value: '654321' } });
    // Turning it on asks for the password too.
    expect(screen.getByRole('button', { name: 'Turn on' })).toBeDisabled();
    fireEvent.change(screen.getByLabelText('Password'), { target: { value: 'correct horse battery' } });
    fireEvent.click(screen.getByRole('button', { name: 'Turn on' }));
    expect(await screen.findByText(/aaaaa-bbbbb/)).toBeInTheDocument();
    expect(screen.getByText(/not shown again/)).toBeInTheDocument();
    expect(JSON.parse(fetchMock.mock.calls[1][1]?.body as string)).toEqual({ password: 'correct horse battery', code: '654321' });
  });

  it('restores a backup once confirmed', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(json({ restored: { checks: 2, settings: 3, parts: 4 }, restarting: true }));
    render(
      <ToastProvider>
        <BackupPanel />
      </ToastProvider>,
    );
    const file = new File([JSON.stringify({ nightjar: '1.0', made: 1700000000, checks: [{}, {}], settings: {} })], 'nightjar-backup.json', { type: 'application/json' });
    fireEvent.change(screen.getByLabelText('Backup file'), { target: { files: [file] } });
    expect(await screen.findByText((_, el) => el?.tagName === 'SPAN' && /with 2 checks\?$/.test(el.textContent ?? ''))).toBeInTheDocument();
    expect(fetchMock).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole('button', { name: 'Restore' }));
    expect(await screen.findByText(/Nightjar is restarting/)).toBeInTheDocument();
    expect(String(fetchMock.mock.calls[0][0])).toBe('/api/backup/restore');
  });
});

describe('appearance', () => {
  // Look shows the look in force, as the rest of the dashboard sees it.
  const Look = () => {
    const a = useAppearance();
    return <output aria-label="look">{`${a.style} ${a.charts || 'own'} ${a.density}`}</output>;
  };
  const config = { auth_required: true, authorized: true, status_page_enabled: false, status_page_title: '', appearance: { style: 'matrix', charts: '', density: 'comfortable', css: '' } } as const;

  it('previews a change everywhere and saves it, with nothing to save until something changes', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response(JSON.stringify({ appearance: {} }), { headers: { 'Content-Type': 'application/json' } }));
    const reloaded = vi.fn();
    window.addEventListener('config-changed', reloaded);
    const { unmount } = render(
      <ToastProvider>
        <ConfigContext.Provider value={{ ...config, appearance: { ...config.appearance } }}>
          <AppearancePanel />
          <Look />
        </ConfigContext.Provider>
      </ToastProvider>,
    );
    const save = screen.getByRole('button', { name: 'Save appearance' });
    expect(save).toBeDisabled();
    fireEvent.click(screen.getByRole('radio', { name: /Cards/ }));
    expect(screen.getByLabelText('look')).toHaveTextContent('cards own comfortable');
    fireEvent.click(screen.getByRole('button', { name: 'Compact' }));
    fireEvent.change(screen.getByLabelText(/Your own CSS/), { target: { value: '.readout-value { font-weight: 300 }' } });
    fireEvent.click(save);
    await waitFor(() => expect(reloaded).toHaveBeenCalled());
    const [url, init] = fetchMock.mock.calls[0];
    expect(String(url)).toContain('/api/appearance');
    expect(JSON.parse(init?.body as string)).toEqual({ style: 'cards', charts: '', density: 'compact', css: '.readout-value { font-weight: 300 }' });
    expect(await screen.findByText('Appearance saved')).toBeInTheDocument();

    // Discarding goes back to the saved look; so does leaving Settings.
    fireEvent.click(screen.getByRole('button', { name: 'Discard' }));
    expect(screen.getByLabelText('look')).toHaveTextContent('matrix own comfortable');
    fireEvent.click(screen.getByRole('button', { name: 'Dots' }));
    expect(screen.getByLabelText('look')).toHaveTextContent('matrix dots comfortable');
    unmount();
    window.removeEventListener('config-changed', reloaded);
  });

  it('draws charts as lines, or in dots when asked', () => {
    const points = Array.from({ length: 40 }, (_, i) => ({ t: 1000 + i * 10, v: 20 + (i % 7) }));
    const chart = (charts: '' | 'dots') => (
      <ConfigContext.Provider value={{ ...config, appearance: { ...config.appearance, charts } }}>
        <TrendChart series={[{ label: 'CPU', color: 'var(--series-1)', points }]} height={60} compact format={String} />
      </ConfigContext.Provider>
    );
    const { container, rerender } = render(chart(''));
    expect(container.querySelector('pattern')).toBeNull();
    expect(container.querySelector('linearGradient')).not.toBeNull();
    rerender(chart('dots'));
    expect(container.querySelector('pattern')).not.toBeNull();
    expect(container.querySelector('linearGradient')).toBeNull();
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

describe('log diagnosis', () => {
  it('says what went wrong, the likely cause first, with the fix and the line', () => {
    render(
      <Diagnosis
        findings={[
          {
            id: 'port_in_use',
            title: 'Port 8989 is already in use',
            explain: 'Another program holds it.',
            fix: "See what holds it with `sudo ss -ltnp 'sport = :8989'`.",
            severity: 'critical',
            line: 'address already in use',
            count: 1,
            known: true,
          },
          { id: 'repeated', title: 'Indexer 9 returned error 429', explain: 'Seen 3 times.', severity: 'warning', line: 'Indexer 9 returned error 429', count: 3, known: false },
        ]}
      />,
    );
    expect(screen.getByText('Likely cause')).toBeInTheDocument();
    expect(screen.getByText('Port 8989 is already in use')).toBeInTheDocument();
    expect(screen.getByText("sudo ss -ltnp 'sport = :8989'").tagName).toBe('CODE');
    expect(screen.getByText('×3')).toBeInTheDocument();
    expect(screen.getAllByText('The log line')).toHaveLength(2);
  });

  it('names no likely cause when nothing was recognised', () => {
    render(<Diagnosis findings={[{ id: 'repeated', title: 'Something failed', explain: 'Seen twice.', severity: 'warning', count: 2, known: false }]} />);
    expect(screen.queryByText('Likely cause')).not.toBeInTheDocument();
    expect(screen.getByText('Something failed')).toBeInTheDocument();
  });
});

describe('plugins', () => {
  const json = (body: unknown) => new Response(JSON.stringify(body), { headers: { 'Content-Type': 'application/json' } });
  afterEach(() => vi.restoreAllMocks());
  const base = { version: '1.0.0', description: '', run: 'run.sh', dir: '/p', values: {}, secrets_set: [] as string[] };
  const list = () => [
    {
      ...base,
      id: 'dns',
      name: 'DNS lookup',
      provides: ['check'],
      check: { argument: 'Host name' },
      enabled: false,
      settings: [{ key: 'TOKEN', label: 'Token', type: 'secret', required: true }],
      missing: ['Token'],
    },
    { ...base, id: 'broken', name: 'Broken', provides: ['card'], enabled: false, problem: 'run.sh is missing' },
  ];

  it('turns a plugin on with its settings, and tries it', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/plugins') return json({ dir: '/opt/nightjar/plugins', plugins: list() });
      if (url === '/api/plugins/dns' && init?.method === 'PUT') {
        const c = JSON.parse(init.body as string);
        return json({ plugin: { ...list()[0], enabled: !!c.enabled, secrets_set: ['TOKEN'], missing: [] } });
      }
      if (url === '/api/plugins/dns/try') return json({ check: { up: true, detail: `${JSON.parse(init!.body as string).argument} is 1.2.3.4`, latency_ms: 9 } });
      return json({});
    });
    render(
      <MemoryRouter>
        <ToastProvider>
          <PluginsPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText('DNS lookup')).toBeInTheDocument();
    expect(screen.getByText('/opt/nightjar/plugins')).toBeInTheDocument();
    expect(screen.getByRole('note')).toHaveTextContent('run.sh is missing');
    expect(screen.getByRole('switch', { name: 'Turn Broken on' })).toBeDisabled();
    expect(screen.getByText('Fill in Token to turn it on.')).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText('Host name'), { target: { value: 'example.com' } });
    fireEvent.click(screen.getByRole('button', { name: 'Try the check' }));
    expect(await screen.findByText('· example.com is 1.2.3.4', { exact: false })).toBeInTheDocument();

    fireEvent.change(screen.getByLabelText(/^Token/), { target: { value: 'abc' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(screen.getByLabelText(/^Token/)).toHaveAttribute('placeholder', 'Saved. Type to replace it'));
    fireEvent.click(screen.getByRole('switch', { name: 'Turn DNS lookup on' }));
    await waitFor(() => expect(screen.getByRole('switch', { name: 'Turn DNS lookup off' })).toBeChecked());
    const puts = fetchMock.mock.calls.filter(([u, i]) => u === '/api/plugins/dns' && i?.method === 'PUT').map(([, i]) => JSON.parse(i!.body as string));
    expect(puts).toEqual([{ settings: { TOKEN: 'abc' } }, { enabled: true }]);
    expect(screen.getByText(/add a check of type/)).toHaveTextContent('dns:argument');
  });

  it('shows a plugin card', () => {
    render(<PluginCardBody card={{ plugin: 'speed', title: 'Internet', updated: 0, items: [{ label: 'Down', value: '930 Mbps', tone: 'good' }] }} />);
    expect(screen.getByText('930 Mbps')).toHaveClass('tone-good');
    render(<PluginCardBody card={{ plugin: 'x', title: 'X', updated: 0, error: 'it exited with status 1' }} />);
    expect(screen.getByText('it exited with status 1')).toBeInTheDocument();
  });
});

describe('SSH for a watched server', () => {
  it('picks a key, shows its public line, saves the login and tests it', async () => {
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    const host = {
      id: 'h1',
      name: 'vps',
      address: 'https://vps.example.net:9101',
      added: 0,
      kind: 'agent',
      status: 'online',
      readings: null,
      history: [],
      problems: [],
    };
    let ssh: unknown = null;
    const calls: string[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      calls.push(`${method} ${url}`);
      if (url === '/api/ssh/keys')
        return json({
          keys: [{ path: '/srv/nightjar/ssh/nightjar', name: 'nightjar', type: 'ED25519', fingerprint: 'SHA256:abc', public: 'ssh-ed25519 AAAATEST nightjar@box', own: true }],
        });
      const saved = () => ({
        ...host,
        ssh: { port: 22, user: 'admin', key: '/srv/nightjar/ssh/nightjar' },
        ssh_command: 'ssh -i /srv/nightjar/ssh/nightjar admin@vps.example.net',
        ssh_check: { ok: true, at: Date.now() / 1000 - 120, as: 'admin@vps', host_key: 'ED25519 SHA256:hostkey' },
      });
      if (url === '/api/hosts/h1/ssh' && method === 'PUT') {
        ssh = JSON.parse(String(init?.body));
        return json({ host: saved(), test: { ok: true, output: 'admin@vps\n first login' } });
      }
      if (url === '/api/hosts/h1/ssh/test') return json({ output: 'admin@vps\n up 3 days' });
      if (url === '/api/hosts/h1') return json({ host: ssh ? saved() : host });
      return json({}, 404);
    });
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
    fireEvent.click(await screen.findByRole('button', { name: 'Set up SSH…' }));
    // It asks whether the key is on the server; the host defaults to the server's own address.
    expect(await screen.findByText('Is nightjar on vps already?')).toBeInTheDocument();
    expect(screen.getByLabelText('Host')).toHaveAttribute('placeholder', 'vps.example.net');
    // Adding it by hand shows the public line to add, and nothing else to run.
    expect(screen.queryByText('ssh-ed25519 AAAATEST nightjar@box')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'I’ll add it' }));
    expect(screen.getByText('ssh-ed25519 AAAATEST nightjar@box')).toBeInTheDocument();
    expect(screen.queryByText(/mkdir/)).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('User'), { target: { value: 'admin' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save and test' }));
    await waitFor(() => expect(ssh).toEqual({ key: '/srv/nightjar/ssh/nightjar', user: 'admin', port: 22 }));
    // Saved and tried at once: whether it logs in, when, and the host key it trusts.
    expect(await screen.findByText('admin@vps')).toBeInTheDocument();
    expect(screen.getByText(/Logs in as/)).toBeInTheDocument();
    expect(screen.getByText('worked 2m ago')).toBeInTheDocument();
    expect(screen.getByText('ED25519 SHA256:hostkey')).toBeInTheDocument();
    expect(screen.getByText('ssh -i /srv/nightjar/ssh/nightjar admin@vps.example.net')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Restart agent' })).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Test again' }));
    await waitFor(() => expect(calls).toContain('POST /api/hosts/h1/ssh/test'));
    // Rebooting asks first.
    fireEvent.click(screen.getByRole('button', { name: 'Reboot…' }));
    expect(screen.getByText('Reboot vps?')).toBeInTheDocument();
    expect(calls).not.toContain('POST /api/hosts/h1/ssh/reboot');
  });

  it('adds a private key you already have, and never shows it back', async () => {
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    const host = { id: 'h1', name: 'vps', address: 'https://vps.example.net:9101', added: 0, kind: 'agent', status: 'online', readings: null, history: [], problems: [] };
    const posted: unknown[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/ssh/keys' && init?.method === 'POST') {
        posted.push(JSON.parse(String(init.body)));
        return json({ key: { path: '/srv/nightjar/ssh/laptop', name: 'laptop', type: 'ED25519', fingerprint: 'SHA256:x', public: 'ssh-ed25519 AAAAMINE laptop@nightjar', own: true } });
      }
      if (url === '/api/ssh/keys') return json({ keys: [] });
      if (url === '/api/hosts/h1') return json({ host });
      return json({}, 404);
    });
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
    fireEvent.click(await screen.findByRole('button', { name: 'Set up SSH…' }));
    // With no keys on the machine, bringing your own is the first choice.
    const box = await screen.findByLabelText('Private key');
    fireEvent.change(screen.getByLabelText('Name for the key'), { target: { value: 'laptop' } });
    fireEvent.change(box, { target: { value: '-----BEGIN OPENSSH PRIVATE KEY-----\nSECRET\n-----END OPENSSH PRIVATE KEY-----' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add key' }));
    // A key just added is on no server yet, so the password is asked for to put it there.
    expect(await screen.findByLabelText('Password on the server')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add key, save and test' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'I’ll add it' }));
    expect(screen.getByText('ssh-ed25519 AAAAMINE laptop@nightjar')).toBeInTheDocument();
    expect(posted).toEqual([{ name: 'laptop', private: '-----BEGIN OPENSSH PRIVATE KEY-----\nSECRET\n-----END OPENSSH PRIVATE KEY-----' }]);
    expect(screen.queryByText(/SECRET/)).not.toBeInTheDocument();
  });

  it('adds a server that is only an SSH login, with a page of its own', async () => {
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    const key = { path: '/srv/nightjar/ssh/laptop', name: 'laptop', type: 'ED25519', fingerprint: 'SHA256:x', public: 'ssh-ed25519 AAAAMINE laptop', own: true };
    const vps = {
      id: 'v1',
      name: 'USVA-2',
      address: '203.0.113.40',
      added: 0,
      kind: 'ssh',
      status: 'online',
      readings: null,
      history: [],
      problems: [],
      ssh: { host: '203.0.113.40', port: 655, user: 'root', key: key.path },
      ssh_command: 'ssh -i /srv/nightjar/ssh/laptop -p 655 root@203.0.113.40',
    };
    let added: unknown;
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/hosts' && init?.method === 'POST') {
        added = JSON.parse(String(init.body));
        return json({ host: vps });
      }
      if (url === '/api/hosts') return json({ hosts: [], port: 9101 });
      if (url === '/api/hosts/token') return json({ token: 'f'.repeat(48), port: 9101 });
      if (url === '/api/ssh/keys') return json({ keys: [key] });
      if (url === '/api/hosts/v1') return json({ host: vps });
      if (url === '/api/services') return json({ services: [svc({ name: 'USVA-2', target: '203.0.113.40:655', server: 'v1' })] });
      return json({}, 404);
    });
    const { unmount } = render(
      <MemoryRouter>
        <ToastProvider>
          <ServersPage />
        </ToastProvider>
      </MemoryRouter>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Add a server' }));
    const sheet = screen.getByRole('dialog', { name: 'Add a server' });
    fireEvent.click(within(sheet).getByLabelText(/SSH only/));
    expect(within(sheet).queryByText(/Download nightjar-agent/)).not.toBeInTheDocument();
    fireEvent.change(within(sheet).getByLabelText('Address'), { target: { value: '203.0.113.40' } });
    fireEvent.change(within(sheet).getByLabelText('Name'), { target: { value: 'USVA-2' } });
    expect(await within(sheet).findByText('Is laptop on the server already?')).toBeInTheDocument();
    fireEvent.change(within(sheet).getByLabelText('Port'), { target: { value: '655' } });
    fireEvent.click(within(sheet).getByRole('button', { name: 'Add server' }));
    await waitFor(() => expect(added).toEqual({ kind: 'ssh', name: 'USVA-2', ssh: { key: key.path, user: 'root', port: 655, host: '203.0.113.40' } }));
    unmount();

    render(
      <MemoryRouter initialEntries={['/servers/v1']}>
        <ToastProvider>
          <ServicesProvider enabled>
            <Routes>
              <Route path="/servers/:id" element={<ServerDetail />} />
            </Routes>
          </ServicesProvider>
        </ToastProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByText('ssh -i /srv/nightjar/ssh/laptop -p 655 root@203.0.113.40')).toBeInTheDocument();
    // No agent: nothing to restart, and its login is all it has, so it cannot be forgotten.
    expect(screen.queryByRole('button', { name: 'Restart agent' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Forget…' })).not.toBeInTheDocument();
    expect(screen.queryByText('CPU and memory')).not.toBeInTheDocument();
    const on = screen.getByText('Services on it').closest('section') as HTMLElement;
    expect(await within(on).findByRole('link', { name: 'USVA-2' })).toBeInTheDocument();
    expect(screen.queryByLabelText('New token')).not.toBeInTheDocument();
  });

  it('reinstalls the agent over SSH from the server page', async () => {
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    const host = {
      id: 'h1',
      name: 'nas',
      address: 'https://192.168.0.20:9101',
      added: 0,
      kind: 'agent',
      status: 'offline',
      readings: null,
      history: [],
      problems: [],
      ssh: { port: 22, user: 'root', key: '/srv/nightjar/ssh/nightjar' },
      ssh_command: 'ssh -i /srv/nightjar/ssh/nightjar root@192.168.0.20',
    };
    const calls: string[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      calls.push(`${init?.method ?? 'GET'} ${url}`);
      if (url === '/api/hosts/h1/ssh/reinstall-agent') return json({ output: "Reinstalling with this machine's agent settings." });
      if (url === '/api/hosts/h1') return json({ host });
      return json({}, 404);
    });
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
    fireEvent.click(await screen.findByRole('button', { name: 'Reinstall agent' }));
    expect(await screen.findByText('Agent reinstalled')).toBeInTheDocument();
    expect(screen.getByText(/Reinstalling with this machine/)).toBeInTheDocument();
    expect(calls).toContain('POST /api/hosts/h1/ssh/reinstall-agent');
  });

  it('puts the key on the server with a password it does not keep', async () => {
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    const host = { id: 'h1', name: 'vps', address: 'https://vps.example.net:9101', added: 0, kind: 'agent', status: 'online', readings: null, history: [], problems: [] };
    const sent: unknown[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/ssh/authorize') {
        const b = JSON.parse(String(init?.body));
        if (b.password === 'wrong') return json({ error: 'the server turned the password down' }, 422);
        sent.push(b);
        return json({ ok: true });
      }
      if (url === '/api/hosts/h1/ssh' && init?.method === 'PUT') {
        sent.push('save');
        return json({ host: { ...host, ssh: { port: 22, user: 'root', key: '/srv/k' } }, test: { ok: true, output: 'root@vps' } });
      }
      if (url === '/api/ssh/keys') return json({ keys: [{ path: '/srv/k', name: 'k', type: 'ED25519', fingerprint: 'SHA256:x', public: 'ssh-ed25519 AAAAK k', own: true }] });
      if (url === '/api/hosts/h1') return json({ host });
      return json({}, 404);
    });
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
    fireEvent.click(await screen.findByRole('button', { name: 'Set up SSH…' }));
    // One step: the password puts the key on, then the login is saved and tried.
    fireEvent.click(await screen.findByRole('button', { name: 'Add it with a password' }));
    const pw = screen.getByLabelText('Password on the server');
    expect(pw).toHaveAttribute('type', 'password');
    // A refused password stops there, with nothing saved.
    fireEvent.change(pw, { target: { value: 'wrong' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add key, save and test' }));
    expect(await screen.findByText(/Could not add the key: the server turned the password down/)).toBeInTheDocument();
    expect(sent).toEqual([]);
    fireEvent.change(pw, { target: { value: 'hunter2' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add key, save and test' }));
    expect(await screen.findByText('SSH login saved')).toBeInTheDocument();
    expect(sent).toEqual([{ id: 'h1', login: { key: '/srv/k', user: 'root', port: 22 }, password: 'hunter2' }, 'save']);
    expect(screen.queryByLabelText('Password on the server')).not.toBeInTheDocument();
  });

  it('shows a host key nobody confirmed, and sends the password only once it is confirmed', async () => {
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    const host = { id: 'h1', name: 'vps', address: 'https://vps.example.net:9101', added: 0, kind: 'agent', status: 'online', readings: null, history: [], problems: [] };
    const sent: unknown[] = [];
    let trusted = false;
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/ssh/authorize') {
        if (!trusted) return json({ error: 'Nobody has confirmed this server’s host key yet', host_key: 'ED25519 SHA256:abc' }, 422);
        sent.push(JSON.parse(String(init?.body)));
        return json({ ok: true });
      }
      if (url === '/api/ssh/trust') {
        const b = JSON.parse(String(init?.body));
        sent.push({ trust: b.host_key });
        trusted = true;
        return json({ ok: true });
      }
      if (url === '/api/hosts/h1/ssh' && init?.method === 'PUT') {
        sent.push('save');
        return json({ host: { ...host, ssh: { port: 22, user: 'root', key: '/srv/k' } }, test: { ok: true, output: 'root@vps' } });
      }
      if (url === '/api/ssh/keys') return json({ keys: [{ path: '/srv/k', name: 'k', type: 'ED25519', fingerprint: 'SHA256:x', public: 'ssh-ed25519 AAAAK k', own: true }] });
      if (url === '/api/hosts/h1') return json({ host });
      return json({}, 404);
    });
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
    fireEvent.click(await screen.findByRole('button', { name: 'Set up SSH…' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Add it with a password' }));
    fireEvent.change(screen.getByLabelText('Password on the server'), { target: { value: 'hunter2' } });
    fireEvent.click(screen.getByRole('button', { name: 'Add key, save and test' }));
    // The key to compare on the server, and how to see it there; nothing is sent or saved yet.
    expect(await screen.findByText('ED25519 SHA256:abc')).toBeInTheDocument();
    expect(screen.getByText('ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub')).toBeInTheDocument();
    expect(sent).toEqual([]);
    fireEvent.click(screen.getByRole('button', { name: 'It matches, add the key' }));
    expect(await screen.findByText('SSH login saved')).toBeInTheDocument();
    expect(sent).toEqual([{ trust: 'ED25519 SHA256:abc' }, { id: 'h1', login: { key: '/srv/k', user: 'root', port: 22 }, password: 'hunter2' }, 'save']);
  });
});

describe('acting on several servers', () => {
  it('runs an action on the selected servers that can take it, and says how each went', async () => {
    const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
    const base = { added: 0, kind: 'agent', status: 'online', readings: null, history: [], problems: [] };
    const login = { port: 22, user: 'root', key: '/srv/k' };
    let hosts = [
      { ...base, id: 'a', name: 'nas', address: 'https://192.168.0.20:9101', ssh: login },
      { ...base, id: 'b', name: 'pi', address: 'https://192.168.0.31:9101' },
      { ...base, id: 'c', name: 'box', address: 'https://192.168.0.44:9101', ssh: login },
    ];
    const calls: string[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      if (method !== 'GET') calls.push(`${method} ${url}`);
      if (url === '/api/hosts') return json({ hosts, port: 9101 });
      if (url === '/api/hosts/a/ssh/test') return json({ output: 'root@nas\n up 3 days' });
      if (url === '/api/hosts/c/ssh/test') return json({ error: 'the server did not answer SSH in time' }, 422);
      const gone = url.match(/^\/api\/hosts\/([a-z])$/);
      if (gone && method === 'DELETE') {
        hosts = hosts.filter((h) => h.id !== gone[1]);
        return new Response(null, { status: 204 });
      }
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
    // While choosing, the cards are boxes to tick, not links.
    expect(screen.queryByRole('link', { name: /nas/ })).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Select all' }));
    expect(screen.getAllByRole('checkbox', { checked: true })).toHaveLength(3);
    const bar = screen.getByRole('toolbar', { name: 'Selected servers' });
    expect(within(bar).getByText('3')).toBeInTheDocument();

    // pi has no SSH login, so a test leaves it out; each server's answer is listed.
    fireEvent.click(within(bar).getByRole('button', { name: 'Test SSH (2)' }));
    const results = await screen.findByRole('region', { name: 'Test SSH results' });
    await waitFor(() => expect(within(results).getByText('1 did not work')).toBeInTheDocument());
    expect(within(results).getByText('root@nas')).toBeInTheDocument();
    expect(within(results).getByText('the server did not answer SSH in time')).toBeInTheDocument();
    expect(within(results).getByText('no SSH login')).toBeInTheDocument();
    expect(calls.filter((c) => c.includes('/ssh/'))).toEqual(expect.arrayContaining(['POST /api/hosts/a/ssh/test', 'POST /api/hosts/c/ssh/test']));
    expect(calls).not.toContain('POST /api/hosts/b/ssh/test');

    // Rebooting asks first.
    fireEvent.click(within(bar).getByRole('button', { name: 'Reboot (2)…' }));
    expect(within(bar).getByText('Reboot 2 servers?')).toBeInTheDocument();
    fireEvent.click(within(bar).getByRole('button', { name: 'Keep running' }));
    expect(calls.some((c) => c.endsWith('/reboot'))).toBe(false);

    // Removing them all asks too, then the selection is empty.
    fireEvent.click(within(bar).getByRole('button', { name: 'Remove…' }));
    fireEvent.click(within(bar).getByRole('button', { name: 'Remove' }));
    await waitFor(() => expect(calls.filter((c) => c.startsWith('DELETE'))).toHaveLength(3));
    await waitFor(() => expect(screen.queryByRole('toolbar', { name: 'Selected servers' })).not.toBeInTheDocument());
  });
});

describe('attack watch', () => {
  it('names each method, how strong it is and how it works, and who is most involved', () => {
    render(
      <AttackWatch
        now={2000}
        view={{
          attacking: true,
          current: {
            id: 'a',
            start: 1905,
            methods: [{ method: 'syn_flood', value: 6100, threshold: 100, normal: 0.2 }],
            peak_rx_bits: 8e8,
            peak_rx_packets: 4e5,
            sources: [
              { address: '185.220.101.4', established: 0, syn_recv: 720, requests: 0 },
              { address: '172.68.10.21', established: 38, syn_recv: 0, requests: 0, network: 'Cloudflare' },
            ],
          },
          findings: [],
          busy: {},
          rx_bits: 0,
          rx_packets: 0,
          learning: false,
          history: [],
        }}
      />,
    );
    expect(screen.getByText('Under attack')).toBeInTheDocument();
    expect(screen.getByText('6.1k per second')).toBeInTheDocument();
    expect(screen.getByText('normal here: under 1')).toBeInTheDocument();
    expect(screen.getByText(/never finishes them/)).toBeInTheDocument();
    expect(screen.getByText(/for 1m 35s · peak/)).toBeInTheDocument();
    expect(screen.getByText(/Cloudflare: many visitors, never blocked/)).toBeInTheDocument();
  });

  it('says calm, and why heavy traffic that is explained is not an attack', () => {
    render(
      <AttackWatch
        now={2000}
        view={{
          attacking: false,
          findings: [{ method: 'volumetric', value: 0.95, threshold: 0.9, normal: 0, expected: 'apt-get is downloading' }],
          busy: { installing: 'apt-get' },
          rx_bits: 0,
          rx_packets: 0,
          learning: true,
          history: [],
        }}
      />,
    );
    expect(screen.getByText(/Calm · still learning/)).toBeInTheDocument();
    expect(screen.getByText('Volumetric-sized traffic (95% of the link), but apt-get is downloading: not an attack.')).toBeInTheDocument();
  });

  it('shows the defense: what it does, who it blocked, and what it cannot do, with the switch and unblock', async () => {
    const toggled: boolean[] = [];
    const unblocked: string[] = [];
    const view = {
      attacking: true,
      current: {
        id: 'a',
        start: 1905,
        methods: [{ method: 'syn_flood', value: 6100, threshold: 100, normal: 0.2 }],
        peak_rx_bits: 8e8,
        peak_rx_packets: 4e5,
        sources: [
          { address: '185.220.101.4', established: 0, syn_recv: 720, requests: 0 },
          { address: '198.51.100.200', established: 3, syn_recv: 0, requests: 0, ssh: true },
        ],
      },
      findings: [],
      busy: {},
      rx_bits: 0,
      rx_packets: 0,
      learning: false,
      history: [],
      defense: {
        on: true,
        active: true,
        web: true,
        measures: [{ id: 'syn_limit', name: 'SYN limit per address', how: 'New connections from one address beyond 50 a second are dropped.' }],
        blocked: [{ address: '185.220.101.4', until: 2000 + 14 * 60, why: '720 half-open connections', where: 'firewall' as const }],
        cannot: ['The line itself is full.'],
      },
    };
    const { rerender } = render(
      <AttackWatch
        now={2000}
        view={view}
        onToggle={async (on) => {
          toggled.push(on);
        }}
        onUnblock={async (a) => {
          unblocked.push(a);
        }}
      />,
    );
    expect(screen.getByText('Defending')).toBeInTheDocument();
    expect(screen.getByText('SYN limit per address')).toBeInTheDocument();
    expect(screen.getByText('720 half-open connections')).toBeInTheDocument();
    expect(screen.getByText('14m more')).toBeInTheDocument();
    expect(screen.getByText('The line itself is full.')).toBeInTheDocument();
    // The sources say who is blocked, and that whoever is on SSH never is.
    expect(screen.getByText(/logged in over SSH, never blocked/)).toBeInTheDocument();
    expect(screen.getByText(/^\s*blocked$/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Unblock' }));
    await waitFor(() => expect(unblocked).toEqual(['185.220.101.4']));
    fireEvent.click(screen.getByRole('button', { name: 'Turn off' }));
    await waitFor(() => expect(toggled).toEqual([false]));

    // An agent's view is read-only: no switch, no unblock.
    rerender(<AttackWatch now={2000} view={{ ...view, defense: { ...view.defense, on: false, active: false, measures: [], blocked: [] } }} />);
    expect(screen.getByText('Off: attacks are only watched')).toBeInTheDocument();
    expect(screen.getByText(/AGENT_DEFEND=off/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Turn/ })).not.toBeInTheDocument();
  });
});

describe('firewall', () => {
  // fakeFirewall is a machine's firewall that behaves like the real one: blocks before allows, the
  // dashboard's own address refused, the default waiting to be kept.
  function fakeFirewall(over: Partial<FirewallStatus> = {}) {
    let st: FirewallStatus = {
      full: true,
      tool: 'ufw',
      policy: 'DROP',
      default: '',
      open: [
        { port: 22, what: 'SSH' },
        { port: 8080, what: 'this dashboard' },
      ],
      rules: [
        { id: 'r1', action: 'block', source: '45.142.120.0/24', note: 'kept scanning SSH', added: 1 },
        { id: 'r0', action: 'limit', port: '22', proto: 'tcp', rate: '6/min', added: 1 },
        { id: 'r9', action: 'block', direction: 'out', port: '25', proto: 'tcp', added: 1 },
      ],
      listening: [
        { port: 22, proto: 'tcp', program: 'sshd', allowed: true },
        { port: 8080, proto: 'tcp', program: 'nightjar', local: true },
        { port: 51413, proto: 'udp', program: 'qbittorrent-nox' },
      ],
      hits: { r1: { packets: 1204, bytes: 72240 } },
      since: Date.now() / 1000 - 3600,
      drops: [
        { at: Date.now() / 1000 - 60, rule: 'r1', source: '45.142.120.11', proto: 'tcp', port: 22 },
        { at: Date.now() / 1000 - 120, rule: 'r1', source: '45.142.120.11', proto: 'tcp', port: 22 },
        { at: Date.now() / 1000 - 180, rule: 'default', source: '198.51.100.9', proto: 'udp', port: 161 },
        { at: Date.now() / 1000 - 240, rule: 'default', source: '198.51.100.9', proto: 'udp', port: 161 },
      ],
      native: 'Status: active\nTo Action From',
      ...over,
    };
    const calls: string[] = [];
    let n = 10;
    const target: FirewallTarget = {
      key: 'nas',
      name: 'nas',
      load: async () => st,
      add: async (rule) => {
        calls.push(`add ${rule.action} ${rule.source ?? ''} ${rule.port ?? ''} ${rule.direction ?? 'in'}`);
        if (rule.port === '9101') throw new Error('that would cut off the dashboard (45.33.1.2, port 9101)');
        const r = { ...rule, id: `r${n++}`, added: 2 } as FirewallRule;
        const at = r.action === 'allow' ? -1 : st.rules.findIndex((o) => o.action === 'allow');
        const rules = [...st.rules];
        rules.splice(at < 0 ? rules.length : at, 0, r);
        st = { ...st, rules };
        return st;
      },
      update: async (id, rule) => {
        calls.push(`update ${id} paused=${!!rule.paused}`);
        st = { ...st, rules: st.rules.map((r) => (r.id === id ? ({ ...r, ...rule, id } as FirewallRule) : r)) };
        return st;
      },
      remove: async (id) => {
        calls.push(`remove ${id}`);
        st = { ...st, rules: st.rules.filter((r) => r.id !== id) };
        return st;
      },
      order: async (ids) => {
        calls.push(`order ${ids.join(',')}`);
        st = { ...st, rules: ids.map((i) => st.rules.find((r) => r.id === i)!) };
        return st;
      },
      setDefault: async (def) => {
        calls.push(`default ${def}`);
        st = { ...st, default: def, pending: { default: def, until: Date.now() / 1000 + 60 } };
        return st;
      },
      keep: async (keep) => {
        calls.push(`keep ${keep}`);
        st = { ...st, default: keep ? st.default : '', pending: undefined };
        return st;
      },
    };
    return { target, calls, get: () => st };
  }

  it('says whether an address is in a network', () => {
    expect(inNetwork('45.142.120.11', '45.142.120.0/24')).toBe(true);
    expect(inNetwork('45.142.121.11', '45.142.120.0/24')).toBe(false);
    expect(inNetwork('203.0.113.7', '203.0.113.7')).toBe(true);
    expect(inNetwork('10.1.2.3', '0.0.0.0/0')).toBe(true);
    expect(inNetwork('2001:db8::1', '2001:db8::/32')).toBe(false);
  });

  it('adds rules in their place, refuses with the reason, pauses, moves, edits and removes them', async () => {
    const fw = fakeFirewall();
    render(
      <ToastProvider>
        <FirewallManager target={fw.target} />
      </ToastProvider>,
    );
    expect(await screen.findByText('45.142.120.0/24')).toBeInTheDocument();
    expect(screen.getByText('kept scanning SSH')).toBeInTheDocument();
    expect(screen.getByText('tcp port 22 · at most 6 a minute each')).toBeInTheDocument();
    expect(screen.getByText(/dropped 1,204/)).toBeInTheDocument();
    expect(screen.getByText('this machine only')).toBeInTheDocument();
    // The outgoing rule waits on its own tab.
    expect(screen.queryByText('to')).not.toBeInTheDocument();

    // A port's "Add rule" fills the form; the dashboard's own port is refused with the reason.
    const submit = () => fireEvent.click(screen.getByLabelText('Port').closest('form')!.querySelector('button[type=submit]')!);
    fireEvent.change(screen.getByLabelText('Port'), { target: { value: '9101' } });
    submit();
    expect(await screen.findByText(/would cut off the dashboard/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Add a rule for 51413/udp' }));
    await waitFor(() => expect(screen.getByLabelText('Port')).toHaveValue('51413'));
    fireEvent.change(screen.getByLabelText('From'), { target: { value: '198.51.100.0/24' } });
    submit();
    await waitFor(() => expect(fw.calls).toContain('add block 198.51.100.0/24 51413 in'));
    expect(await screen.findByText('198.51.100.0/24')).toBeInTheDocument();

    // A preset opens a port for everyone.
    fireEvent.click(screen.getByRole('button', { name: 'Web' }));
    expect(screen.getByLabelText('Port')).toHaveValue('80,443');
    submit();
    await waitFor(() => expect(fw.calls).toContain('add allow  80,443 in'));

    // Pausing keeps the rule, out of place; moving swaps it with its neighbour.
    const first = () => screen.getAllByRole('listitem').find((li) => li.classList.contains('fw-rule'))!;
    fireEvent.click(within(first()).getByRole('button', { name: 'Pause' }));
    await waitFor(() => expect(fw.calls).toContain('update r1 paused=true'));
    expect(await screen.findByText('paused: not in place')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Move down: Block 45.142.120.0/24 on every port' }));
    await waitFor(() => expect(fw.calls.some((c) => c.startsWith('order r0,r1,'))).toBe(true));
    expect(screen.getByRole('button', { name: /Move up: Limit anyone/ })).toBeDisabled();

    // Editing loads the rule into the form and saves it in place.
    fireEvent.click(within(screen.getByText('tcp port 22 · at most 6 a minute each').closest('li')!).getByRole('button', { name: 'Edit' }));
    expect(screen.getByText('Changing: Limit anyone on tcp port 22 to 6 a minute each')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('How many'), { target: { value: '10' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    expect(await screen.findByText('tcp port 22 · at most 10 a minute each')).toBeInTheDocument();
    expect(fw.get().rules.find((r) => r.id === 'r0')?.rate).toBe('10/min');

    fireEvent.click(within(screen.getByText('198.51.100.0/24').closest('li')!).getByRole('button', { name: 'Remove' }));
    await waitFor(() => expect(screen.queryByText('198.51.100.0/24')).not.toBeInTheDocument());

    // Outgoing: its own rules, and no limits.
    fireEvent.click(screen.getByRole('button', { name: /Outgoing/ }));
    expect(screen.getByText('tcp port 25')).toBeInTheDocument();
    expect(screen.getByLabelText('To')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Limit' })).not.toBeInTheDocument();
  });

  it('asks which open ports to keep before dropping what no rule allows, then waits to be kept', async () => {
    const fw = fakeFirewall();
    render(
      <ToastProvider>
        <FirewallManager target={fw.target} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('radio', { name: /Drop it/ }));
    // qBittorrent's port is open and no rule lets anyone in: it is offered, ticked.
    const box = screen.getByRole('checkbox');
    expect(box).toBeChecked();
    expect(within(screen.getByRole('group', { name: 'Ports to keep open' })).getByText('51413/udp')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Keep 1 open, drop the rest' }));
    await waitFor(() => expect(fw.calls).toEqual(['add allow  51413 in', 'default drop']));
    expect(await screen.findByText('Now dropping what no rule allows.')).toBeInTheDocument();
    expect(screen.getByText(/goes back by itself in/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Keep it' }));
    await waitFor(() => expect(screen.queryByText('Now dropping what no rule allows.')).not.toBeInTheDocument());
    expect(fw.calls.at(-1)).toBe('keep true');
    expect(screen.getByRole('radio', { name: /Drop it/ })).toHaveAttribute('aria-checked', 'true');
  });

  it('shows what was dropped, who most, and offers to block those not blocked yet', async () => {
    const fw = fakeFirewall();
    render(
      <ToastProvider>
        <FirewallManager target={fw.target} />
      </ToastProvider>,
    );
    expect(await screen.findByText('Dropped most')).toBeInTheDocument();
    // 45.142.120.11 is in a blocked network; 198.51.100.9 is not, so it can be blocked from here.
    const top = screen.getByText('Dropped most').closest('.fw-top') as HTMLElement;
    expect(within(top).getByText('blocked')).toBeInTheDocument();
    expect(within(top).queryByRole('button', { name: 'Block 45.142.120.11' })).not.toBeInTheDocument();
    fireEvent.click(within(top).getByRole('button', { name: 'Block 198.51.100.9' }));
    await waitFor(() => expect(screen.getByLabelText('From')).toHaveValue('198.51.100.9'));
    expect(screen.getAllByText('no rule allowed it').length).toBe(2);
    expect(screen.getByText(/Show what ufw has/)).toBeInTheDocument();
  });

  it('shows an older agent its rules only, and says it updates itself', async () => {
    const fw = fakeFirewall({ full: false, rules: [{ id: 'r1', action: 'block', source: '203.0.113.7', added: 1 }] });
    render(
      <ToastProvider>
        <FirewallManager target={fw.target} />
      </ToastProvider>,
    );
    expect(await screen.findByText('203.0.113.7')).toBeInTheDocument();
    expect(screen.getByText(/runs an older version/)).toBeInTheDocument();
    expect(screen.queryByRole('radio', { name: /Drop it/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Pause' })).not.toBeInTheDocument();
    expect(screen.queryByText('Dropped lately')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove' })).toBeInTheDocument();
  });
});

describe('new builds', () => {
  it('names a build by its main script', () => {
    expect(buildIn('<script type="module" crossorigin src="/assets/index-B9x_2kQe.js"></script>')).toBe('/assets/index-B9x_2kQe.js');
    expect(buildIn('/src/main.tsx')).toBe('');
    expect(buildIn(null)).toBe('');
  });

  it('notices a newer build, waits while something is typed, and moves over on the next page', async () => {
    const script = document.createElement('script');
    script.type = 'module';
    script.src = '/assets/index-old1.js';
    document.head.appendChild(script);
    const reload = vi.fn();
    const real = window.location;
    Object.defineProperty(window, 'location', { configurable: true, value: { ...real, reload } });
    const fetched = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('<script type="module" src="/assets/index-new2.js"></script>'));
    onTestFinished(() => {
      script.remove();
      fetched.mockRestore();
      Object.defineProperty(window, 'location', { configurable: true, value: real });
    });

    const input = document.createElement('input');
    document.body.appendChild(input);
    onTestFinished(() => input.remove());
    const { result, rerender } = renderHook(({ path }) => useNewBuild(path), { initialProps: { path: '/' } });
    await waitFor(() => expect(result.current).toBe(true));
    expect(fetched).toHaveBeenCalledWith('/', expect.objectContaining({ cache: 'no-store' }));

    // Something typed: coming back to the tab does not throw it away.
    fireEvent.input(input, { target: { value: 'half a rule' } });
    document.dispatchEvent(new Event('visibilitychange'));
    expect(reload).not.toHaveBeenCalled();

    // A page change leaves it behind anyway, so that loads the new build.
    rerender({ path: '/network' });
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('does nothing when the page is served from source', async () => {
    const fetched = vi.spyOn(globalThis, 'fetch').mockResolvedValue(new Response('<script type="module" src="/assets/index-same.js"></script>'));
    onTestFinished(() => fetched.mockRestore());
    const { result } = renderHook(() => useNewBuild('/'));
    // No built script on the page (the dev server): nothing to compare, nothing fetched.
    expect(fetched).not.toHaveBeenCalled();
    expect(result.current).toBe(false);
  });
});
