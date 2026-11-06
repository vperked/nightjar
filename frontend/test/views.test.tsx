import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { useState } from 'react';
import { MemoryRouter, Route, Routes, useLocation, useNavigate, type NavigateFunction } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../src/api/client';
import type { ActivityEntry, AnalyticsResponse, FirewallRule, FirewallStatus, Service } from '../src/api/types';
import { ActivityPanel } from '../src/components/ActivityPanel';
import { FirewallManager, type FirewallTarget } from '../src/components/Firewall';
import { Select, type Option } from '../src/components/Select';
import { TimePicker } from '../src/components/TimePicker';
import { ToastProvider } from '../src/components/Toasts';
import { AnalyticsPage } from '../src/pages/Analytics';
import { DocsPage } from '../src/pages/Docs';
import { ServiceRow } from '../src/pages/Services';

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('Select', () => {
  const opts = (...names: string[]): Option[] => names.map((n) => ({ value: n, label: n }));

  it('shows a value that is not among the options as it is, with no option marked chosen', () => {
    render(<Select label="Interface" value="veth9" options={opts('eth0', 'eth1')} onChange={() => {}} />);
    const button = screen.getByRole('combobox', { name: 'Interface' });
    expect(button).toHaveTextContent('veth9');
    fireEvent.click(button);
    expect(screen.getAllByRole('option').map((o) => o.getAttribute('aria-selected'))).toEqual(['false', 'false']);
  });

  it('keeps Enter safe when the options shrink while open, and does not open with none', () => {
    const onChange = vi.fn();
    let shrink: (o: Option[]) => void = () => {};
    function Host() {
      const [options, setOptions] = useState(opts('eth0', 'eth1', 'veth1', 'veth2'));
      shrink = setOptions;
      return <Select label="Interface" value="eth0" options={options} onChange={onChange} />;
    }
    render(<Host />);
    const button = screen.getByRole('combobox', { name: 'Interface' });
    fireEvent.keyDown(button, { key: 'Enter' });
    fireEvent.keyDown(button, { key: 'End' });
    act(() => shrink(opts('eth0', 'eth1', 'veth1')));
    fireEvent.keyDown(button, { key: 'Enter' });
    expect(onChange).toHaveBeenLastCalledWith('veth1');

    act(() => shrink([]));
    fireEvent.keyDown(button, { key: 'Enter' });
    expect(button).toHaveAttribute('aria-expanded', 'false');
    fireEvent.keyDown(button, { key: 'Enter' });
    fireEvent.keyDown(button, { key: 'ArrowDown' });
    expect(onChange).toHaveBeenCalledTimes(1);
  });
});

describe('TimePicker', () => {
  it('closes with Escape while focus is still on the field', () => {
    render(<TimePicker label="Quiet hours start" value="22:00" onChange={() => {}} />);
    const field = screen.getByRole('button', { name: 'Quiet hours start' });
    fireEvent.click(field);
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    fireEvent.keyDown(field, { key: 'Escape' });
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });

  it('closes when focus moves on past it', () => {
    render(
      <>
        <TimePicker label="Quiet hours start" value="22:00" onChange={() => {}} />
        <button type="button">Next</button>
      </>,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Quiet hours start' }));
    fireEvent.blur(screen.getByRole('button', { name: 'Quiet hours start' }), { relatedTarget: screen.getByRole('button', { name: 'Next' }) });
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });
});

describe('ActivityPanel', () => {
  const entries = (from: number, to: number): ActivityEntry[] =>
    Array.from({ length: from - to + 1 }, (_, i) => ({ id: from - i, ts: 1_700_000_000, who: 'admin', kind: 'settings', action: `change ${from - i}` }));

  it('keeps the entries that slide off the newest page after older ones were loaded', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    let newest = entries(200, 101);
    vi.spyOn(api, 'activity').mockImplementation(async (before = 0) => ({ activity: before ? entries(before - 1, 1) : newest }));
    render(<ActivityPanel />);
    fireEvent.click(await screen.findByRole('button', { name: 'Older' }));
    await waitFor(() => expect(screen.getByText('change 1')).toBeInTheDocument());

    newest = entries(205, 106);
    await act(() => vi.advanceTimersByTimeAsync(30000));
    await waitFor(() => expect(screen.getByText('change 205')).toBeInTheDocument());
    const shown = screen.getAllByRole('listitem').map((li) => li.querySelector('.activity-what')!.textContent);
    expect(shown).toEqual(entries(205, 1).map((a) => a.action));
  });
});

describe('Firewall manager', () => {
  function target(over: Partial<FirewallStatus> = {}) {
    const st = {
      full: true,
      tool: 'ufw',
      policy: 'DROP',
      default: '',
      open: [{ port: 22, what: 'SSH' }],
      rules: [],
      listening: [{ port: 22, proto: 'tcp', program: 'sshd', allowed: true }],
      ...over,
    } as FirewallStatus;
    const added: Partial<FirewallRule>[] = [];
    const t: FirewallTarget = {
      key: 'nas',
      name: 'nas',
      load: async () => st,
      add: async (rule) => {
        added.push(rule);
        return st;
      },
      update: async () => st,
      remove: async () => st,
      order: async () => st,
      setDefault: async () => st,
      keep: async () => st,
    };
    return { t, added };
  }

  it('reads an answer with no open ports (null from an older agent) instead of failing', async () => {
    const { t } = target({ listening: null as unknown as FirewallStatus['listening'] });
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: '+ Add a rule' }));
    expect(screen.getByLabelText('Port')).toBeInTheDocument();
  });

  it('leaves the protocol out once the port is cleared', async () => {
    const { t, added } = target();
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Add a rule for 22/tcp' }));
    await waitFor(() => expect(screen.getByLabelText('Port')).toHaveValue('22'));
    expect(screen.getByLabelText('Protocol')).toHaveValue('tcp');
    fireEvent.click(screen.getByRole('radio', { name: /^Block/ }));
    fireEvent.change(screen.getByLabelText('Port'), { target: { value: '' } });
    expect(screen.getByLabelText('Protocol')).toHaveValue('');
    fireEvent.click(screen.getByLabelText('Every port'));
    fireEvent.change(screen.getByLabelText('From'), { target: { value: '203.0.113.7' } });
    fireEvent.click(screen.getByLabelText('Port').closest('form')!.querySelector('button[type=submit]')!);
    await waitFor(() => expect(added).toHaveLength(1));
    expect(added[0]).toMatchObject({ source: '203.0.113.7', port: '' });
    expect(added[0].proto).toBeUndefined();
  });
});

