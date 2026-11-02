import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { ApiError, api } from '../api/client';
import { useServices } from '../api/ServicesContext';
import type { PublicService, PublicStatus, StatusDesign, StatusDesignResponse } from '../api/types';
import { Select } from '../components/Select';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { StatusView } from './StatusPage';

// The status page designer: everything on the public page, edited on the left and previewed live
// on the right with the checks' real state. Only checks added here are ever shown publicly.

type Group = StatusDesign['groups'][number];

const move = <T,>(list: T[], from: number, to: number): T[] => {
  if (to < 0 || to >= list.length) return list;
  const out = [...list];
  const [it] = out.splice(from, 1);
  out.splice(to, 0, it);
  return out;
};

function Field({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <label className="sd-field">
      <span className="sd-label">
        {label}
        {hint && <span className="form-help">{hint}</span>}
      </span>
      {children}
    </label>
  );
}

const Arrow = ({ up }: { up?: boolean }) => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true">
    <path d={up ? 'm6 15 6-6 6 6' : 'm6 9 6 6 6-6'} />
  </svg>
);
const Cross = () => (
  <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" aria-hidden="true">
    <path d="M6 6l12 12M18 6 6 18" />
  </svg>
);

function GroupEditor({
  g,
  index,
  count,
  available,
  onChange,
  onMove,
  onRemove,
}: {
  g: Group;
  index: number;
  count: number;
  available: string[];
  onChange: (g: Group) => void;
  onMove: (to: number) => void;
  onRemove: () => void;
}) {
  const items = g.items;
  return (
    <div className="sd-group">
      <div className="sd-group-head">
        <input className="field" aria-label={`Group ${index + 1} heading`} placeholder="Heading (optional)" value={g.name} onChange={(e) => onChange({ ...g, name: e.target.value })} />
        <button type="button" className="icon-btn" aria-label="Move group up" disabled={index === 0} onClick={() => onMove(index - 1)}>
          <Arrow up />
        </button>
        <button type="button" className="icon-btn" aria-label="Move group down" disabled={index === count - 1} onClick={() => onMove(index + 1)}>
          <Arrow />
        </button>
        <button type="button" className="icon-btn" aria-label="Remove group" onClick={onRemove}>
          <Cross />
        </button>
      </div>
      {items.length === 0 && <p className="form-help sd-empty">No checks in this group yet.</p>}
      <ul className="sd-items">
        {items.map((it, i) => (
          <li key={it.check}>
            <span className="sd-check mono" title={`check: ${it.check}`}>
              {it.check}
            </span>
            <input
              className="field"
              aria-label={`Public name for ${it.check}`}
              placeholder={`shown as “${it.check}”`}
              value={it.label ?? ''}
              onChange={(e) => onChange({ ...g, items: items.map((x, j) => (j === i ? { ...x, label: e.target.value } : x)) })}
            />
            <button type="button" className="icon-btn" aria-label={`Move ${it.check} up`} disabled={i === 0} onClick={() => onChange({ ...g, items: move(items, i, i - 1) })}>
              <Arrow up />
            </button>
            <button type="button" className="icon-btn" aria-label={`Move ${it.check} down`} disabled={i === items.length - 1} onClick={() => onChange({ ...g, items: move(items, i, i + 1) })}>
              <Arrow />
            </button>
            <button type="button" className="icon-btn" aria-label={`Take ${it.check} off the page`} onClick={() => onChange({ ...g, items: items.filter((_, j) => j !== i) })}>
              <Cross />
            </button>
          </li>
        ))}
      </ul>
      {available.length > 0 && (
        <div className="sd-add">
          <Select
            label={`Add a check to group ${index + 1}`}
            value=""
            onChange={(check) => check && onChange({ ...g, items: [...items, { check }] })}
            options={[{ value: '', label: '+ add a check', text: '+' }, ...available.map((c) => ({ value: c, label: c }))]}
          />
          {available.length > 1 && (
            <button type="button" className="btn-quiet" onClick={() => onChange({ ...g, items: [...items, ...available.map((check) => ({ check }))] })}>
              add the other {available.length}
            </button>
          )}
        </div>
      )}
    </div>
  );
}

