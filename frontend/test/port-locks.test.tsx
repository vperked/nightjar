import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { ConfigContext } from '../src/api/ConfigContext';
import type { AppConfig, FirewallLock, FirewallStatus, SSHAccess } from '../src/api/types';
import { FirewallManager, serviceName, type FirewallTarget } from '../src/components/Firewall';
import { ToastProvider } from '../src/components/Toasts';

// A firewall with postgres on 5432 and a script on 8000, and locks that a target changes as the API
// would.
function target(locks: FirewallLock[] | null | undefined = []) {
  const st = {
    full: true,
    tool: 'iptables',
    policy: 'ACCEPT',
    default: '',
    open: [{ port: 22, what: 'SSH' }],
    rules: [],
    listening: [
      { port: 5432, proto: 'tcp', program: 'postgres', exe: '/usr/lib/postgresql/16/bin/postgres', unit: 'postgresql@16-main.service' },
      { port: 8000, proto: 'tcp', program: 'python3', exe: '/usr/bin/python3.12' },
    ],
    locks,
  } as FirewallStatus;
  const calls: string[] = [];
  const t: FirewallTarget = {
    key: 'nas',
    name: 'nas',
    load: async () => st,
    add: async () => st,
    update: async () => st,
    remove: async () => st,
    order: async () => st,
    setDefault: async () => st,
    keep: async () => st,
    lock: async (port, proto) => {
      calls.push(`lock ${port}/${proto}`);
      const p = st.listening.find((l) => l.port === port)!;
      st.locks = [...(st.locks ?? []), { id: `l${port}`, port, proto: 'tcp', program: p.program, exe: p.exe, unit: p.unit, added: 1, state: 'held' }];
      return { ...st };
    },
    relock: async (id) => {
      calls.push(`relock ${id}`);
      st.locks = st.locks!.map((l) => (l.id === id ? { ...l, ...l.holder, state: 'held', holder: undefined } : l));
      return { ...st };
    },
    unlock: async (id) => {
      calls.push(`unlock ${id}`);
      st.locks = st.locks!.filter((l) => l.id !== id);
      return { ...st };
    },
  };
  return { t, calls };
}

const show = (t: FirewallTarget) =>
  render(
    <ToastProvider>
      <FirewallManager target={t} />
    </ToastProvider>,
  );

describe('Port locks', () => {
  it('names a port by its systemd service, else its program', () => {
    expect(serviceName({ program: 'postgres', unit: 'postgresql@16-main.service' })).toBe('postgresql@16-main');
    expect(serviceName({ program: 'python3', exe: '/usr/bin/python3.12' })).toBe('python3');
    expect(serviceName({ exe: '/opt/app/bin/app' })).toBe('app');
  });

  it('locks an open port to the service on it, and lists it', async () => {
    const { t, calls } = target();
    show(t);
    expect(await screen.findByText('postgresql@16-main')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Lock 5432/tcp to postgresql@16-main' }));
    await waitFor(() => expect(calls).toEqual(['lock 5432/tcp']));
    const locked = (await screen.findByText('Locked ports')).closest('.fw-locks') as HTMLElement;
    expect(within(locked).getByText('held by its service')).toBeInTheDocument();
    // A locked port offers no second lock.
    expect(screen.queryByRole('button', { name: 'Lock 5432/tcp to postgresql@16-main' })).not.toBeInTheDocument();
    fireEvent.click(within(locked).getByRole('button', { name: 'Unlock' }));
    await waitFor(() => expect(calls).toEqual(['lock 5432/tcp', 'unlock l5432']));
    await waitFor(() => expect(screen.queryByText('Locked ports')).not.toBeInTheDocument());
  });

  it('says a taken port is being dropped, and trusts the program on it when asked', async () => {
    const { t, calls } = target([
      {
        id: 'l1',
        port: 5432,
        proto: 'tcp',
        program: 'postgres',
        exe: '/usr/lib/postgresql/16/bin/postgres',
        unit: 'postgresql@16-main.service',
        added: 1,
        state: 'taken',
        holder: { program: 'python3', exe: '/usr/bin/python3.12' },
      },
    ]);
    show(t);
    const locked = (await screen.findByText('Locked ports')).closest('.fw-locks') as HTMLElement;
    expect(within(locked).getByText(/python3 holds it: dropping what comes to it/)).toBeInTheDocument();
    expect(screen.getByText('taken · dropping')).toBeInTheDocument();
    fireEvent.click(within(locked).getByRole('button', { name: 'Trust it' }));
    await waitFor(() => expect(calls).toEqual(['relock l1']));
    expect(await within(locked).findByText('held by its service')).toBeInTheDocument();
  });

  it('says a taken port kept open (SSH’s) is only watched', async () => {
    const { t } = target([{ id: 'l1', port: 22, proto: 'tcp', program: 'sshd', unit: 'ssh.service', added: 1, state: 'taken', watch_only: true, holder: { program: 'dropbear' } }]);
    show(t);
    expect(await screen.findByText(/dropbear holds it: the port is kept open, so it is only watched/)).toBeInTheDocument();
  });

  it('offers no locking on an agent from before locks', async () => {
    const { t } = target(null);
    show(t);
    expect(await screen.findByText('postgresql@16-main')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Lock / })).not.toBeInTheDocument();
  });
});

describe('Firewall clocks and viewers', () => {
  it('counts down on the machine’s clock, not the browser’s', async () => {
    // The machine's clock is two minutes behind the browser's; its change goes back in a minute.
    const machineNow = Math.round(Date.now() / 1000) - 120;
    const st = { full: true, tool: 'iptables', default: 'drop', pending: { default: 'drop', until: machineNow + 60 }, now: machineNow, rules: [], listening: [] } as unknown as FirewallStatus;
    const { t } = target();
    t.load = async () => st;
    show(t);
    const alert = await screen.findByRole('alert');
    const left = Number(
      within(alert)
        .getByText(/^\d+s$/)
        .textContent!.replace('s', ''),
    );
    expect(left).toBeGreaterThan(50);
    expect(left).toBeLessThanOrEqual(60);
  });

  it('does not ask for SSH logins as a viewer, which the server refuses', async () => {
    const { t } = target();
    let asked = 0;
    t.ssh = {
      load: async () => {
        asked++;
        return { ports: [22], password_login: true, keys_only: false, users: [] } as unknown as SSHAccess;
      },
      addKey: async () => ({}) as SSHAccess,
      keysOnly: async () => ({}) as SSHAccess,
      keep: async () => ({}) as SSHAccess,
    };
    render(
      <ConfigContext.Provider value={{ account: { configured: true, username: 'sam', role: 'viewer' }, authorized: true } as unknown as AppConfig}>
        <ToastProvider>
          <FirewallManager target={t} />
        </ToastProvider>
      </ConfigContext.Provider>,
    );
    expect(await screen.findByText('postgresql@16-main')).toBeInTheDocument();
    expect(asked).toBe(0);
  });
});
