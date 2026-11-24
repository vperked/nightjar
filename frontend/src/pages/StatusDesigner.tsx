import { useEffect, useMemo, useState, type ReactNode } from 'react';
import { Link } from 'react-router';
import { api, errorText } from '../api/client';
import { useServices } from '../api/ServicesContext';
import type { PublicService, PublicStatus, StatusDesign, StatusDesignResponse, StatusProxyResponse, StatusProxyStep } from '../api/types';
import { useConfig } from '../api/ConfigContext';
import { Select } from '../components/Select';
import { Skeleton } from '../components/Skeleton';
import { useToast } from '../components/Toasts';
import { Seg } from '../components/ui';
import { useUnsaved } from '../freshness';
import { StatusView, uptimeOf } from './StatusPage';

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

type CodeKey = 'css' | 'header_html' | 'footer_html';

const CODE: Record<CodeKey, { tab: string; limit: number; hint: string; placeholder: string }> = {
  css: {
    tab: 'CSS',
    limit: 50000,
    hint: 'Styles the public page. Rules for body and :root work too. The preview keeps them to itself, so the dashboard never changes.',
    placeholder: '.status-page {\n  max-width: 820px;\n}\n.status-hero.operational {\n  background: #0f2a1d;\n}',
  },
  header_html: {
    tab: 'Top HTML',
    limit: 20000,
    hint: 'Shown above the title: a logo, links, a notice. Scripts, frames and forms are taken out; links and images work.',
    placeholder: '<a href="https://example.com">\n  <img src="https://example.com/logo.png" alt="Example" height="32">\n</a>',
  },
  footer_html: {
    tab: 'Bottom HTML',
    limit: 20000,
    hint: 'Shown above the footer: contact details, links to your other pages. Scripts, frames and forms are taken out.',
    placeholder: '<p>Something wrong? <a href="mailto:you@example.com">Email us</a>.</p>',
  },
};

// The page's parts, for the owner's CSS.
const CLASSES: [string, string][] = [
  ['.status-page', 'the whole page'],
  ['.status-custom-top', 'your top HTML'],
  ['.status-top', 'title and intro'],
  ['.status-announcement', 'the banner (.info, .warning, .crit)'],
  ['.status-hero', 'the verdict (.operational, .degraded, .outage)'],
  ['.status-notice', 'an ongoing incident or maintenance (.incident, .maint)'],
  ['.status-group', 'a group of checks'],
  ['.status-row', 'one check'],
  ['.status-state', 'its state (.up, .down, .paused)'],
  ['.day', 'a day bar (.good, .minor, .major, .none)'],
  ['.status-legend', 'what the bars mean'],
  ['.status-incident', 'a resolved incident'],
  ['.status-custom-bottom', 'your bottom HTML'],
  ['.status-footer', 'the footer'],
];