export function StatusDesignerPage() {
  const [loaded, setLoaded] = useState<StatusDesignResponse>();
  const [design, setDesign] = useState<StatusDesign>();
  const [enabled, setEnabled] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const notify = useToast();
  const { data: live } = useServices();

  useEffect(() => {
    api.statusDesign().then(
      (r) => {
        setLoaded(r);
        setDesign(r.design);
        setEnabled(r.enabled);
      },
      (e: Error) => setError(e.message),
    );
  }, []);

  const dirty = !!loaded && JSON.stringify(design) !== JSON.stringify(loaded.design);
  const used = useMemo(() => new Set(design?.groups.flatMap((g) => g.items.map((i) => i.check)) ?? []), [design]);
  const available = (loaded?.checks ?? []).filter((c) => !used.has(c));

  // The preview is the public page as it would be, from the checks' real state.
  const preview = useMemo<PublicStatus | undefined>(() => {
    if (!design) return undefined;
    const byName = new Map((live?.services ?? []).map((s) => [s.name, s]));
    const groups = design.groups.map((g) => ({
      name: g.name,
      services: g.items.flatMap<PublicService>((it) => {
        const s = byName.get(it.check);
        if (!s) return [];
        return [
          {
            name: it.label || it.check,
            status: s.status,
            uptime_24h: design.show_uptime ? s.uptime_24h : null,
            uptime_30d: design.show_uptime ? s.uptime_30d : null,
            recent: design.show_history ? s.recent : [],
          },
        ];
      }),
    }));
    const all = groups.flatMap((g) => g.services);
    const watched = all.filter((s) => s.status !== 'paused');
    const down = watched.filter((s) => s.status === 'down').length;
    return {
      title: design.title || 'Service Status',
      description: design.description,
      footer: design.footer,
      show_uptime: design.show_uptime,
      show_history: design.show_history,
      announcement: design.announcement ? { text: design.announcement, tone: design.tone } : undefined,
      overall: down === 0 ? 'operational' : down === watched.length ? 'outage' : 'degraded',
      updated: Date.now() / 1000,
      groups,
      services: all,
    };
  }, [design, live]);

  if (error) return <main className="alert">{error}</main>;
  if (!design || !loaded) return <Skeleton kind="form" />;

  const set = <K extends keyof StatusDesign>(k: K, v: StatusDesign[K]) => setDesign({ ...design, [k]: v });
  const setGroup = (i: number, g: Group) =>
    set(
      'groups',
      design.groups.map((x, j) => (j === i ? g : x)),
    );

  const toggleEnabled = async (on: boolean) => {
    try {
      await api.saveSettings({ STATUS_PAGE_ENABLED: String(on) });
      setEnabled(on);
      notify({ tone: 'ok', title: on ? 'Status page is public' : 'Status page is off', body: on ? 'Anyone with the address can see it.' : undefined });
    } catch (err) {
      notify({ tone: 'error', title: 'Could not change it', body: (err as Error).message });
    }
  };
  const save = async () => {
    setSaving(true);
    try {
      const { design: saved } = await api.saveStatusDesign(design);
      setLoaded({ ...loaded, design: saved, designed: true });
      setDesign(saved);
      notify({ tone: 'ok', title: 'Status page saved', body: enabled ? 'It is live now.' : 'Turn it on to make it public.' });
    } catch (err) {
      notify({ tone: 'error', title: 'Could not save the status page', body: err instanceof ApiError ? err.message : String(err) });
    } finally {
      setSaving(false);
    }
  };

  return (
    <main className={dirty ? 'has-savebar' : ''}>
      <div className="page-title">
        <div>
          <div className="crumb">
            <Link to="/settings">settings</Link> /
          </div>
          <h1>Status page</h1>
          <div className="page-sub">A public page with the checks you choose, at /status. Targets and error details are never shown.</div>
        </div>
        <div className="toolbar">
          <label className="check">
            <input type="checkbox" checked={enabled} onChange={(e) => toggleEnabled(e.target.checked)} /> {enabled ? 'public' : 'off'}
          </label>
          {enabled && (
            <a className="btn-quiet" href="/status" target="_blank" rel="noreferrer">
              open ↗
            </a>
          )}
        </div>
      </div>

      <div className="sd-layout">
        <div className="sd-editor">
          <section className="panel">
            <div className="section-head">
              <span className="label">Page</span>
            </div>
            <Field label="Title">
              <input className="field" value={design.title} placeholder="Service Status" onChange={(e) => set('title', e.target.value)} />
            </Field>
            <Field label="Intro" hint="A sentence or two under the title (optional).">
              <textarea className="field" rows={2} value={design.description} onChange={(e) => set('description', e.target.value)} />
            </Field>
            <Field label="Footer" hint="Shown at the bottom (optional).">
              <input className="field" value={design.footer} placeholder="server monitor" onChange={(e) => set('footer', e.target.value)} />
            </Field>
            <div className="sd-toggles">
              <label className="check">
                <input type="checkbox" checked={design.show_uptime} onChange={(e) => set('show_uptime', e.target.checked)} /> uptime percentages
              </label>
              <label className="check">
                <input type="checkbox" checked={design.show_history} onChange={(e) => set('show_history', e.target.checked)} /> recent checks
              </label>
            </div>
          </section>

          <section className="panel">
            <div className="section-head">
              <span className="label">Announcement</span>
            </div>
            <Field label="Message" hint="A banner at the top: maintenance, an incident, anything. Empty hides it.">
              <textarea className="field" rows={2} value={design.announcement} placeholder="Plex is down for maintenance until 3 am." onChange={(e) => set('announcement', e.target.value)} />
            </Field>
            <Field label="Style">
              <Select
                className="short"
                label="Announcement style"
                value={design.tone}
                onChange={(v) => set('tone', v as StatusDesign['tone'])}
                options={[
                  { value: 'info', label: 'Information' },
                  { value: 'warning', label: 'Warning' },
                  { value: 'crit', label: 'Incident' },
                ]}
              />
            </Field>
          </section>

          <section className="panel">
            <div className="section-head">
              <span className="label">Checks on the page</span>
              <span className="small ink-3">{available.length ? `${available.length} not shown` : 'all shown'}</span>
            </div>
            {design.groups.map((g, i) => (
              <GroupEditor
                key={i}
                g={g}
                index={i}
                count={design.groups.length}
                available={available}
                onChange={(next) => setGroup(i, next)}
                onMove={(to) => set('groups', move(design.groups, i, to))}
                onRemove={() =>
                  set(
                    'groups',
                    design.groups.filter((_, j) => j !== i),
                  )
                }
              />
            ))}
            <button type="button" className="btn-quiet" onClick={() => set('groups', [...design.groups, { name: '', items: [] }])}>
              + add a group
            </button>
          </section>
        </div>

        <div className="sd-preview" aria-label="Preview">
          <div className="sd-preview-label">
            <span className="label">Preview</span>
            <span className="small ink-3">live, with your checks' real state</span>
          </div>
          <div className="sd-frame">{preview && <StatusView data={preview} />}</div>
        </div>
      </div>

      {dirty && (
        <div className="savebar" role="region" aria-label="Unsaved changes">
          <span>Unsaved changes to the status page</span>
          <span className="savebar-actions">
            <button className="btn-quiet" onClick={() => setDesign(loaded.design)}>
              discard
            </button>
            <button className="btn" onClick={save} disabled={saving}>
              {saving ? 'saving…' : 'Save'}
            </button>
          </span>
        </div>
      )}
    </main>
  );
}
