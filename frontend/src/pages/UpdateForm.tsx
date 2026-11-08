import { useState, type FormEvent, type ReactNode } from 'react';
import { ApiError, api } from '../api/client';
import type { UpdateKind, UpdateSpec } from '../api/types';
import { useToast } from '../components/Toasts';
import { Seg } from '../components/ui';

export const KIND_LABEL: Record<UpdateKind, string> = { system: 'OS packages', snap: 'snaps', apt: 'apt package', arr: 'Sonarr-style app', git: 'git checkout', release: 'GitHub release' };

const KIND_HELP: Record<UpdateKind, string> = {
  system: 'Every package from apt, dnf, yum or pacman.',
  snap: 'Every installed snap, refreshed together.',
  apt: 'One Debian package, upgraded on its own.',
  arr: 'Sonarr, Radarr, Prowlarr, Lidarr or Readarr, updated by their built-in updater.',
  git: 'A git checkout pulled from its upstream branch, then optionally built.',
  release: 'A program compared with its project’s latest GitHub release.',
};

const NEW: UpdateSpec = { id: '', name: '', kind: 'release', auto: false, paused: false };

// The fields an update target sends back; status-only fields stay out of saves.
const SPEC_KEYS: (keyof UpdateSpec)[] = [
  'id',
  'name',
  'kind',
  'auto',
  'paused',
  'unit',
  'restart',
  'package',
  'url',
  'api_key',
  'config_file',
  'dir',
  'repo',
  'tag_prefix',
  'version_command',
  'update_command',
  'self',
];

export function specOf(t: UpdateSpec): UpdateSpec {
  const out: Record<string, unknown> = {};
  for (const k of SPEC_KEYS) if (t[k] !== undefined) out[k] = t[k];
  return out as unknown as UpdateSpec;
}

function Row({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="form-row">
      <div className="form-label">
        {label}
        {hint && <div className="form-help">{hint}</div>}
      </div>
      <div className="form-control">{children}</div>
    </div>
  );
}

