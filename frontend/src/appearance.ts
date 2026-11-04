import { useSyncExternalStore } from 'react';
import { useConfig } from './api/ConfigContext';
import type { Appearance } from './api/types';

// The dashboard's look comes from the config, except while Settings → Appearance is being edited:
// then the draft is shown everywhere at once, as a live preview, until it is saved or discarded.

export const DEFAULT_APPEARANCE: Appearance = { style: 'matrix', charts: '', density: 'comfortable', css: '' };

let preview: Appearance | null = null;
const listeners = new Set<() => void>();

// previewAppearance shows a draft look in place of the saved one; null goes back to the saved one.
export function previewAppearance(a: Appearance | null) {
  preview = a;
  listeners.forEach((l) => l());
}

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

// useAppearance is the look in force: the draft being previewed, else the saved one.
export function useAppearance(): Appearance {
  const saved = useConfig()?.appearance;
  const draft = useSyncExternalStore(subscribe, () => preview);
  return draft ?? saved ?? DEFAULT_APPEARANCE;
}

// chartsOf is how charts and meters are drawn: lines and bars unless dots are chosen.
export const chartsOf = (a: Appearance): 'dots' | 'lines' => a.charts || 'lines';

// useCharts is how charts and meters are drawn now.
export const useCharts = () => chartsOf(useAppearance());