describe('Firewall rule form', () => {
  it('starts with nothing chosen, says what is missing, and puts the rule in words', async () => {
    const st = { full: true, tool: 'ufw', policy: 'DROP', default: '', open: [], rules: [], listening: [] } as unknown as FirewallStatus;
    const added: Partial<FirewallRule>[] = [];
    const t: FirewallTarget = {
      key: 'x',
      name: 'x',
      load: async () => st,
      add: async (rule) => {
        added.push(rule);
        return st;
      },
      update: async () => st,
      remove: async () => st,
      order: async () => st,
      setDefault: async () => st,
      keep: async () => st,
    };
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: '+ Add a rule' }));
    const add = screen.getByRole('button', { name: 'Add rule' });
    expect(add).toBeDisabled();
    expect(screen.getByText('Pick what the rule does.')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('radio', { name: /^Block/ }));
    // Anyone on every port would block everything: it says so instead of greying the button out silently.
    expect(screen.getByText(/would block everything, you included/)).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText('Only', { selector: 'input[name="fw-who"]' }));
    expect(screen.getByText(/Type who/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('From'), { target: { value: '203.0.113.7' } });
    expect(screen.getByText(/block 203\.0\.113\.7 on every port/)).toBeInTheDocument();
    // A limit needs a port, so choosing it asks for one.
    fireEvent.click(screen.getByRole('radio', { name: /^Limit/ }));
    expect(screen.getByLabelText('Every port')).toBeDisabled();
    expect(screen.getByText(/Type the port/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Port'), { target: { value: '22' } });
    expect(add).toBeEnabled();
    fireEvent.click(add);
    await waitFor(() => expect(added).toHaveLength(1));
    expect(added[0]).toMatchObject({ action: 'limit', source: '203.0.113.7', port: '22', rate: '6/min' });
    // It closes once added.
    await waitFor(() => expect(screen.queryByRole('form', { name: 'Add a rule' })).not.toBeInTheDocument());
  });
});

describe('ServiceRow', () => {
  it('goes into the history once when the name itself is clicked', async () => {
    const svc = { name: 'web', type: 'http', target: 'https://example.com', status: 'up', recent: [], uptime_24h: 100, last_check: 0 } as unknown as Service;
    let go: NavigateFunction = () => {};
    function Where() {
      go = useNavigate();
      return <p data-testid="where">{useLocation().pathname}</p>;
    }
    render(
      <MemoryRouter initialEntries={['/services']}>
        <Where />
        <Routes>
          <Route
            path="/services"
            element={
              <table>
                <tbody>
                  <ServiceRow s={svc} now={0} />
                </tbody>
              </table>
            }
          />
          <Route path="/services/:name" element={null} />
        </Routes>
      </MemoryRouter>,
    );
    fireEvent.click(screen.getByRole('link', { name: 'web' }));
    expect(screen.getByTestId('where')).toHaveTextContent('/services/web');
    act(() => void go(-1));
    expect(screen.getByTestId('where')).toHaveTextContent(/^\/services$/);
  });
});

describe('Docs', () => {
  it('opens on a link whose #fragment is not valid percent-encoding', async () => {
    render(
      <MemoryRouter initialEntries={['/docs/alerts#%E0%A4%A']}>
        <Routes>
          <Route path="/docs/:slug" element={<DocsPage />} />
        </Routes>
      </MemoryRouter>,
    );
    expect(await screen.findAllByRole('heading')).not.toHaveLength(0);
  });
});

describe('Analytics', () => {
  it('says the database is unavailable when a server has no points (null)', async () => {
    const none = null;
    const blank = {
      coverage: 0,
      uptime: none,
      downtime_s: 0,
      incidents: 0,
      cpu_avg: none,
      cpu_peak: none,
      mem_avg: none,
      mem_peak: none,
      temp_avg: none,
      temp_peak: none,
      disk_now: none,
      disk_change: none,
      disk_full_days: none,
      rx_bytes: 0,
      tx_bytes: 0,
      latency_avg: none,
      latency_peak: none,
    };
    const reply = {
      range: { name: '24h', label: '24 hours' },
      ranges: [],
      step: 300,
      kept: false,
      hosts: [
        { id: 'local', name: 'nas', kind: 'local', status: 'up', points: null, summary: blank, incidents: [] },
        { id: 'h1', name: 'pi', kind: 'agent', status: 'up', points: null, summary: blank, incidents: [] },
      ],
    } as unknown as AnalyticsResponse;
    vi.spyOn(api, 'analytics').mockResolvedValue(reply);
    render(
      <MemoryRouter>
        <AnalyticsPage />
      </MemoryRouter>,
    );
    expect(await screen.findByText('The database is unavailable, so nothing is being kept.')).toBeInTheDocument();
    expect(within(screen.getByRole('table')).getAllByText(/no readings yet/)).toHaveLength(2);
  });
});
