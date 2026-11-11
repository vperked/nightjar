import { useEffect } from 'react';
import { useCanEdit } from '../../api/ConfigContext';
import type { Drive, DrivesResponse } from '../../api/types';
import { MuteButton } from '../../components/MuteButton';
import { Section } from '../../components/ui';
import { formatAgo, formatBytes } from '../../format';

// The overview's drive health card: each disk's SMART verdict, temperature, age and wear.

const HEALTH: Record<Drive['health'], [string, string]> = { good: ['good', 'Healthy'], warning: ['warn', 'Warning'], failing: ['crit', 'Failing'], unknown: ['idle', 'Unknown'] };

function hours(h: number): string {
  return h >= 24 * 365 ? `${(h / (24 * 365)).toFixed(1)} years` : h >= 48 ? `${Math.round(h / 24)} days` : `${h} h`;
}

// DriveRow is one disk's verdict, temperature, age and whatever counters say it is wearing out.
// Another server's drives have no mute of their own: its alerts are muted per server.
export function DriveRow({ d, now, mutable = true }: { d: Drive; now: number; mutable?: boolean }) {
  const canEdit = useCanEdit();
  const [tone, word] = d.asleep && d.health === 'unknown' ? ['idle', 'Asleep'] : HEALTH[d.health];
  const facts = [
    d.capacity ? formatBytes(d.capacity) : '',
    d.temperature !== undefined ? `${d.temperature}°C` : '',
    d.power_on_hours !== undefined ? `on for ${hours(d.power_on_hours)}` : '',
    d.percent_used !== undefined ? `${d.percent_used}% worn` : '',
  ].filter(Boolean);
  return (
    <div className="drive">
      <div className="drive-line">
        <span className="min0">
          <span className="drive-name truncate" title={d.model}>
            {d.model || d.device}
          </span>
          {d.model && <span className="sub mono">{d.device}</span>}
        </span>
        <span className="drive-state">
          {mutable && canEdit && <MuteButton subject={`drive:${d.serial || d.device}`} label={d.model || d.device} compact />}
          <span className={`state ${tone}`}>{word}</span>
        </span>
      </div>
      <div className="sub num">
        {[...facts, d.asleep ? (d.checked ? `asleep, read ${formatAgo(d.checked, now)}` : 'asleep, not read yet: the monitor does not spin disks up') : ''].filter(Boolean).join(' · ')}
      </div>
      {d.problems && d.problems.length > 0 && (
        <ul className={`drive-problems ${d.health === 'failing' ? 'failing' : ''}`}>
          {d.problems.map((p) => (
            <li key={p}>{p}</li>
          ))}
        </ul>
      )}
      {d.error && <div className="sub form-error">{d.error}</div>}
    </div>
  );
}

export function DrivesSection({ data }: { data: DrivesResponse | undefined }) {
  const now = Date.now() / 1000;
  // A drive alert's link (/#drive-health) lands here once the drives have loaded.
  const ready = !!data?.enabled;
  useEffect(() => {
    if (ready && window.location.hash === '#drive-health') document.getElementById('drive-health')?.scrollIntoView?.({ block: 'center' });
  }, [ready]);
  if (!data || !data.enabled) return null;
  return (
    <Section
      label="Drive health"
      aside={
        <span className="small ink-3" id="drive-health">
          SMART
        </span>
      }
    >
      {data.error ? (
        <p className="note" style={{ padding: 0 }}>
          {data.error}
        </p>
      ) : data.drives.length === 0 ? (
        <p className="note" style={{ padding: 0 }}>
          First reading within a minute or two of starting.
        </p>
      ) : (
        <div className="drives">
          {data.drives.map((d) => (
            <DriveRow key={d.device} d={d} now={now} />
          ))}
        </div>
      )}
    </Section>
  );
}
