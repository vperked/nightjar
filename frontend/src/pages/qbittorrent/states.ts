// How qBittorrent's many torrent states read on the page: a group, a word, and a tone.

export type Group = 'downloading' | 'stalled' | 'seeding' | 'paused' | 'error' | 'other';

// group sorts qBittorrent's many states into what someone watching the box cares about.
export function group(state: string): Group {
  if (state === 'stalleddl') return 'stalled';
  if (['downloading', 'forceddl', 'metadl', 'forcedmetadl', 'allocating', 'queueddl', 'checkingdl'].includes(state)) return 'downloading';
  if (['uploading', 'stalledup', 'forcedup', 'queuedup', 'checkingup'].includes(state)) return 'seeding';
  if (['pauseddl', 'stoppeddl', 'pausedup', 'stoppedup'].includes(state)) return 'paused';
  if (state === 'error' || state === 'missingfiles') return 'error';
  return 'other';
}

export const WORD: Record<string, string> = {
  downloading: 'Downloading',
  forceddl: 'Downloading (forced)',
  metadl: 'Getting metadata',
  forcedmetadl: 'Getting metadata',
  allocating: 'Allocating',
  queueddl: 'Queued',
  checkingdl: 'Checking',
  stalleddl: 'Stalled',
  uploading: 'Seeding',
  forcedup: 'Seeding (forced)',
  stalledup: 'Seeding',
  queuedup: 'Queued to seed',
  checkingup: 'Checking',
  pauseddl: 'Paused',
  stoppeddl: 'Stopped',
  pausedup: 'Completed',
  stoppedup: 'Completed',
  error: 'Error',
  missingfiles: 'Missing files',
  moving: 'Moving',
  checkingresumedata: 'Checking',
};

// Pill tones reuse the update states: blue busy, green good, amber attention, red broken.
export const TONE: Record<Group, string> = { downloading: 'busy', stalled: 'warn', seeding: 'good', paused: 'idle', error: 'crit', other: 'idle' };
export const ORDER: Group[] = ['error', 'stalled', 'downloading', 'seeding', 'paused', 'other'];

// UNKNOWN_ETA is how qBittorrent writes "no idea when".
export const UNKNOWN_ETA = 8640000;
