import { act, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ConfigContext } from '../src/api/ConfigContext';
import type { AppConfig, SettingField } from '../src/api/types';
import { LatestIncident } from '../src/components/LatestIncident';
import { formatTime, inYourTime, ServerTime } from '../src/components/TimePicker';
import { LogsPage } from '../src/pages/Logs';
import { SettingsPage } from '../src/pages/Settings';

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

describe('monitor log follow', () => {
  it('keeps the lines and does not reload the tail when Follow is turned off and on', async () => {
    const entry = (seq: number, message: string) => ({ seq, time: Date.now() - (10 - seq) * 1000, level: 'info', component: 'web', message });
    const fetchMock = vi
      .spyOn(globalThis, 'fetch')
      .mockImplementation(async () => json({ entries: [entry(1, 'Web dashboard listening'), entry(2, 'Checked 4 services')], latest: 2, components: ['web'], level: 'INFO', kept: 3000 }));
    render(
      <MemoryRouter>
        <LogsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText('Checked 4 services')).toBeInTheDocument();
    // A picked line keeps its actions through the toggle.
    fireEvent.click(screen.getByText('Checked 4 services'));
    const tails = () => fetchMock.mock.calls.filter(([u]) => !String(u).includes('after=')).length;
    const before = tails();
    fireEvent.click(screen.getByLabelText('Follow'));
    fireEvent.click(screen.getByLabelText('Follow'));
    await act(async () => {});
    expect(screen.getByText('Checked 4 services')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Copy line' })).toBeInTheDocument();
    // Turning it back on asks only for newer lines.
    expect(tails()).toBe(before);
    await waitFor(() => expect(fetchMock.mock.calls.some(([u]) => String(u).includes('after=2'))).toBe(true));
  });
});

describe('settings reset and errors', () => {
  const field = (over: Partial<SettingField>): SettingField => ({ key: 'K', group: 'G', label: 'L', kind: 'text', default: '', value: '', set: false, source: 'default', ...over });

  it('shows the default a reset goes back to, and clears a field’s error once it is edited', async () => {
    const settings = {
      groups: [
        {
          name: 'Resources',
          fields: [
            field({ key: 'AUTO_REPAIR', label: 'Repairs', kind: 'bool', value: 'false', default: 'true', source: 'dashboard' }),
            field({ key: 'CPU_THRESHOLD', label: 'CPU', kind: 'number', value: '80', default: '80' }),
          ],
        },
      ],
    };
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      if (String(input).startsWith('/api/activity')) return json({ activity: [] });
      if (init?.method === 'PUT') return json({ error: 'Some settings are invalid', fields: { CPU_THRESHOLD: 'must be at most 100' } }, 400);
      return json(settings);
    });
    render(
      <MemoryRouter>
        <SettingsPage />
      </MemoryRouter>,
    );
    const repairs = await screen.findByLabelText('Repairs');
    expect(repairs).not.toBeChecked();
    fireEvent.click(screen.getByRole('button', { name: 'Reset' }));
    expect(screen.getByLabelText('Repairs')).toBeChecked();

    fireEvent.change(screen.getByLabelText('CPU'), { target: { value: '150' } });
    fireEvent.click(screen.getByText('Save and restart'));
    expect(await screen.findByText('must be at most 100')).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('CPU'), { target: { value: '90' } });
    expect(screen.queryByText('must be at most 100')).not.toBeInTheDocument();
  });
});

describe('server time', () => {
  const here = -new Date().getTimezoneOffset() * 60;
  const withZone = (offset: number, name = 'Europe/London') => {
    const config = { auth_required: false, authorized: true, status_page_enabled: false, status_page_title: '', time_zone: { name, offset } } as AppConfig;
    return render(
      <ConfigContext.Provider value={config}>
        <ServerTime from="22:00" to="06:00" />
      </ConfigContext.Provider>,
    );
  };

  it('names the server zone, and adds the browser time only where it differs', () => {
    const { container, unmount } = withZone(here);
    expect(container).toHaveTextContent(/^Server time \(Europe\/London\)$/);
    unmount();
    // The server an hour ahead: its 22:00 is 21:00 here.
    withZone(here + 3600);
    expect(screen.getByText(/Server time \(Europe\/London\)/)).toHaveTextContent(`${formatTime('21:00')}–${formatTime('05:00')} your time`);
  });

  it('moves weekly days across midnight', () => {
    expect(inYourTime('00:30', '01:30', -60, [0, 3])).toBe(`Tue, Sat ${formatTime('23:30')}–${formatTime('00:30')}`);
    expect(inYourTime('23:30', '00:30', 60, [6])).toBe(`Sun ${formatTime('00:30')}–${formatTime('01:30')}`);
    // Every day stays every day, so no days are named.
    expect(inYourTime('23:30', '00:30', 60, [0, 1, 2, 3, 4, 5, 6])).toBe(`${formatTime('00:30')}–${formatTime('01:30')}`);
  });
});

describe('latest incident', () => {
  it('counts an ongoing incident in whole minutes', async () => {
    const started = Date.now() / 1000 - (4 * 60 + 40);
    vi.spyOn(globalThis, 'fetch').mockResolvedValue(json({ incidents: [{ id: 1, title: 'website down', started, ended: null, cause: null, also: [], events: [], checks: [] }], days: 3 }));
    render(
      <MemoryRouter>
        <LatestIncident />
      </MemoryRouter>,
    );
    expect(await screen.findByText(/4m so far/)).toBeInTheDocument();
  });
});