// CodePanel is the owner's own CSS and HTML, one at a time.
// AddressPanel is where the page is: a path on the dashboard's own address, and a domain of its own
// where it is the whole site. For the domain, it can set up nginx and HTTPS, or show the server
// block to set up by hand.
function AddressPanel() {
  const config = useConfig();
  const notify = useToast();
  const savedDomain = config?.status_page_domain ?? '';
  const savedPath = config?.status_page_path || '/status';
  const [domain, setDomain] = useState(savedDomain);
  const [path, setPath] = useState(savedPath);
  const [saving, setSaving] = useState(false);
  const [proxy, setProxy] = useState<StatusProxyResponse>();
  const [email, setEmail] = useState('');
  const [busy, setBusy] = useState<'' | 'install' | 'remove'>('');
  const [steps, setSteps] = useState<StatusProxyStep[]>();
  // The fields follow the saved values when the config arrives or changes elsewhere.
  useEffect(() => setDomain(savedDomain), [savedDomain]);
  useEffect(() => setPath(savedPath), [savedPath]);
  useEffect(() => {
    if (!savedDomain) {
      setProxy(undefined);
      return;
    }
    api.statusProxy().then(setProxy, () => setProxy(undefined));
  }, [savedDomain]);

  const changed = domain.trim().toLowerCase() !== savedDomain || (path.trim() || '/status') !== savedPath;
  const save = async () => {
    setSaving(true);
    try {
      await api.saveSettings({ STATUS_PAGE_DOMAIN: domain.trim(), STATUS_PAGE_PATH: path.trim() || '/status' });
      window.dispatchEvent(new Event('config-changed'));
      setSteps(undefined);
      notify({ tone: 'ok', title: 'Address saved' });
    } catch (err) {
      notify({ tone: 'error', title: 'Could not save the address', body: errorText(err) });
    } finally {
      setSaving(false);
    }
  };
  const run = async (what: 'install' | 'remove') => {
    setBusy(what);
    setSteps(undefined);
    try {
      const r = what === 'install' ? await api.setUpStatusProxy(email.trim()) : await api.removeStatusProxy();
      setSteps(r.steps);
      setProxy({ domain: savedDomain, state: r.state });
      const https = r.steps.find((st) => st.name.startsWith('Get an HTTPS'));
      notify(
        !r.ok
          ? { tone: 'error', title: what === 'install' ? 'nginx was not set up' : 'Could not take it out', body: r.steps.at(-1)?.detail }
          : what === 'remove'
            ? { tone: 'ok', title: `${savedDomain} is out of nginx` }
            : https && !https.ok
              ? { tone: 'warning', title: `${savedDomain} works over HTTP`, body: 'HTTPS could not be set up yet; see the steps.' }
              : { tone: 'ok', title: `${savedDomain} is live`, body: 'nginx serves it, with HTTPS.' },
      );
    } catch (err) {
      notify({ tone: 'error', title: 'Could not reach Nightjar', body: errorText(err) });
    } finally {
      setBusy('');
    }
  };
  const copy = async (text: string) => {
    try {
      await navigator.clipboard.writeText(text);
      notify({ tone: 'ok', title: 'Copied' });
    } catch {
      notify({ tone: 'error', title: 'Could not copy', body: 'Select the text and copy it instead.' });
    }
  };
  const st = proxy?.state;
  const origin = typeof window === 'undefined' ? '' : window.location.origin;

  return (
    <section className="panel sd-address" aria-label="Address">
      <div className="section-head">
        <span className="label">Address</span>
        <span className="small ink-3">where visitors find it</span>
      </div>
      <Field label="Path" hint={`On this dashboard's address: ${origin}${path.trim() || '/status'}. /status always works too.`}>
        <input className="field mono" value={path} placeholder="/status" onChange={(e) => setPath(e.target.value)} aria-label="Status page path" />
      </Field>
      <Field label="Its own domain" hint="Optional. There the status page is the whole site, and the dashboard can't be reached. Point the domain's DNS at this machine first.">
        <input className="field mono" value={domain} placeholder="status.example.com" onChange={(e) => setDomain(e.target.value)} aria-label="Status page domain" />
      </Field>
      {changed && (
        <div className="sd-address-save">
          <button type="button" className="btn" onClick={save} disabled={saving}>
            {saving ? 'Saving…' : 'Save address'}
          </button>
          <button type="button" className="btn-quiet" onClick={() => (setDomain(savedDomain), setPath(savedPath))}>
            Undo
          </button>
        </div>
      )}

      {savedDomain && !changed && st && (
        <div className="sd-proxy">
          {st.installed ? (
            <div className="sd-proxy-state">
              <span className={`state ${st.https ? 'up' : 'maintenance'}`}>{st.https ? 'HTTPS' : 'HTTP only'}</span>
              <span>
                nginx serves{' '}
                <a href={`${st.https ? 'https' : 'http'}://${savedDomain}/`} target="_blank" rel="noreferrer">
                  {savedDomain} ↗
                </a>
                {st.https ? ', and certbot renews its certificate.' : '. Set it up again to get a certificate.'}
              </span>
            </div>
          ) : (
            <p className="note sd-proxy-note">
              {st.nginx
                ? `nginx isn't serving ${savedDomain} yet. Nightjar can add the site, check nginx takes it, reload nginx and get an HTTPS certificate from Let's Encrypt.`
                : `nginx isn't installed. Put the server block below in your reverse proxy, or install nginx and set it up from here.`}
            </p>
          )}
          {st.nginx && (
            <div className="sd-proxy-actions">
              {(!st.installed || !st.https) && (
                <>
                  <input
                    className="field"
                    type="email"
                    value={email}
                    placeholder="Email for certificate notices (optional)"
                    onChange={(e) => setEmail(e.target.value)}
                    aria-label="Email for certificate notices"
                  />
                  <button type="button" className="btn" onClick={() => run('install')} disabled={!!busy}>
                    {busy === 'install' ? 'Setting up…' : st.installed ? 'Set up HTTPS' : 'Set up nginx and HTTPS'}
                  </button>
                </>
              )}
              {st.installed && (
                <button type="button" className="btn-quiet danger" onClick={() => run('remove')} disabled={!!busy}>
                  {busy === 'remove' ? 'Taking it out…' : 'Take it out of nginx'}
                </button>
              )}
            </div>
          )}
          {steps && (
            <ol className="sd-steps" aria-label="What was done">
              {steps.map((x, i) => (
                <li key={i} className={x.ok ? 'ok' : 'failed'}>
                  <strong>{x.name}</strong>
                  {x.detail && <span className="mono">{x.detail}</span>}
                </li>
              ))}
            </ol>
          )}
          <details className="sd-proxy-manual">
            <summary>Set it up yourself</summary>
            <p className="note">
              This nginx server block forwards the domain to Nightjar and passes the host on, which is how Nightjar knows to show only the status page. Any reverse proxy works the same way.
            </p>
            <pre className="mono">{st.site}</pre>
            <button type="button" className="btn-quiet" onClick={() => copy(st.site)}>
              Copy
            </button>
          </details>
        </div>
      )}
    </section>
  );
}

