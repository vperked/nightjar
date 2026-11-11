import { afterAll, beforeAll, describe, expect, it } from 'vitest';
import { timeTicks } from '../src/components/TrendChart';

// Ticks across the end of US daylight saving time (2am, 2026-11-01, New York).
describe('timeTicks across a clock change', () => {
  const tz = process.env.TZ;
  beforeAll(() => {
    process.env.TZ = 'America/New_York';
  });
  afterAll(() => {
    process.env.TZ = tz;
  });
  const local = (t: number) => {
    const d = new Date(t * 1000);
    return [d.getMonth() + 1, d.getDate(), d.getHours(), d.getMinutes()];
  };

  it('keeps daily ticks on local midnight, one per date', () => {
    const t0 = new Date(2026, 9, 29, 10).getTime() / 1000;
    const t1 = new Date(2026, 10, 4, 10).getTime() / 1000;
    const { ticks, step } = timeTicks(t0, t1, 7);
    expect(step).toBe(86400);
    expect(ticks.map(local)).toEqual([
      [10, 30, 0, 0],
      [10, 31, 0, 0],
      [11, 1, 0, 0],
      [11, 2, 0, 0],
      [11, 3, 0, 0],
      [11, 4, 0, 0],
    ]);
  });

  it('keeps multi-hour ticks on the local hours', () => {
    const t0 = new Date(2026, 9, 31, 20).getTime() / 1000;
    const t1 = new Date(2026, 10, 1, 20).getTime() / 1000;
    const { ticks, step } = timeTicks(t0, t1, 6);
    expect(step).toBe(6 * 3600);
    expect(ticks.map((t) => local(t).slice(1, 3))).toEqual([
      [1, 0],
      [1, 6],
      [1, 12],
      [1, 18],
    ]);
  });

  it('skips an hour the clocks jump over', () => {
    // Spring forward: 2am on 2026-03-08 does not exist in New York.
    const t0 = new Date(2026, 2, 8, 0).getTime() / 1000;
    const t1 = new Date(2026, 2, 8, 5).getTime() / 1000;
    const { ticks } = timeTicks(t0, t1, 6);
    expect(ticks.map((t) => local(t)[2])).toEqual([0, 1, 3, 4, 5]);
  });
});
