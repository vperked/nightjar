import { useEffect, useState } from 'react';
import { api } from '../api/client';
import { useConfig } from '../api/ConfigContext';
import type { Appearance } from '../api/types';
import { DEFAULT_APPEARANCE, previewAppearance } from '../appearance';
import { useUnsaved } from '../freshness';
import { configChanged } from './Sections';
import { SettingsGroup } from './SettingsGroup';
import { useToast } from './Toasts';
import { Seg } from './ui';

// Appearance is Settings' choice of look: a style, how charts and meters are drawn, the density,
// and the owner's own CSS. A change shows on the whole dashboard at once, as a preview, and stays
// only once saved; leaving Settings unsaved puts the saved look back.

const MAX_CSS = 50000;

const STYLES: { value: Appearance['style']; name: string; hint: string }[] = [
  { value: 'matrix', name: 'Ruled', hint: 'Soft flat panels, mono readings' },
  { value: 'cards', name: 'Cards', hint: 'Rounded panels, sans type' },
];

// What the CSS box can lean on: the colours and sizes every page draws with, and its main parts.
const HOOKS: [string, string][] = [
  ['--bg, --surface, --surface-2', 'The page, and panels or menus over it'],
  ['--ink, --ink-2, --ink-3', 'Text, from strongest to quietest'],
  ['--rule, --rule-strong', 'Hairlines and panel rules'],
  ['--good, --warn, --crit', 'Up, warning and down'],
  ['--series-1, --series-2', 'Chart inks'],
  ['--dot-off, --dot-sky', 'Unlit dots, and the dots behind the page'],
  ['--radius, --radius-sm', 'Corner rounding'],
  ['--sans, --mono', 'The two typefaces'],
  ['.masthead, .nav', 'The header and its tabs'],
  ['.readout, .readout-value', "The overview's four readings"],
  ['.section, .section-head', 'Every panel and its title'],
  ['.table, .state, .health-chip', 'Tables, status marks and the overview chips'],
  [':root[data-style="cards"]', 'Rules for one style only; also data-charts, data-density and data-theme'],
];

const same = (a: Appearance, b: Appearance) => a.style === b.style && a.charts === b.charts && a.density === b.density && a.css.trim() === b.css.trim();

export function AppearancePanel() {
  const saved = useConfig()?.appearance ?? DEFAULT_APPEARANCE;
  const notify = useToast();
  const [draft, setDraft] = useState<Appearance>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const current = draft ?? saved;
  const dirty = draft !== undefined && !same(draft, saved);
  // The draft is outside a form, edited with buttons: say so, so a new build does not reload it away.
  useUnsaved(dirty);

  // The draft is shown everywhere while it differs from the saved look; once the saved look
  // catches up (after a save) or the draft is dropped, the saved one shows again.
  useEffect(() => previewAppearance(dirty ? current : null), [dirty, current]);
  useEffect(() => () => previewAppearance(null), []);

  const set = (patch: Partial<Appearance>) => {
    setError('');
    setDraft({ ...current, ...patch });
  };
  const over = current.css.length > MAX_CSS;
  const save = async () => {
    setSaving(true);
    try {
      await api.saveAppearance(current);
      configChanged();
      notify({ tone: 'ok', title: 'Appearance saved' });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setSaving(false);
    }
  };
  const style = STYLES.find((s) => s.value === current.style)!;
  return (
    <SettingsGroup id="appearance" name="Appearance" aside={<span className="small ink-3">{dirty ? 'previewing' : `${style.name}${current.css.trim() ? ' with your CSS' : ''}`}</span>}>
      <div className="look-row">
        <span className="look-label">Style</span>
        <div className="look-styles" role="radiogroup" aria-label="Style">
          {STYLES.map((s) => (
            <button
              key={s.value}
              type="button"
              role="radio"
              aria-checked={current.style === s.value}
              className={`look-style ${current.style === s.value ? 'on' : ''}`}
              onClick={() => set({ style: s.value })}
            >
              <span className={`look-thumb ${s.value}`} aria-hidden="true">
                <span />
                <span />
                <span />
              </span>
              <strong>{s.name}</strong>
              <span>{s.hint}</span>
            </button>
          ))}
        </div>
      </div>
      <div className="look-row">
        <span className="look-label">Charts and meters</span>
        <Seg<Appearance['charts']>
          label="Charts and meters"
          value={current.charts}
          onChange={(charts) => set({ charts })}
          options={[
            { value: '', label: 'Lines' },
            { value: 'dots', label: 'Dots' },
          ]}
        />
      </div>
      <div className="look-row">
        <span className="look-label">Density</span>
        <Seg<Appearance['density']>
          label="Density"
          value={current.density}
          onChange={(density) => set({ density })}
          options={[
            { value: 'comfortable', label: 'Comfortable' },
            { value: 'compact', label: 'Compact' },
          ]}
        />
      </div>
      <div className="look-css">
        <label className="look-label" htmlFor="look-css">
          Your own CSS
          <span className="form-help">
            Applies over the style on every page of this dashboard. The public status page has its own, in its designer. Images and fonts from other sites are blocked here.
          </span>
        </label>
        <textarea
          id="look-css"
          className={`field mono sd-code ${over ? 'over' : ''}`}
          rows={9}
          spellCheck={false}
          autoCapitalize="off"
          autoCorrect="off"
          value={current.css}
          placeholder={':root {\n  --radius: 0;\n}\n.readout-value {\n  font-weight: 300;\n}'}
          onChange={(e) => set({ css: e.target.value })}
        />
        <div className="sd-code-foot">
          <details className="sd-classes">
            <summary>Variables and classes you can use</summary>
            <dl>
              {HOOKS.map(([c, what]) => (
                <div key={c}>
                  <dt className="mono">{c}</dt>
                  <dd>{what}</dd>
                </div>
              ))}
            </dl>
          </details>
          <span className={`small num ${over ? 'sd-over' : 'ink-3'}`}>
            {current.css.length.toLocaleString()} / {MAX_CSS.toLocaleString()}
          </span>
        </div>
      </div>
      <div className="look-actions">
        {error && <span className="look-error">{error}</span>}
        <button type="button" className="btn-quiet" onClick={() => setDraft(undefined)} disabled={!dirty || saving}>
          Discard
        </button>
        <button type="button" className="btn" onClick={save} disabled={!dirty || saving || over}>
          {saving ? 'Saving…' : 'Save appearance'}
        </button>
      </div>
    </SettingsGroup>
  );
}
