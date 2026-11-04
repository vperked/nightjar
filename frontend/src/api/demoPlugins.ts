import type { CheckResult, Plugin, PluginCard, PluginChange } from './types';

// The demo's plugins: the examples Nightjar ships, plus one that cannot run, to show what the
// page says then. Changes last as long as the tab.

const DIR = '/opt/nightjar/plugins';
const now = () => Date.now() / 1000;

const plugins: Plugin[] = [
  {
    id: 'load-card',
    name: 'System load',
    version: '1.0.0',
    description: 'Load averages, running processes and logged-in users, as a card on the overview.',
    author: 'Nightjar examples',
    provides: ['card'],
    run: 'run.sh',
    interval: 60,
    dir: `${DIR}/load-card`,
    enabled: true,
    values: {},
    secrets_set: [],
    last: { mode: 'card', at: now() - 42, ok: true, ms: 18, output: '5 items' },
  },
  {
    id: 'dns-resolve',
    name: 'DNS lookup',
    version: '1.0.0',
    description: 'A check that is up while a host name resolves, optionally to the address you expect.',
    author: 'Nightjar examples',
    provides: ['check'],
    run: 'run.sh',
    check: { argument: 'Host name', placeholder: 'example.com' },
    settings: [{ key: 'EXPECT', label: 'Expected address', type: 'text', help: 'Down when the name resolves to anything else. Empty accepts any address.', placeholder: '93.184.215.14' }],
    dir: `${DIR}/dns-resolve`,
    enabled: true,
    values: { EXPECT: '' },
    secrets_set: [],
    last: { mode: 'check', at: now() - 25, ok: true, ms: 9, output: 'example.com is 93.184.215.14' },
  },
  {
    id: 'alert-log',
    name: 'Alert log',
    version: '1.0.0',
    description: 'An alert channel that writes every alert as a line of JSON to a file, for scripts to pick up.',
    author: 'Nightjar examples',
    provides: ['notify'],
    run: 'run.sh',
    settings: [
      { key: 'FILE', label: 'File', type: 'text', default: 'alerts.jsonl', help: 'Relative to the plugin’s folder.', required: true },
      { key: 'MIN_SEVERITY', label: 'Only from', type: 'select', options: ['info', 'warning', 'critical'], default: 'info' },
    ],
    dir: `${DIR}/alert-log`,
    enabled: false,
    values: { FILE: 'alerts.jsonl', MIN_SEVERITY: 'info' },
    secrets_set: [],
  },
  {
    id: 'pihole',
    name: 'Pi-hole',
    version: '0.3.0',
    description: 'Queries and blocked share from a Pi-hole.',
    author: 'a friend',
    provides: ['card', 'check'],
    run: 'pihole.py',
    settings: [
      { key: 'URL', label: 'Address', type: 'text', placeholder: 'http://pi.hole', required: true },
      { key: 'PASSWORD', label: 'App password', type: 'secret', required: true },
    ],
    dir: `${DIR}/pihole`,
    problem: 'pihole.py can be changed by other users; run chmod go-w on it',
    enabled: false,
    values: { URL: '' },
    secrets_set: [],
  },
];

function loadCard(): PluginCard {
  const t = now();
  const wave = (k: number) => (0.6 + 0.35 * Math.sin(t / 300 + k)).toFixed(2);
  return {
    plugin: 'load-card',
    title: 'System load',
    items: [
      { label: '1 min', value: wave(0), tone: Number(wave(0)) > 0.9 ? 'warn' : undefined },
      { label: '5 min', value: wave(1) },
      { label: '15 min', value: wave(2) },
      { label: 'Processes', value: String(212 + Math.round(6 * Math.sin(t / 90))) },
      { label: 'Signed in', value: '1 user', hint: 'media on pts/0' },
    ],
    updated: t - (t % 60),
  };
}

function checkDNS(argument: string): CheckResult {
  const name = argument.trim() || 'example.com';
  if (/\.(invalid|test)$/.test(name)) return { up: false, latency_ms: 4, detail: `${name} does not resolve` };
  return { up: true, latency_ms: 9, detail: `${name} is 93.184.215.14` };
}

// demoCheckResult is a plugin check's result in the demo.
export function demoPluginCheck(target: string): CheckResult {
  const [id, ...rest] = target.split(':');
  const p = plugins.find((x) => x.id === id);
  if (!p) return { up: false, latency_ms: null, detail: `no plugin "${id}" is installed` };
  if (!p.enabled) return { up: false, latency_ms: null, detail: `the ${id} plugin is turned off` };
  return checkDNS(rest.join(':'));
}

type Reply = (body: unknown, status?: number) => Response;

// pluginRoute answers the plugin API, or is undefined for any other path.
export function pluginRoute(method: string, path: string, body: unknown, json: Reply, error: (status: number, message: string) => Response): Response | undefined {
  if (!path.startsWith('/api/plugins')) return undefined;
  if (method === 'GET' && path === '/api/plugins') return json({ dir: DIR, plugins });
  if (method === 'POST' && path === '/api/plugins/rescan') return json({ dir: DIR, plugins });
  if (method === 'GET' && path === '/api/plugins/cards') return json({ cards: plugins.some((p) => p.id === 'load-card' && p.enabled) ? [loadCard()] : [] });
  const m = path.match(/^\/api\/plugins\/([a-z0-9-]+)(\/try)?$/);
  const p = m && plugins.find((x) => x.id === m[1]);
  if (!m || !p) return error(404, 'no plugin by that name');
  if (method === 'PUT' && !m[2]) {
    const c = body as PluginChange;
    if (c.enabled && p.problem) return error(400, `${p.name} cannot run: ${p.problem}`);
    for (const [k, v] of Object.entries(c.settings ?? {})) {
      const s = p.settings?.find((x) => x.key === k);
      if (!s) return error(400, `${p.name} has no setting ${k}`);
      if (s.type === 'secret') {
        if (v) p.secrets_set = [...new Set([...p.secrets_set, k])];
      } else p.values[k] = v.trim();
    }
    p.secrets_set = p.secrets_set.filter((k) => !c.clear?.includes(k));
    if (c.enabled !== undefined) p.enabled = c.enabled;
    return json({ plugin: p });
  }
  if (method === 'POST' && m[2]) {
    const { mode, argument } = body as { mode: string; argument?: string };
    const at = now();
    if (mode === 'check') {
      const check = checkDNS(argument ?? '');
      p.last = { mode, at, ok: true, ms: check.latency_ms ?? 0, output: check.detail };
      return json({ check });
    }
    if (mode === 'card') {
      p.last = { mode, at, ok: true, ms: 18, output: '5 items' };
      return json({ card: loadCard() });
    }
    return error(400, 'The mode must be check or card');
  }
  return error(405, 'method not allowed');
}