// UpdateForm adds an update target, or edits `initial`. Commands and paths are read-only unless the
// dashboard has an access token, since they run as root.
export function UpdateForm({
  initial,
  apiKeySet,
  commandsEditable,
  onSaved,
  onCancel,
}: {
  initial?: UpdateSpec;
  apiKeySet?: boolean;
  commandsEditable: boolean;
  onSaved: () => void;
  onCancel: () => void;
}) {
  const [draft, setDraft] = useState<UpdateSpec>(initial ? specOf(initial) : { ...NEW, kind: commandsEditable ? 'release' : 'apt' });
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const notify = useToast();
  const set = <K extends keyof UpdateSpec>(key: K, value: UpdateSpec[K]) => setDraft((d) => ({ ...d, [key]: value }));
  const text = (key: keyof UpdateSpec, label: string, placeholder = '', command = false) => (
    <input
      className={`field ${command ? 'mono' : ''}`}
      aria-label={label}
      value={(draft[key] as string | undefined) ?? ''}
      placeholder={placeholder}
      disabled={command && !commandsEditable}
      title={command && !commandsEditable ? 'Set WEB_AUTH_TOKEN to edit commands and paths here' : undefined}
      onChange={(e) => set(key, e.target.value as never)}
    />
  );

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setError('');
    try {
      const { target } = await api.saveUpdate(draft, !initial);
      notify({ tone: 'ok', title: initial ? 'Saved' : `${target.name} added`, body: initial ? undefined : 'Checking it now.' });
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not save.');
    } finally {
      setSaving(false);
    }
  };

  const kind = draft.kind;
  return (
    <form className="panel" onSubmit={submit} onKeyDown={(e) => e.key === 'Escape' && !e.defaultPrevented && onCancel()}>
      <div className="section-head">
        <span className="label">{initial ? `Edit ${initial.name}` : 'Keep something updated'}</span>
      </div>
      <Row label="Name">
        <input className="field" value={draft.name} onChange={(e) => set('name', e.target.value)} required autoFocus={!initial} aria-label="Name" />
      </Row>
      <Row label="Kind" hint={KIND_HELP[kind]}>
        {initial ? (
          <span className="ink-2">{KIND_LABEL[kind]}</span>
        ) : (
          <Seg<UpdateKind> label="Kind" value={kind} onChange={(k) => set('kind', k)} options={(['release', 'apt', 'arr', 'git'] as UpdateKind[]).map((k) => ({ value: k, label: KIND_LABEL[k] }))} />
        )}
      </Row>
      {kind === 'apt' && <Row label="Package">{text('package', 'Package', 'plexmediaserver')}</Row>}
      {kind === 'arr' && (
        <>
          <Row label="config.xml" hint="The URL and API key are read from it when left empty">
            {text('config_file', 'config.xml', '/home/me/.config/Sonarr/config.xml', true)}
          </Row>
          <Row label="URL">{text('url', 'URL', 'http://127.0.0.1:8989')}</Row>
          <Row label="API key" hint="Settings → General → Security">
            <input
              className="field"
              type="password"
              autoComplete="off"
              aria-label="API key"
              placeholder={apiKeySet ? '•••••• (set — type to replace)' : 'from config.xml'}
              value={draft.api_key ?? ''}
              onChange={(e) => set('api_key', e.target.value)}
            />
          </Row>
        </>
      )}
      {kind === 'git' && (
        <>
          <Row label="Directory" hint="Its git commands run as the directory’s owner">
            {text('dir', 'Directory', '/opt/app', true)}
          </Row>
          <Row label="Build after pulling" hint="Optional, runs in the directory">
            {text('update_command', 'Build command', 'make build', true)}
          </Row>
        </>
      )}
      {kind === 'release' && (
        <>
          <Row label="GitHub repository">{text('repo', 'GitHub repository', 'owner/name')}</Row>
          <Row label="Tag prefix" hint="For projects that publish tags, not releases">
            {text('tag_prefix', 'Tag prefix', 'release-')}
          </Row>
          <Row label="Installed version" hint="A command that prints it">
            {text('version_command', 'Version command', 'app --version', true)}
          </Row>
          <Row label="Update command" hint="Empty means check only. Sees $VERSION and $TAG">
            {text('update_command', 'Update command', 'app --update', true)}
          </Row>
        </>
      )}
      {kind !== 'system' && kind !== 'snap' && !draft.self && (
        <Row label="Service" hint="systemd unit that must be running after an update">
          <span className="inline-fields">
            {text('unit', 'systemd unit', 'app.service')}
            {(kind === 'git' || kind === 'release') && (
              <label className="check">
                <input type="checkbox" checked={!!draft.restart} onChange={(e) => set('restart', e.target.checked)} /> Restart it after updating
              </label>
            )}
          </span>
        </Row>
      )}
      <Row label="Automatic" hint="Installs in the update window, then alerts">
        <label className="check">
          <input type="checkbox" checked={draft.auto} onChange={(e) => set('auto', e.target.checked)} /> Update automatically
        </label>
      </Row>
      <Row label="Paused" hint="Not checked at all">
        <label className="check">
          <input type="checkbox" checked={draft.paused} onChange={(e) => set('paused', e.target.checked)} /> Paused
        </label>
      </Row>
      {!commandsEditable && (kind === 'git' || kind === 'release' || kind === 'arr') && (
        <p className="form-help" style={{ margin: '4px 0 0' }}>
          Commands and paths run as root, so they can be changed here only when the dashboard has an access token (WEB_AUTH_TOKEN).
        </p>
      )}
      <div className="form-actions">
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <button type="button" className="btn-quiet" onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" className="btn" disabled={saving}>
          {saving ? 'Saving…' : initial ? 'Save' : 'Add'}
        </button>
      </div>
    </form>
  );
}
