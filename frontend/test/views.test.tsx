import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { useState } from 'react';
import { MemoryRouter, Route, Routes, useLocation, useNavigate, type NavigateFunction } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { api, ApiError } from '../src/api/client';
import { ConfigContext } from '../src/api/ConfigContext';
import { ServicesProvider } from '../src/api/ServicesContext';
import type { ActivityEntry, AnalyticsResponse, AttackView, FirewallRule, FirewallStatus, Service, SSHAccess } from '../src/api/types';
import { ActivityPanel } from '../src/components/ActivityPanel';
import { AttackWatch } from '../src/components/Attacks';
import { FirewallManager, type FirewallTarget, type SSHTarget } from '../src/components/Firewall';
import { Select, type Option } from '../src/components/Select';
import { TimePicker } from '../src/components/TimePicker';
import { ToastProvider } from '../src/components/Toasts';
import { AnalyticsPage } from '../src/pages/Analytics';
import { DocsPage } from '../src/pages/Docs';
import { ServerDetail, ServersPage } from '../src/pages/Servers';
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

  it('lets only Nightjar reach a port: an allow from its address, then a block for everyone else', async () => {
    const { t, added } = target({ dashboard: '198.51.100.10' });
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: '+ Add a rule' }));
    fireEvent.click(screen.getByRole('radio', { name: /^Allow/ }));
    fireEvent.click(screen.getByRole('radio', { name: /^Nightjar/ }));
    fireEvent.change(screen.getByLabelText('Port'), { target: { value: '9101' } });
    fireEvent.change(screen.getByLabelText('Protocol'), { target: { value: 'tcp' } });
    // Picking Nightjar ticks "keep everyone else out".
    expect(screen.getByRole('checkbox', { name: /Keep everyone else out/ })).toBeChecked();
    fireEvent.click(screen.getByLabelText('Port').closest('form')!.querySelector('button[type=submit]')!);
    await waitFor(() => expect(added).toHaveLength(2));
    expect(added[0]).toMatchObject({ action: 'allow', source: '198.51.100.10', port: '9101', proto: 'tcp' });
    expect(added[1]).toMatchObject({ action: 'block', port: '9101', proto: 'tcp' });
    expect(added[1].source).toBeFalsy();
  });

  it('adds only the allow when "keep everyone else out" is unticked, and offers no Nightjar on this machine', async () => {
    const { t, added } = target({ dashboard: '198.51.100.10' });
    const view = render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: '+ Add a rule' }));
    fireEvent.click(screen.getByRole('radio', { name: /^Allow/ }));
    fireEvent.click(screen.getByRole('radio', { name: /^Nightjar/ }));
    fireEvent.change(screen.getByLabelText('Port'), { target: { value: '9101' } });
    fireEvent.click(screen.getByRole('checkbox', { name: /Keep everyone else out/ }));
    fireEvent.click(screen.getByLabelText('Port').closest('form')!.querySelector('button[type=submit]')!);
    await waitFor(() => expect(added).toHaveLength(1));
    expect(added[0]).toMatchObject({ action: 'allow', source: '198.51.100.10' });
    view.unmount();

    // This machine's own firewall has no dashboard address: no Nightjar choice.
    const { t: local } = target();
    render(
      <ToastProvider>
        <FirewallManager target={local} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: '+ Add a rule' }));
    expect(screen.queryByRole('radio', { name: /^Nightjar/ })).not.toBeInTheDocument();
  });

  it('says a port a rule shuts is open only to those allowed ahead of it, naming Nightjar', async () => {
    const { t } = target({
      dashboard: '198.51.100.10',
      listening: [
        { port: 9101, proto: 'tcp', program: 'nightjar-agent', blocked: true, only: ['198.51.100.10'] },
        { port: 8080, proto: 'tcp', program: 'web', blocked: true },
        { port: 49622, proto: 'tcp', program: 'sshd', allowed: true },
      ],
    });
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    expect(await screen.findByText('only Nightjar')).toBeInTheDocument();
    expect(screen.getByText('closed by a rule')).toBeInTheDocument();
    expect(screen.getAllByText('open to anyone')).toHaveLength(1);
  });

  it('offers Secure: SSH… (Block SSH) and, on a server, Agent (only Nightjar on the agent port)', async () => {
    const { t, added } = target({
      dashboard: '198.51.100.10',
      open: [
        { port: 22, what: 'SSH' },
        { port: 9101, what: 'this agent' },
      ],
    });
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: '+ Add a rule' }));
    const secure = () => within(screen.getByRole('group', { name: 'Secure' }));
    fireEvent.click(secure().getByRole('button', { name: 'Agent' }));
    expect(screen.getByLabelText('Port')).toHaveValue('9101');
    expect(screen.getByRole('radio', { name: /^Nightjar/ })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: /Keep everyone else out/ })).toBeChecked();
    fireEvent.click(screen.getByLabelText('Port').closest('form')!.querySelector('button[type=submit]')!);
    await waitFor(() => expect(added).toHaveLength(2));
    expect(added[0]).toMatchObject({ action: 'allow', source: '198.51.100.10', port: '9101', proto: 'tcp' });
    expect(added[1]).toMatchObject({ action: 'block', port: '9101', proto: 'tcp' });

    fireEvent.click(await screen.findByRole('button', { name: '+ Add a rule' }));
    fireEvent.click(secure().getByRole('button', { name: 'SSH…' }));
    expect(await screen.findByText('Who should still get in over SSH?')).toBeInTheDocument();
  });

  it('Secure email adds the mail rules: 25 open, TLS ports limited, plain-text IMAP and POP3 blocked', async () => {
    const { t, added } = target();
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: '+ Add a rule' }));
    fireEvent.click(within(screen.getByRole('group', { name: 'Secure' })).getByRole('button', { name: 'Email…' }));
    const box = within(screen.getByRole('group', { name: 'Secure email' }));
    expect(box.getByText(/Block ports 110 and 143/)).toBeInTheDocument();
    fireEvent.click(box.getByRole('button', { name: 'Add these rules' }));
    await waitFor(() => expect(added).toHaveLength(3));
    expect(added[0]).toMatchObject({ action: 'allow', port: '25', proto: 'tcp' });
    expect(added[1]).toMatchObject({ action: 'limit', port: '465,587,993', proto: 'tcp', rate: '30/min' });
    expect(added[2]).toMatchObject({ action: 'block', port: '110,143', proto: 'tcp' });
    await waitFor(() => expect(screen.queryByRole('group', { name: 'Secure email' })).not.toBeInTheDocument());
  });

  it('Secure web opens the site (80/443, HTTP/3) and shuts the databases behind it', async () => {
    const { t, added } = target();
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: '+ Add a rule' }));
    fireEvent.click(within(screen.getByRole('group', { name: 'Secure' })).getByRole('button', { name: 'Web…' }));
    const box = within(screen.getByRole('group', { name: 'Secure web' }));
    expect(box.getByText(/PostgreSQL \(5432\)/)).toBeInTheDocument();
    fireEvent.click(box.getByRole('button', { name: 'Add these rules' }));
    await waitFor(() => expect(added).toHaveLength(3));
    expect(added[0]).toMatchObject({ action: 'allow', port: '80,443', proto: 'tcp' });
    expect(added[1]).toMatchObject({ action: 'allow', port: '443', proto: 'udp' });
    expect(added[2]).toMatchObject({ action: 'block', port: '3306,5432,6379,9200,11211,27017', proto: 'tcp' });
    await waitFor(() => expect(screen.queryByRole('group', { name: 'Secure web' })).not.toBeInTheDocument());
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

  it('counts only allowed ports as open to anyone while the machine’s own firewall drops', async () => {
    const { t } = target({
      listening: [
        { port: 22, proto: 'tcp', program: 'sshd', allowed: true },
        { port: 8080, proto: 'tcp', program: 'web' },
      ],
    });
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    const tile = (await screen.findByText('Open to anyone')).parentElement!;
    expect(within(tile).getByText('1', { selector: '.readout-value' })).toBeInTheDocument();
    expect(screen.getByText('Dropped', { selector: '.readout-value' })).toBeInTheDocument();
  });

  it('keeps the Keep/Undo banner when a read sent before the change answers after it', async () => {
    const { t } = target();
    const before = await t.load();
    const after = { ...before, default: 'drop', pending: { default: 'drop', until: Date.now() / 1000 + 60 } } as FirewallStatus;
    // The first read answers at once; the 15 s one hangs, with the firewall from before the change.
    let reads = 0;
    let release: () => void = () => {};
    t.load = () => (++reads === 2 ? new Promise((res) => (release = () => res(before))) : Promise.resolve(reads === 1 ? before : after));
    t.add = async () => after;
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Add a rule for 22/tcp' }));
    await act(async () => {
      vi.advanceTimersByTime(15_000);
    });
    expect(reads).toBe(2);
    fireEvent.click(screen.getByRole('radio', { name: /^Allow/ }));
    fireEvent.click(screen.getByLabelText('Port').closest('form')!.querySelector('button[type=submit]')!);
    expect(await screen.findByText(/If this page still answers, keep it/)).toBeInTheDocument();
    await act(async () => release());
    expect(screen.getByText(/If this page still answers, keep it/)).toBeInTheDocument();
  });

  it('keeps the last firewall read, rule form and all, when a later read fails', async () => {
    const { t } = target();
    const st = await t.load();
    let reads = 0;
    t.load = () => (++reads === 1 ? Promise.resolve(st) : Promise.reject(new Error('agent offline')));
    vi.useFakeTimers({ shouldAdvanceTime: true });
    render(
      <ToastProvider>
        <FirewallManager target={t} />
      </ToastProvider>,
    );
    fireEvent.click(await screen.findByRole('button', { name: '+ Add a rule' }));
    fireEvent.change(screen.getByLabelText('Port'), { target: { value: '8443' } });
    await act(async () => {
      vi.advanceTimersByTime(15_000);
    });
    expect(await screen.findByText(/Could not read the firewall again \(agent offline\)/)).toBeInTheDocument();
    expect(screen.getByLabelText('Port')).toHaveValue('8443');
  });

  describe('Block SSH', () => {
    // sshTarget is a machine's SSH logins that behaves like the agent: keys counted per account,
    // keys only waiting two minutes to be kept.
    function sshTarget(over: Partial<SSHAccess> = {}) {
      let a: SSHAccess = {
        ports: [22],
        password_login: true,
        keys_only: false,
        users: [
          { name: 'alex', keys: 0 },
          { name: 'root', keys: 1 },
        ],
        ...over,
      };
      const calls: string[] = [];
      const ssh: SSHTarget = {
        load: async () => a,
        addKey: async (user, key) => {
          calls.push(`key ${user} ${key}`);
          a = { ...a, users: a.users.map((u) => (u.name === user ? { ...u, keys: u.keys + 1 } : u)) };
          return a;
        },
        keysOnly: async (on, user) => {
          calls.push(`keys-only ${on} ${user ?? ''}`);
          a = on ? { ...a, password_login: false, keys_only: true, pending: { until: Date.now() / 1000 + 120 } } : { ...a, password_login: true, keys_only: false, pending: undefined };
          return a;
        },
        keep: async (keep) => {
          calls.push(`keep ${keep}`);
          a = keep ? { ...a, pending: undefined } : { ...a, password_login: true, keys_only: false, pending: undefined };
          return a;
        },
      };
      return { ssh, calls };
    }
    const nightjarKey = {
      path: '/k/nightjar',
      name: 'nightjar',
      type: 'ED25519',
      fingerprint: 'SHA256:abc',
      public: 'ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIHk3m9Zq2T0bWc7yPj1sVx nightjar@box',
      own: true,
    };

    it('lets only one address reach SSH: an allow from it, then a block for everyone on the SSH port', async () => {
      const { t, added } = target({ dashboard: '198.51.100.10' });
      t.ssh = sshTarget({ ports: [2222] }).ssh;
      render(
        <ToastProvider>
          <FirewallManager target={t} />
        </ToastProvider>,
      );
      expect(await screen.findByText('passwords on')).toBeInTheDocument();
      fireEvent.click(screen.getByRole('button', { name: 'Block SSH…' }));
      expect(screen.getByText('Who should still get in over SSH?')).toBeInTheDocument();
      fireEvent.click(screen.getByRole('radio', { name: /^An address/ }));
      // Nightjar's own address is a quick fill on a server.
      expect(screen.getByRole('button', { name: /Nightjar 198\.51\.100\.10/ })).toBeInTheDocument();
      fireEvent.change(screen.getByLabelText('Address or network allowed to reach SSH'), { target: { value: '203.0.113.7' } });
      fireEvent.click(screen.getByRole('button', { name: 'Block SSH' }));
      await waitFor(() => expect(added).toHaveLength(2));
      // The ports are sshd's own, not the firewall's guess.
      expect(added[0]).toMatchObject({ action: 'allow', source: '203.0.113.7', port: '2222', proto: 'tcp' });
      expect(added[1]).toMatchObject({ action: 'block', port: '2222', proto: 'tcp' });
      expect(added[1].source).toBeFalsy();
      expect(await screen.findByText('Only 203.0.113.7 can reach SSH on nas')).toBeInTheDocument();
    });

    it('shows a refused block as the firewall said it, and keeps the allow', async () => {
      const { t, added } = target();
      t.add = async (rule) => {
        added.push(rule);
        if (rule.action === 'block') throw new ApiError('that would cut off the SSH login open now from 192.168.1.20');
        return t.load();
      };
      render(
        <ToastProvider>
          <FirewallManager target={t} />
        </ToastProvider>,
      );
      fireEvent.click(await screen.findByRole('button', { name: 'Block SSH…' }));
      fireEvent.click(screen.getByRole('radio', { name: /^An address/ }));
      fireEvent.change(screen.getByLabelText('Address or network allowed to reach SSH'), { target: { value: '203.0.113.7' } });
      fireEvent.click(screen.getByRole('button', { name: 'Block SSH' }));
      expect(await screen.findByText('that would cut off the SSH login open now from 192.168.1.20')).toBeInTheDocument();
      // Without sshd's own ports, the firewall's SSH port is used.
      expect(added.map((r) => `${r.action} ${r.port}`)).toEqual(['allow 22', 'block 22']);
      expect(screen.getByText(/The allow for 203\.0\.113\.7 went in/)).toBeInTheDocument();
    });

    it('adds a key to root, asks "Only allow keys?", and on Yes turns passwords off with Keep to press', async () => {
      vi.spyOn(api, 'sshKeys').mockResolvedValue({ keys: [nightjarKey] });
      const { t } = target();
      const { ssh, calls } = sshTarget();
      t.ssh = ssh;
      render(
        <ToastProvider>
          <FirewallManager target={t} />
        </ToastProvider>,
      );
      fireEvent.click(await screen.findByRole('button', { name: 'Block SSH…' }));
      fireEvent.click(screen.getByRole('radio', { name: /^A key/ }));
      expect(await screen.findByRole('radio', { name: /nightjar SHA256:abc/ })).toBeChecked();
      // root is picked, with how many keys each account has.
      expect(screen.getByLabelText('Account')).toHaveValue('root');
      expect(screen.getByRole('option', { name: 'alex · 0 keys' })).toBeInTheDocument();
      fireEvent.click(screen.getByRole('button', { name: 'Add key' }));
      expect(await screen.findByText(/Only allow keys\?/)).toBeInTheDocument();
      expect(calls).toEqual([`key root ${nightjarKey.public}`]);
      fireEvent.click(screen.getByRole('button', { name: 'Yes, only keys' }));
      const keep = await screen.findByRole('button', { name: 'Keep keys only' });
      expect(calls[1]).toBe('keys-only true root');
      expect(screen.getByText(/Passwords come back by themselves in/)).toBeInTheDocument();
      expect(screen.getByText('keys only')).toBeInTheDocument();
      fireEvent.click(keep);
      await waitFor(() => expect(calls[2]).toBe('keep true'));
      await waitFor(() => expect(screen.queryByRole('button', { name: 'Keep keys only' })).not.toBeInTheDocument());
      // Kept: passwords can be let back in, after a yes.
      fireEvent.click(screen.getByRole('button', { name: 'Allow passwords again' }));
      fireEvent.click(screen.getByRole('button', { name: 'Allow' }));
      await waitFor(() => expect(calls[3]).toBe('keys-only false '));
    });

    it('refuses a pasted private key before sending it', async () => {
      vi.spyOn(api, 'sshKeys').mockResolvedValue({ keys: [] });
      const { t } = target();
      const { ssh, calls } = sshTarget();
      t.ssh = ssh;
      render(
        <ToastProvider>
          <FirewallManager target={t} />
        </ToastProvider>,
      );
      fireEvent.click(await screen.findByRole('button', { name: 'Block SSH…' }));
      fireEvent.click(screen.getByRole('radio', { name: /^A key/ }));
      // With no keys of Nightjar's, pasting is picked.
      const box = await screen.findByLabelText('Public key');
      fireEvent.change(box, { target: { value: '-----BEGIN OPENSSH PRIVATE KEY-----\nb3BlbnNzaC1rZXktdjEAAAAABG5vbmU=\n-----END OPENSSH PRIVATE KEY-----' } });
      expect(screen.getByRole('alert')).toHaveTextContent(/private key, which should never leave your computer/);
      const add = screen.getByRole('button', { name: 'Add key' });
      expect(add).toBeDisabled();
      fireEvent.submit(add.closest('form')!);
      await act(async () => {});
      expect(calls).toEqual([]);
    });

    it('hides the SSH state when the agent cannot say, and says so only on the key path', async () => {
      const { t } = target();
      t.ssh = { ...sshTarget().ssh, load: async () => Promise.reject(new ApiError('nas’s agent is too old to manage SSH logins: update the agent')) };
      render(
        <ToastProvider>
          <FirewallManager target={t} />
        </ToastProvider>,
      );
      fireEvent.click(await screen.findByRole('button', { name: 'Block SSH…' }));
      expect(screen.queryByText(/^SSH:/)).not.toBeInTheDocument();
      expect(screen.queryByText(/too old/)).not.toBeInTheDocument();
      fireEvent.click(screen.getByRole('radio', { name: /^A key/ }));
      expect(screen.getByText('nas’s agent is too old to manage SSH logins: update the agent')).toBeInTheDocument();
    });
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

describe('viewer: change controls left out', () => {
  // as wraps a page in a signed-in account: the admin, or a read-only viewer.
  const as = (role: 'admin' | 'viewer', page: React.ReactNode) => (
    <ConfigContext.Provider value={{ auth_required: true, authorized: true, status_page_enabled: false, status_page_title: '', account: { configured: true, username: 'sam', role } }}>
      <ToastProvider>{page}</ToastProvider>
    </ConfigContext.Provider>
  );
  const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

  function fwTarget(): FirewallTarget {
    const rule = { id: 'r1', action: 'block', source: '203.0.113.7' } as FirewallRule;
    const st = {
      full: true,
      tool: 'ufw',
      policy: 'DROP',
      default: '',
      open: [],
      rules: [rule, { ...rule, id: 'r2', source: '198.51.100.9' }],
      listening: [{ port: 8080, proto: 'tcp', program: 'web', allowed: false }],
      drops: [
        { at: 1, source: '192.0.2.5', rule: 'default' },
        { at: 2, source: '192.0.2.5', rule: 'default' },
      ],
      pending: { default: 'drop', until: Date.now() / 1000 + 60 },
    } as unknown as FirewallStatus;
    const same = async () => st;
    return { key: 'nas', name: 'nas', load: same, add: same, update: same, remove: same, order: same, setDefault: same, keep: same };
  }

  it('shows a viewer the firewall rules read-only, and the admin every control', async () => {
    const t = fwTarget();
    const { unmount } = render(as('viewer', <FirewallManager target={t} />));
    expect(await screen.findByText('203.0.113.7')).toBeInTheDocument();
    expect(screen.getByText('198.51.100.9')).toBeInTheDocument();
    for (const name of ['+ Add a rule', 'Edit', 'Pause', 'Remove', 'Keep it', 'Undo', 'Block 192.0.2.5', 'Add a rule for 8080/tcp']) expect(screen.queryByRole('button', { name })).toBeNull();
    expect(screen.queryByRole('button', { name: /^Move (up|down)/ })).toBeNull();
    // The default stays to read, greyed out with the reason.
    const drop = screen.getByRole('radio', { name: /^Drop it/ });
    expect(drop).toBeDisabled();
    expect(drop).toHaveAttribute('title', "Viewers can't change this");
    unmount();

    render(as('admin', <FirewallManager target={t} />));
    expect(await screen.findByRole('button', { name: '+ Add a rule' })).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Edit' })).toHaveLength(2);
    expect(screen.getAllByRole('button', { name: /^Move down/ })).toHaveLength(2);
    for (const name of ['Keep it', 'Undo', 'Block 192.0.2.5', 'Add a rule for 8080/tcp']) expect(screen.getByRole('button', { name })).toBeInTheDocument();
    expect(screen.getByRole('radio', { name: /^Drop it/ })).toBeEnabled();
    expect(screen.getByRole('radio', { name: /^Drop it/ })).not.toHaveAttribute('title');
  });

  it('leaves the defense switch and unblock out for a viewer', () => {
    const view = {
      attacking: false,
      findings: [],
      busy: {},
      rx_bits: 0,
      rx_packets: 0,
      learning: false,
      history: [],
      defense: { on: true, active: true, measures: [], blocked: [{ address: '192.0.2.9', until: Date.now() / 1000 + 600, why: 'SYN flood', where: 'firewall' }], web: false },
    } as AttackView;
    const toggle = vi.fn(async () => {});
    const { unmount } = render(as('viewer', <AttackWatch view={view} now={Date.now() / 1000} onToggle={toggle} onUnblock={toggle} />));
    expect(screen.getByText('192.0.2.9')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Turn off/ })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Unblock' })).toBeNull();
    unmount();
    render(as('admin', <AttackWatch view={view} now={Date.now() / 1000} onToggle={toggle} onUnblock={toggle} />));
    expect(screen.getByRole('button', { name: 'Turn off…' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Unblock' })).toBeInTheDocument();
  });

  it('gives a viewer the servers without adding or choosing them', async () => {
    const host = (id: string) => ({ id, name: id, address: id, added: 0, kind: 'ssh', status: 'online', readings: null, history: [], problems: [] });
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => (String(input) === '/api/hosts' ? reply({ hosts: [host('a'), host('b')], port: 9101 }) : reply({}, 404)));
    const page = (
      <MemoryRouter>
        <ServersPage />
      </MemoryRouter>
    );
    const { unmount } = render(as('viewer', page));
    expect(await screen.findByRole('link', { name: /^a:/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add a server' })).toBeNull();
    expect(screen.queryByRole('button', { name: 'Select' })).toBeNull();
    unmount();
    render(as('admin', page));
    expect(await screen.findByRole('button', { name: 'Add a server' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Select' })).toBeInTheDocument();
  });

  it('shows a viewer a server and its SSH login, without editing, removing or using it', async () => {
    const host = {
      id: 'h1',
      name: 'nas',
      address: 'https://nas:9101',
      added: 0,
      kind: 'agent',
      status: 'online',
      readings: null,
      history: [],
      problems: [],
      ssh: { key: '/srv/nightjar/ssh/laptop', user: 'root', port: 22 },
      ssh_check: { ok: true, at: 1, as: 'root' },
    };
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => (String(input) === '/api/hosts/h1' ? reply({ host }) : reply({}, 404)));
    const page = (
      <MemoryRouter initialEntries={['/servers/h1']}>
        <ServicesProvider enabled>
          <Routes>
            <Route path="/servers/:id" element={<ServerDetail />} />
          </Routes>
        </ServicesProvider>
      </MemoryRouter>
    );
    const controls = ['Save', 'Remove…', 'Test again', 'Restart agent', 'Reinstall agent', 'Reboot…', 'Change login…', 'Forget…'];
    const { unmount } = render(as('viewer', page));
    expect(await screen.findByText('Logs in as')).toBeInTheDocument();
    for (const name of controls) expect(screen.queryByRole('button', { name })).toBeNull();
    expect(screen.queryByLabelText('New token')).toBeNull();
    expect(screen.queryByRole('link', { name: 'add a check →' })).toBeNull();
    // Looking around stays: the firewall link and the way down to Manage.
    expect(screen.getByRole('link', { name: 'Firewall' })).toBeInTheDocument();
    unmount();

    render(as('admin', page));
    expect(await screen.findByText('Logs in as')).toBeInTheDocument();
    for (const name of controls) expect(screen.getByRole('button', { name })).toBeInTheDocument();
    expect(screen.getByLabelText('New token')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'add a check →' })).toBeInTheDocument();
  });
});
