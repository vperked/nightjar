import { useEffect, useRef, useState } from 'react';

const reduced = () => typeof window !== 'undefined' && !!window.matchMedia?.('(prefers-reduced-motion: reduce)').matches;

// Num shows a number that glides to each new value instead of jumping, formatted by format.
export function Num({ value, format, duration = 500 }: { value: number; format: (n: number) => string; duration?: number }) {
  const [shown, setShown] = useState(value);
  const from = useRef(value);
  const frame = useRef(0);
  useEffect(() => {
    const start = from.current;
    if (start === value || reduced() || !Number.isFinite(start) || !Number.isFinite(value)) {
      from.current = value;
      setShown(value);
      return;
    }
    const t0 = performance.now();
    const step = (t: number) => {
      const p = Math.min(1, (t - t0) / duration);
      const eased = 1 - Math.pow(1 - p, 3);
      const v = start + (value - start) * eased;
      from.current = v;
      setShown(v);
      if (p < 1) frame.current = requestAnimationFrame(step);
    };
    frame.current = requestAnimationFrame(step);
    return () => cancelAnimationFrame(frame.current);
  }, [value, duration]);
  return <>{format(shown)}</>;
}
