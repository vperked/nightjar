import { createContext, useContext } from 'react';
import type { AppConfig } from './types';

// ConfigContext shares /api/config (which features are on) with the pages, once loaded.
export const ConfigContext = createContext<AppConfig | undefined>(undefined);

export const useConfig = () => useContext(ConfigContext);

// useCanEdit is false for a viewer, who is signed in read-only: pages leave out what would change
// things (the server refuses those anyway), so a viewer never meets a button that only fails.
export const useCanEdit = () => useContext(ConfigContext)?.account?.role !== 'viewer';

// browserZone is the browser's own time zone name, for saying whose clock a time is on.
export const browserZone = () => {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'your time zone';
  } catch {
    return 'your time zone';
  }
};

// useServerZone is the server's time zone (weekly maintenance and quiet hours follow its clock),
// with shift, the minutes to add to a server time to get the browser's: 0 when they agree.
export function useServerZone() {
  const tz = useContext(ConfigContext)?.time_zone;
  if (!tz) return undefined;
  const here = -new Date().getTimezoneOffset() * 60;
  return { name: tz.name, shift: Math.round((here - tz.offset) / 60) };
}
