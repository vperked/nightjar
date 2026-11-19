import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { UpdatesSummary } from '../src/components/UpdatesSummary';

const reply = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
const target = (over: Record<string, unknown>) => ({ id: 'os', name: 'OS packages (apt)', kind: 'system', auto: false, can_update: true, ...over });

afterEach(() => vi.restoreAllMocks());

describe('a server page’s Updates card', () => {
  it('says what waits there and links to the Updates page picked to that server', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) =>
      String(input) === '/api/hosts/h1/updates'
        ? reply({
            enabled: true,
            interval: 21600,
            window: { start: 240, end: 360, label: '04:00–06:00' },
            commands_editable: true,
            kinds: [],
            checking: false,
            last_check: Date.now() / 1000 - 3600,
            targets: [
              target({ available: '3 packages', checked: 1 }),
              target({ id: 'plex', name: 'Plex Media Server', available: '1.42', checked: 1 }),
              target({ id: 'snaps', name: 'Snaps', checked: 1 }),
            ],
          })
        : reply({}, 404),
    );
    render(
      <MemoryRouter>
        <UpdatesSummary h={{ id: 'h1', name: 'nas' }} />
      </MemoryRouter>,
    );
    expect(await screen.findByText(/2 updates waiting: OS packages \(apt\), Plex Media Server/)).toBeInTheDocument();
    expect(screen.getByText(/auto-updates 04:00–06:00/)).toBeInTheDocument();
    for (const link of screen.getAllByRole('link')) expect(link).toHaveAttribute('href', '/updates?host=h1');
  });

  it('says why when the agent cannot answer', async () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => reply({ error: "this server's agent is too old for updates; it updates itself shortly, or reinstall it" }, 404));
    render(
      <MemoryRouter>
        <UpdatesSummary h={{ id: 'h1', name: 'nas' }} />
      </MemoryRouter>,
    );
    expect(await screen.findByText(/too old for updates/)).toBeInTheDocument();
  });
});
