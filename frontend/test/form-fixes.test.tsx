import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { CheckForm } from '../src/pages/CheckForm';

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
});

describe('CheckForm', () => {
  const posted = (m: { mock: { calls: Parameters<typeof fetch>[] } }) => m.mock.calls.filter(([u]) => String(u) === '/api/checks' || String(u) === '/api/checks/test');
  const pick = (type: RegExp) => {
    fireEvent.click(screen.getByRole('combobox', { name: 'Check type' }));
    fireEvent.mouseDown(screen.getByRole('option', { name: type }));
  };

  it('starts the target afresh on becoming a heartbeat, so the server makes its token', async () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => json({ check: { name: 'backup' } }, 201));
    const onSaved = vi.fn();
    render(<CheckForm onSaved={onSaved} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'backup' } });
    fireEvent.change(screen.getByLabelText('Target'), { target: { value: 'https://example.com' } });
    pick(/^Heartbeat/);
    expect(screen.getByText('Made when the check is added.')).toBeInTheDocument();
    expect(screen.queryByText(/ping\/https/)).not.toBeInTheDocument();
    fireEvent.change(screen.getByLabelText('Schedule'), { target: { value: '@daily' } });
    fireEvent.click(screen.getByText('Add check'));
    await waitFor(() => expect(onSaved).toHaveBeenCalled());
    const post = posted(fetchMock).find(([, i]) => i?.method === 'POST')!;
    expect(JSON.parse(post[1]?.body as string)).toMatchObject({ type: 'heartbeat', target: '' });
  });

  it('gives an edited heartbeat its own token back after trying another type', () => {
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => json({ plugins: [], available: false, containers: [] }));
    const initial = {
      name: 'backup',
      type: 'heartbeat' as const,
      target: 'q7Lr2xVb9KpTn4WcYs3mEa',
      interval: 30,
      timeout: 10,
      retries: 0,
      expected_status: null,
      keyword: '',
      verify_tls: true,
      tls_warn_days: 14,
      paused: false,
      retry_interval: 30,
      ping_count: 3,
      ping_interval: 0.2,
      schedule: '@daily',
      grace: 600,
      max_runtime: 0,
    };
    render(<CheckForm initial={initial} onSaved={() => {}} onCancel={() => {}} />);
    pick(/^HTTP/);
    expect(screen.getByLabelText('Target')).toHaveValue('');
    pick(/^Heartbeat/);
    expect(screen.getByText(/ping\/q7Lr2xVb9KpTn4WcYs3mEa$/)).toBeInTheDocument();
  });

  it('says which expected statuses it cannot read, and sends nothing', () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => json({}));
    render(<CheckForm onSaved={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'web' } });
    fireEvent.change(screen.getByLabelText('Target'), { target: { value: 'https://example.com' } });
    fireEvent.change(screen.getByLabelText('Expected status codes'), { target: { value: '200, 200-299' } });
    fireEvent.submit(screen.getByText('Add check').closest('form')!);
    expect(screen.getByRole('alert')).toHaveTextContent('“200-299” is not a status code');
    expect(posted(fetchMock)).toHaveLength(0);
  });

  it('will not save an emptied number, which the server would swap for its default', () => {
    const fetchMock = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => json({}));
    render(<CheckForm onSaved={() => {}} onCancel={() => {}} />);
    fireEvent.change(screen.getByLabelText('Name'), { target: { value: 'web' } });
    fireEvent.change(screen.getByLabelText('Target'), { target: { value: 'https://example.com' } });
    fireEvent.change(screen.getByLabelText('Timeout'), { target: { value: '' } });
    expect(screen.getByLabelText('Timeout')).toHaveValue(null);
    expect(screen.getByLabelText('Timeout')).toBeRequired();
    fireEvent.submit(screen.getByText('Add check').closest('form')!);
    expect(screen.getByRole('alert')).toHaveTextContent('Timeout is empty');
    expect(posted(fetchMock)).toHaveLength(0);
  });
});
