import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { AttackView, PlexState, Plugin } from '../src/api/types';
import { copyText } from '../src/clipboard';
import { AttackWatch } from '../src/components/Attacks';
import { PlexPage } from '../src/pages/Plex';
import { PluginsPage } from '../src/pages/Plugins';

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('copyText', () => {
  const setClipboard = (c: unknown) => Object.defineProperty(navigator, 'clipboard', { value: c, configurable: true });
  const setExec = (f: unknown) => Object.defineProperty(document, 'execCommand', { value: f, configurable: true, writable: true });

  it('uses the clipboard API where there is one', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    setClipboard({ writeText });
    expect(await copyText('hello')).toBe('copied');
    expect(writeText).toHaveBeenCalledWith('hello');
  });

  it('copies through a hidden textarea on plain http, and leaves nothing behind', async () => {
    setClipboard(undefined);
    let copied = '';
    setExec(() => {
      copied = (document.activeElement as HTMLTextAreaElement).value;
      return true;
    });
    expect(await copyText('journalctl -u nightjar')).toBe('copied');
    expect(copied).toBe('journalctl -u nightjar');
    expect(document.querySelector('textarea')).toBeNull();
  });

  it('selects the text shown when nothing can copy, for Ctrl+C', async () => {
    setClipboard(undefined);
    setExec(() => false);
    const el = document.createElement('code');
    el.textContent = '/home/media/downloads';
    document.body.appendChild(el);
    expect(await copyText('/home/media/downloads', el)).toBe('selected');
    expect(String(window.getSelection())).toBe('/home/media/downloads');
    expect(await copyText('x')).toBe('failed');
    el.remove();
  });
});

describe('AttackWatch', () => {
  const view = (attacking: boolean): AttackView => ({
    attacking,
    current: attacking ? { id: 'a', start: Date.now() / 1000 - 60, end: 0, methods: [], sources: [], peak_rx_bits: 1e9, peak_rx_packets: 0 } : undefined,
    findings: [],
    busy: {},
    rx_bits: 0,
    rx_packets: 0,
    learning: false,
    history: [],
    defense: { on: true, active: attacking, measures: [], blocked: [], web: false },
  });

  it('asks before turning the defense off during an attack', async () => {
    const onToggle = vi.fn().mockResolvedValue(undefined);
    render(<AttackWatch view={view(true)} now={Date.now() / 1000} onToggle={onToggle} />);
    fireEvent.click(screen.getByRole('button', { name: 'Turn off…' }));
    expect(onToggle).not.toHaveBeenCalled();
    expect(screen.getByText(/Turn off the defense during an attack\?/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Keep on' }));
    expect(screen.queryByText(/during an attack\?/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Turn off…' }));
    await act(async () => fireEvent.click(screen.getByRole('button', { name: 'Turn off' })));
    expect(onToggle).toHaveBeenCalledWith(false);
  });

  it('turns it off at once when calm, in words that fit a remote server too', async () => {
    const onToggle = vi.fn().mockResolvedValue(undefined);
    render(<AttackWatch view={view(false)} now={Date.now() / 1000} onToggle={onToggle} />);
    expect(screen.getByText(/^No flood under way\./)).toBeInTheDocument();
    await act(async () => fireEvent.click(screen.getByRole('button', { name: 'Turn off' })));
    expect(onToggle).toHaveBeenCalledWith(false);
  });
});

describe('PluginsPage', () => {
  const plugin: Plugin = {
    id: 'weather',
    name: 'Weather',
    version: '1.0',
    description: '',
    provides: ['card'],
    run: 'run.sh',
    settings: [{ key: 'city', label: 'City', type: 'text' }],
    dir: '/plugins/weather',
    enabled: false,
    values: { city: 'Oslo' },
    secrets_set: [],
  };

  it('flips the switch without saving half-typed settings', async () => {
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async (_u, init) => (init?.method === 'PUT' ? json({ plugin: { ...plugin, enabled: true } }) : json({ dir: '/plugins', plugins: [plugin] })));
    render(
      <MemoryRouter>
        <PluginsPage />
      </MemoryRouter>,
    );
    fireEvent.change(await screen.findByLabelText('City'), { target: { value: 'Bergen' } });
    await act(async () => fireEvent.click(screen.getByRole('switch', { name: 'Turn Weather on' })));
    const put = fetchMock.mock.calls.find(([, i]) => i?.method === 'PUT')!;
    expect(JSON.parse(put[1]?.body as string)).toEqual({ enabled: true });
    // The edit is still there to save or discard.
    expect(screen.getByLabelText('City')).toHaveValue('Bergen');
    expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument();
  });

  it('lets Look again retry a first read that failed, and clears the error', async () => {
    let fail = true;
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => (fail ? json({ error: 'plugins folder unreadable' }, 500) : json({ dir: '/plugins', plugins: [plugin] })));
    render(
      <MemoryRouter>
        <PluginsPage />
      </MemoryRouter>,
    );
    const again = screen.getByRole('button', { name: 'Look again' });
    await waitFor(() => expect(again).toBeEnabled());
    expect(screen.getByText(/unreadable/)).toBeInTheDocument();
    fail = false;
    await act(async () => fireEvent.click(again));
    await screen.findByText('Weather');
    expect(screen.queryByText(/unreadable/)).not.toBeInTheDocument();
  });
});

describe('PlexPage', () => {
  const state = (over: Partial<PlexState> = {}): PlexState => ({
    url: 'http://plex',
    status: 'up',
    detail: '',
    since: 0,
    checked_at: 1000,
    latency_ms: 3,
    server: null,
    streams: [],
    direct: 0,
    transcode: 0,
    bandwidth_kbps: 0,
    wan_kbps: 0,
    libraries: [],
    libraries_at: 500,
    samples: [],
    recent: [],
    ...over,
  });

  it('polls the light summary between full reads, adding each check to the charts itself', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let checked = 1000;
    let streams: PlexState['streams'] = [];
    const urls: string[] = [];
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (u) => {
      const url = String(u);
      if (url.startsWith('/api/plex/stats')) return json({ enabled: true, stats: null });
      urls.push(url);
      const summary = url.includes('view=summary');
      return json({ enabled: true, plex: state({ checked_at: checked, streams, direct: streams.length, samples: summary ? [] : [{ ts: 1000, direct: 0, transcode: 0, bandwidth_kbps: 0 }] }) });
    });
    try {
      render(
        <MemoryRouter>
          <PlexPage />
        </MemoryRouter>,
      );
      await waitFor(() => expect(urls).toEqual(['/api/plex']));
      checked = 1005;
      await act(() => vi.advanceTimersByTimeAsync(5000));
      expect(urls.at(-1)).toBe('/api/plex?view=summary');
      // A stream starting (or ending) brings the whole state, for the finished plays.
      streams = [{ id: 's1' } as PlexState['streams'][number]];
      await act(() => vi.advanceTimersByTimeAsync(5000));
      expect(urls.at(-1)).toBe('/api/plex');
      expect(urls.filter((x) => x === '/api/plex')).toHaveLength(2);
    } finally {
      vi.useRealTimers();
    }
  });
});
