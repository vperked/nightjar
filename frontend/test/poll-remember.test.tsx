import { act, renderHook, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { forgetPolls, usePoll } from '../src/hooks/usePoll';

const never = () => new Promise<string>(() => {});

describe('usePoll remembers', () => {
  it('shows the last answer at once when a page opens again', async () => {
    const units = () => Promise.resolve('units');
    const first = renderHook(() => usePoll(units, 60000));
    await waitFor(() => expect(first.result.current.data).toBe('units'));
    first.unmount();

    // Opened again: the answer is there on the first render, while a fresh one is fetched.
    let fresh: (v: string) => void = () => {};
    const slow = () => new Promise<string>((r) => (fresh = r));
    const again = renderHook(() => usePoll(slow, 60000, true, units));
    expect(again.result.current.data).toBe('units');
    expect(again.result.current.loading).toBe(false);
    act(() => fresh('units, newer'));
    await waitFor(() => expect(again.result.current.data).toBe('units, newer'));
  });

  it('keeps answers for different keys apart', async () => {
    const a = renderHook(() => usePoll(() => Promise.resolve('host a'), 60000, true, 'host:a'));
    await waitFor(() => expect(a.result.current.data).toBe('host a'));
    a.unmount();
    expect(renderHook(() => usePoll(never, 60000, true, 'host:b')).result.current.data).toBeUndefined();
    expect(renderHook(() => usePoll(never, 60000, true, 'host:a')).result.current.data).toBe('host a');
  });

  it('forgets everything on signing in or out', async () => {
    const a = renderHook(() => usePoll(() => Promise.resolve('admin only'), 60000, true, 'settings'));
    await waitFor(() => expect(a.result.current.data).toBe('admin only'));
    a.unmount();
    forgetPolls();
    const b = renderHook(() => usePoll(never, 60000, true, 'settings'));
    expect(b.result.current.data).toBeUndefined();
    expect(b.result.current.loading).toBe(true);
  });

  it('asks again as soon as the tab is shown after missing a turn', async () => {
    let calls = 0;
    const f = () => Promise.resolve(++calls);
    renderHook(() => usePoll(f, 20));
    await waitFor(() => expect(calls).toBe(1));
    const hidden = Object.getOwnPropertyDescriptor(Document.prototype, 'visibilityState');
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
    await new Promise((r) => setTimeout(r, 60));
    expect(calls).toBe(1);
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'visible' });
    act(() => void document.dispatchEvent(new Event('visibilitychange')));
    await waitFor(() => expect(calls).toBe(2));
    if (hidden) Object.defineProperty(document, 'visibilityState', hidden);
    else delete (document as unknown as Record<string, unknown>).visibilityState;
  });
});
