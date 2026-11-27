import { useEffect, useState } from 'react';

// useNow is the current time in seconds, refreshed every intervalMs. A hidden tab skips the ticks
// (nothing shows them) and catches up the moment it is shown again.
export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(() => Date.now() / 1000);
  useEffect(() => {
    const tick = () => {
      if (document.visibilityState !== 'hidden') setNow(Date.now() / 1000);
    };
    const id = setInterval(tick, intervalMs);
    document.addEventListener('visibilitychange', tick);
    return () => {
      clearInterval(id);
      document.removeEventListener('visibilitychange', tick);
    };
  }, [intervalMs]);
  return now;
}
