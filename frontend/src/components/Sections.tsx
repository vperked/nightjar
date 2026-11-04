import { useState } from 'react';
import { api } from '../api/client';
import { useConfig } from '../api/ConfigContext';
import { SettingsGroup } from './SettingsGroup';
import { useToast } from './Toasts';

// Sections are parts of the dashboard that can be turned off in Settings. Monitoring carries on
// either way: alerts still go out for a hidden section, it only leaves the screen.

export const SECTIONS = [
  { id: 'servers', label: 'Servers', group: 'Pages', hint: 'Other machines, watched through their agents' },
  { id: 'network', label: 'Network', group: 'Pages', hint: 'The live network meter tab' },
  { id: 'media', label: 'Media', group: 'Pages', hint: 'Plex and qBittorrent' },
  { id: 'apps', label: 'Apps', group: 'Pages', hint: 'App store and updates' },
  { id: 'notifications', label: 'Notification bell', group: 'Pages', hint: 'The bell and its unread count' },
  { id: 'attention', label: 'Needs attention', group: 'Overview', hint: 'Failed or restarting system services' },
  { id: 'services', label: 'Uptime checks', group: 'Overview', hint: '' },
  { id: 'remote', label: 'Other servers', group: 'Overview', hint: 'Once a server is added' },
  { id: 'plex', label: 'Now playing', group: 'Overview', hint: 'Plex streams' },
  { id: 'processes', label: 'Processes', group: 'Overview', hint: '' },
  { id: 'host', label: 'Host', group: 'Overview', hint: '' },
  { id: 'cores', label: 'CPU cores', group: 'Overview', hint: '' },
  { id: 'storage', label: 'Storage', group: 'Overview', hint: '' },
  { id: 'drives', label: 'Drive health', group: 'Overview', hint: '' },
  { id: 'interfaces', label: 'Interfaces', group: 'Overview', hint: '' },
] as const;

export type SectionID = (typeof SECTIONS)[number]['id'];

// configChanged asks the app to reload its config, e.g. after sections change.
export const configChanged = () => window.dispatchEvent(new Event('config-changed'));

// useShown reports whether a section is on.
export function useShown(): (id: SectionID) => boolean {
  const hidden = useConfig()?.hidden ?? [];
  return (id) => !hidden.includes(id);
}

function useSetHidden() {
  const notify = useToast();
  return async (hidden: string[], message: string) => {
    try {
      await api.saveSettings({ DASHBOARD_HIDDEN: hidden.join(',') });
      configChanged();
      notify({ tone: 'ok', title: message });
      return true;
    } catch (e) {
      notify({ tone: 'error', title: 'Could not change the sections', body: (e as Error).message });
      return false;
    }
  };
}

// SectionsPanel is Settings' list of switches, saved as soon as one is flipped.
export function SectionsPanel() {
  const config = useConfig();
  const [hidden, setHidden] = useState<string[]>();
  const current = hidden ?? config?.hidden ?? [];
  const save = useSetHidden();
  const toggle = (id: string, label: string, on: boolean) => {
    const next = on ? current.filter((x) => x !== id) : [...current, id];
    setHidden(next);
    // A save that fails puts the switches back to what the monitor has.
    save(next, `${label} ${on ? 'shown' : 'hidden'}`).then((ok) => !ok && setHidden(undefined));
  };
  return (
    <SettingsGroup id="sections" name="Sections" aside={<span className="small ink-3">{current.length ? `${current.length} hidden` : 'everything shown'}</span>}>
      {(['Pages', 'Overview'] as const).map((group) => (
        <div key={group} className="sections-group">
          <span className="sections-group-label">{group === 'Pages' ? 'Pages and header' : 'Overview cards'}</span>
          <div className="sections-grid">
            {SECTIONS.filter((s) => s.group === group).map((s) => {
              const on = !current.includes(s.id);
              return (
                <label key={s.id} className={`check section-toggle ${on ? 'on' : ''}`}>
                  <input type="checkbox" role="switch" checked={on} onChange={(e) => toggle(s.id, s.label, e.target.checked)} />
                  <span className="min0">
                    <strong>{s.label}</strong>
                    {s.hint && <span>{s.hint}</span>}
                  </span>
                </label>
              );
            })}
          </div>
        </div>
      ))}
    </SettingsGroup>
  );
}

// SectionOff stands in for a page whose section is turned off.
export function SectionOff({ id }: { id: SectionID }) {
  const config = useConfig();
  const save = useSetHidden();
  const s = SECTIONS.find((x) => x.id === id)!;
  return (
    <main>
      <div className="section-off">
        <strong>{s.label} is turned off</strong>
        <p>It was hidden under Settings → Sections. Monitoring for it carries on.</p>
        <button
          className="btn"
          onClick={() =>
            save(
              (config?.hidden ?? []).filter((x) => x !== id),
              `${s.label} shown`,
            )
          }
        >
          Turn it back on
        </button>
      </div>
    </main>
  );
}