function CodePanel({ design, set }: { design: StatusDesign; set: (k: CodeKey, v: string) => void }) {
  const [tab, setTab] = useState<CodeKey>('css');
  const spec = CODE[tab];
  const value = design[tab] ?? '';
  const over = value.length > spec.limit;
  return (
    <section className="panel">
      <div className="section-head">
        <span className="label">Your own code</span>
        <span className="small ink-3">{(['css', 'header_html', 'footer_html'] as CodeKey[]).filter((k) => design[k]?.trim()).length || 'none'} in use</span>
      </div>
      <Seg<CodeKey>
        label="Code to edit"
        value={tab}
        onChange={setTab}
        options={(Object.keys(CODE) as CodeKey[]).map((k) => ({
          value: k,
          label: (
            <>
              {CODE[k].tab}
              {design[k]?.trim() ? <span className="sd-code-dot" aria-label="(in use)" /> : null}
            </>
          ),
        }))}
      />
      <p className="form-help sd-code-hint">{spec.hint}</p>
      <textarea
        className={`field mono sd-code ${over ? 'over' : ''}`}
        aria-label={spec.tab}
        rows={11}
        spellCheck={false}
        autoCapitalize="off"
        autoCorrect="off"
        value={value}
        placeholder={spec.placeholder}
        onChange={(e) => set(tab, e.target.value)}
      />
      <div className="sd-code-foot">
        {tab === 'css' ? (
          <details className="sd-classes">
            <summary>Classes you can style</summary>
            <dl>
              {CLASSES.map(([c, what]) => (
                <div key={c}>
                  <dt className="mono">{c}</dt>
                  <dd>{what}</dd>
                </div>
              ))}
            </dl>
          </details>
        ) : (
          <span />
        )}
        <span className={`small num ${over ? 'sd-over' : 'ink-3'}`}>
          {value.length.toLocaleString()} / {spec.limit.toLocaleString()}
        </span>
      </div>
    </section>
  );
}

