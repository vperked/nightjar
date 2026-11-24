import { afterEach, describe, expect, it, vi } from 'vitest';
import { api } from '../src/api/client';

type Early = { __nightjarConfig?: Promise<Response> };
const json = (v: unknown) => new Response(JSON.stringify(v), { headers: { 'Content-Type': 'application/json' } });

afterEach(() => {
  delete (window as Early).__nightjarConfig;
  vi.restoreAllMocks();
});

describe('the config asked for as the page arrived', () => {
  it('answers the first look at the config, once', async () => {
    (window as Early).__nightjarConfig = Promise.resolve(json({ version: 'early' }));
    const fetch = vi.spyOn(globalThis, 'fetch').mockImplementation(async () => json({ version: 'asked' }));
    expect(await api.config()).toEqual({ version: 'early' });
    expect(fetch).not.toHaveBeenCalled();
    expect(await api.config()).toEqual({ version: 'asked' });
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  it('asks again when the early request failed', async () => {
    const failed = Promise.reject(new TypeError('offline'));
    failed.catch(() => {});
    (window as Early).__nightjarConfig = failed;
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => json({ version: 'asked' }));
    expect(await api.config()).toEqual({ version: 'asked' });
  });

  it('is not taken by other requests', async () => {
    (window as Early).__nightjarConfig = Promise.resolve(json({ version: 'early' }));
    vi.spyOn(globalThis, 'fetch').mockImplementation(async () => json({ units: [] }));
    await api.units();
    expect((window as Early).__nightjarConfig).toBeDefined();
  });
});
