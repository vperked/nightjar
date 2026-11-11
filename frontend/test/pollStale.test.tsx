import { renderHook, waitFor } from '@testing-library/react';
import { describe, expect, it } from 'vitest';
import { usePoll } from '../src/hooks/usePoll';

describe('usePoll stale', () => {
  it('keeps the old data but marks it stale until the new fetcher answers', async () => {
    const week = () => Promise.resolve('7d');
    let answer: (v: string) => void = () => {};
    const month = () => new Promise<string>((r) => (answer = r));
    const { result, rerender } = renderHook(({ f }) => usePoll(f, 60000), { initialProps: { f: week } });
    await waitFor(() => expect(result.current.data).toBe('7d'));
    expect(result.current.stale).toBe(false);

    rerender({ f: month });
    // Stale from the very render the range changes, so nothing is drawn under the wrong label.
    expect(result.current.data).toBe('7d');
    expect(result.current.stale).toBe(true);

    answer('30d');
    await waitFor(() => expect(result.current.data).toBe('30d'));
    expect(result.current.stale).toBe(false);
  });

  it('is not stale before anything has arrived', () => {
    const { result } = renderHook(() => usePoll(() => new Promise<string>(() => {}), 60000));
    expect(result.current.stale).toBe(false);
  });
});
