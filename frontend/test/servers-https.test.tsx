import { fireEvent, render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter, Route, Routes } from 'react-router';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ServicesProvider } from '../src/api/ServicesContext';
import { ToastProvider } from '../src/components/Toasts';
import { ServerDetail } from '../src/pages/Servers';

// A plain-HTTP agent's token has crossed the network; moving it to HTTPS takes a new one.

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

afterEach(() => {
  vi.restoreAllMocks();
  localStorage.clear();
});

const page = () =>
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

describe('moving an agent to HTTPS', () => {
  it('makes a new token, shows the reinstall command and saves both', async () => {
    const host = { id: 'h1', name: 'nas', address: 'http://nas:9200', added: 0, kind: 'agent', status: 'online', readings: null, history: [], problems: [] };
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async (input, init) => {
      const url = String(input);
      if (url === '/api/hosts/token') return json({ token: 'fresh-token-0123456789abcdef', port: 9101 });
      if (url === '/api/hosts/h1') return json({ host: init?.method === 'PUT' ? { ...host, address: 'https://nas:9200' } : host });
      return json({}, 404);
    });
    page();
    fireEvent.click(await screen.findByRole('button', { name: 'Move to HTTPS…' }));
    expect(await screen.findByText('sudo nightjar-agent install -token fresh-token-0123456789abcdef -tls=true -listen :9200')).toBeInTheDocument();
    expect(screen.getByLabelText('Address')).toHaveValue('https://nas:9200');
    expect(screen.getByLabelText('New token')).toHaveValue('fresh-token-0123456789abcdef');

    fireEvent.click(screen.getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(fetchMock.mock.calls.some(([, i]) => i?.method === 'PUT')).toBe(true));
    const put = fetchMock.mock.calls.find(([, i]) => i?.method === 'PUT');
    expect(JSON.parse(String(put?.[1]?.body))).toMatchObject({ kind: 'agent', address: 'https://nas:9200', token: 'fresh-token-0123456789abcdef' });
  });

  it('offers no move to an agent already on HTTPS', async () => {
    const host = { id: 'h1', name: 'nas', address: 'https://nas:9101', added: 0, kind: 'agent', status: 'online', readings: null, history: [], problems: [] };
    vi.spyOn(globalThis, 'fetch').mockImplementation(async (input) => (String(input) === '/api/hosts/h1' ? json({ host }) : json({}, 404)));
    page();
    expect(await screen.findByText(/Encrypted with HTTPS/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Move to HTTPS…' })).toBeNull();
  });
});
