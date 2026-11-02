import type { Unit } from '../api/types';

// How a systemd service is doing, shared by the overview's attention card and the System services page.

export type Tone = 'good' | 'warn' | 'crit' | 'busy' | 'idle';

// verdict says what a service is doing in a word, with a tone, so colour never carries it alone.
export function verdict(u: Unit): [Tone, string] {
  if (u.sub === 'auto-restart') return ['crit', 'Restarting'];
  if (u.active === 'failed') return [u.ignored ? 'idle' : 'crit', u.ignored ? 'Failed (ignored)' : 'Failed'];
  if (u.flapping) return ['warn', 'Unstable'];
  if (u.active === 'activating' || u.active === 'reloading') return ['busy', 'Starting'];
  if (u.active === 'deactivating') return ['busy', 'Stopping'];
  if (u.active === 'active') return u.sub === 'exited' ? ['idle', 'Done'] : ['good', 'Running'];
  return ['idle', 'Stopped'];
}

export const isProblem = (u: Unit) => !u.ignored && (u.active === 'failed' || u.sub === 'auto-restart' || !!u.flapping);