export function StatusDesignerPage() {
  const [loaded, setLoaded] = useState<StatusDesignResponse>();
  const [design, setDesign] = useState<StatusDesign>();
  const [enabled, setEnabled] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const notify = useToast();
  const config = useConfig();
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

  // Your own code left empty is the same as none, so typing and deleting it is no change.
  const same = (a?: StatusDesign, b?: StatusDesign) => {
    const plain = (d?: StatusDesign) => d && JSON.stringify({ ...d, css: d.css ?? '', header_html: d.header_html ?? '', footer_html: d.footer_html ?? '' });
    return plain(a) === plain(b);
  };
  const dirty = !!loaded && !same(design, loaded.design);
  // The design is edited with buttons and pickers: hold off reloading onto a new build.
  useUnsaved(dirty);
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
            uptime_90d: design.show_uptime ? uptimeOf(loaded?.days?.[it.check] ?? []) : null,
            recent: design.show_history ? s.recent : [],
            days: design.show_history ? (loaded?.days?.[it.check] ?? []) : [],
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
      css: design.css,
      header_html: design.header_html,
      footer_html: design.footer_html,
      overall: down === 0 ? 'operational' : down === watched.length ? 'outage' : 'degraded',
      updated: Date.now() / 1000,
      groups,
      services: all,
    };
  }, [design, live, loaded]);

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
      notify({ tone: 'error', title: 'Could not change it', body: errorText(err) });
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
      notify({ tone: 'error', title: 'Could not save the status page', body: errorText(err) });
    } finally {
      setSaving(false);
    }
  };

  return (
    <main className={dirty ? 'has-savebar' : ''}>
      <div className="page-title">
        <div>
          <div className="crumb">
            <Link to="/settings">Settings</Link> /
          </div>
          <h1>Status page</h1>
          <div className="page-sub">
            A public page with the checks you choose, at {config?.status_page_path || '/status'}
            {config?.status_page_domain ? ` and on ${config.status_page_domain}` : ''}. Targets and error details are never shown.
          </div>
        </div>
        <div className="toolbar">
          <label className="check">
            <input type="checkbox" checked={enabled} onChange={(e) => toggleEnabled(e.target.checked)} /> {enabled ? 'Public' : 'Off'}
          </label>
          {enabled && (
            <a className="btn-quiet" href={config?.status_page_path || '/status'} target="_blank" rel="noreferrer">
              Open ↗
            </a>
          )}
        </div>
      </div>

      <div className="sd-layout">
        <div className="sd-editor">
          <AddressPanel />
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
              <input className="field" value={design.footer} placeholder="Nightjar" onChange={(e) => set('footer', e.target.value)} />
            </Field>
            <div className="sd-toggles">
              <label className="check">
                <input type="checkbox" checked={design.show_uptime} onChange={(e) => set('show_uptime', e.target.checked)} /> Uptime percentages
              </label>
              <label className="check">
                <input type="checkbox" checked={design.show_history} onChange={(e) => set('show_history', e.target.checked)} /> 90-day history
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
              + Add a group
            </button>
          </section>

          <CodePanel design={design} set={set} />
        </div>

        <div className="sd-preview" aria-label="Preview">
          <div className="sd-preview-label">
            <span className="label">Preview</span>
            <span className="small ink-3">live, with your checks' real state{design.css?.trim() ? ' and your CSS' : ''}</span>
          </div>
          <div className="sd-frame">{preview && <StatusView data={preview} scope=".sd-frame" />}</div>
        </div>
      </div>

      {dirty && (
        <div className="savebar" role="region" aria-label="Unsaved changes">
          <span>Unsaved changes to the status page</span>
          <span className="savebar-actions">
            <button className="btn-quiet" onClick={() => setDesign(loaded.design)}>
              Discard
            </button>
            <button className="btn" onClick={save} disabled={saving}>
              {saving ? 'Saving…' : 'Save'}
            </button>
          </span>
        </div>
      )}
    </main>
  );
}
