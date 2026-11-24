import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import type { TailscaleSettings, TailscaleState } from '../src/api/types';
import { ToastProvider } from '../src/components/Toasts';
import { TailscaleManager, type TailscaleTarget } from '../src/pages/Tailscale';

const SETTINGS: TailscaleSettings = {
  accept_routes: false,
  accept_dns: true,
  ssh: false,
  shields_up: false,
  advertise_exit_node: false,
  advertise_routes: [],
  exit_node: '',
  exit_node_allow_lan: false,
  hostname: '',
};

const node = (id: string, name: string, extra = {}) => ({ id, name, ips: ['100.64.0.' + id.length], online: true, active: false, rx: 0, tx: 0, exit_node: false, exit_node_option: false, ...extra });

let made = 0;

// A machine on a tailnet whose target changes it as the API would, and notes each call.
function target(start: Partial<TailscaleState> = {}) {
  let st: TailscaleState = {
    installed: true,
    running: true,
    backend: 'Running',
    logged_in: true,
    version: '1.104.7',
    tailnet: 'owner@example.com',
    self: node('self', 'home', { dns_name: 'home.tail1.ts.net' }),
    peers: [node('nLaptop', 'laptop', { direct: '192.168.1.2:41641' }), node('nVps', 'vps', { exit_node_option: true, relay: 'fra', online: false, last_seen: Date.now() / 1000 - 7200 })],
    health: [],
    settings: SETTINGS,
    ...start,
  };
  const calls: string[] = [];
  const t: TailscaleTarget = {
    // A key of its own, so no test starts from another's state remembered for the visit.
    key: `test-${++made}`,
    name: 'this machine',
    load: async () => st,
    login: async (k) => {
      calls.push(`login ${k ?? ''}`);
      st = { ...st, auth_url: k ? undefined : 'https://login.tailscale.com/a/x', logged_in: !!k, backend: k ? 'Running' : 'NeedsLogin' };
      return st;
    },
    action: async (a) => {
      calls.push(a);
      st = { ...st, backend: a === 'up' ? 'Running' : a === 'down' ? 'Stopped' : 'NeedsLogin', logged_in: a !== 'logout' };
      return st;
    },
    set: async (s) => {
      calls.push(`set ${JSON.stringify(s)}`);
      st = { ...st, settings: { ...st.settings, ...s }, pending: s.exit_node || s.shields_up ? { until: Date.now() / 1000 + 120, what: 'the exit node' } : st.pending };
      return st;
    },
    keep: async (k) => {
      calls.push(`keep ${k}`);
      st = { ...st, pending: undefined };
      return st;
    },
  };
  return { t, calls };
}

const show = (t: TailscaleTarget) =>
  render(
    <ToastProvider>
      <TailscaleManager target={t} />
    </ToastProvider>,
  );

describe('Tailscale', () => {
  it('shows the tailnet and its machines', async () => {
    show(target().t);
    expect(await screen.findByText('Connected')).toBeInTheDocument();
    expect(screen.getByText('owner@example.com')).toBeInTheDocument();
    expect(screen.getByText('laptop')).toBeInTheDocument();
    expect(screen.getByText('seen 2h ago')).toBeInTheDocument();
    expect(screen.getByText('1 direct · 0 relayed')).toBeInTheDocument();
  });

  it('filters the machines', async () => {
    show(target().t);
    fireEvent.click(await screen.findByRole('button', { name: 'Online' }));
    expect(screen.getByText('laptop')).toBeInTheDocument();
    expect(within(screen.getByRole('region', { name: 'Machines' })).queryByText('vps')).not.toBeInTheDocument();
  });

  it('flips a switch at once, and back if the change is refused', async () => {
    const { t, calls } = target();
    let refuse = false;
    const set = t.set;
    t.set = async (s) => {
      if (refuse) throw new Error('tailscale set: nope');
      return set(s);
    };
    show(t);
    const ssh = await screen.findByRole('switch', { name: 'Tailscale SSH' });
    fireEvent.click(ssh);
    expect(ssh).toBeChecked();
    await waitFor(() => expect(calls).toEqual(['set {"ssh":true}']));
    refuse = true;
    const routes = screen.getByRole('switch', { name: 'Use subnet routes' });
    fireEvent.click(routes);
    expect(routes).toBeChecked();
    await waitFor(() => expect(routes).not.toBeChecked());
    expect(await screen.findByText('tailscale set: nope')).toBeInTheDocument();
    expect(ssh).toBeChecked();
  });

  it('picks an exit node, and asks to keep it', async () => {
    const { t, calls } = target();
    show(t);
    fireEvent.click(await screen.findByRole('radio', { name: /vps/ }));
    await waitFor(() => expect(calls).toEqual(['set {"exit_node":"nVps","exit_node_allow_lan":true}']));
    expect(screen.getByRole('radio', { name: /vps/ })).toHaveAttribute('aria-checked', 'true');
    expect(await screen.findByText(/Check that you can still reach this machine/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Keep' }));
    await waitFor(() => expect(calls.at(-1)).toBe('keep true'));
  });

  it('asks before disconnecting', async () => {
    const { t, calls } = target();
    show(t);
    fireEvent.click(await screen.findByRole('button', { name: 'Disconnect' }));
    expect(calls).toEqual([]);
    fireEvent.click(within(screen.getByRole('group', { name: 'Disconnect' })).getByRole('button', { name: 'Disconnect' }));
    await waitFor(() => expect(calls).toEqual(['down']));
    expect(await screen.findByRole('button', { name: 'Connect' })).toBeInTheDocument();
  });

  it('logs in with a link or an auth key', async () => {
    const { t, calls } = target({ backend: 'NeedsLogin', logged_in: false, peers: [] });
    show(t);
    fireEvent.click(await screen.findByRole('button', { name: 'Get login link' }));
    expect(await screen.findByRole('link', { name: 'Open login link' })).toHaveAttribute('href', 'https://login.tailscale.com/a/x');
    fireEvent.change(screen.getByLabelText('Auth key'), { target: { value: 'tskey-auth-abc' } });
    fireEvent.click(screen.getByRole('button', { name: 'Log in' }));
    await waitFor(() => expect(calls).toEqual(['login ', 'login tskey-auth-abc']));
    expect(await screen.findByText('Connected')).toBeInTheDocument();
  });

  it('says how to install Tailscale when it is not there', async () => {
    show(target({ installed: false, running: false }).t);
    expect(await screen.findByText(/install\.sh/)).toBeInTheDocument();
  });
});
