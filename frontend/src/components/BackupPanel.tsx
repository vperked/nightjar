import { useRef, useState } from 'react';
import { api } from '../api/client';
import { SettingsGroup } from './SettingsGroup';
import { useToast } from './Toasts';

// Backup and restore: Nightjar's own setup in one file, for a new machine or to undo a mistake.

// readText reads a chosen file, with FileReader where File.text is missing.
function readText(f: File): Promise<string> {
  if (typeof f.text === 'function') return f.text();
  return new Promise((resolve, reject) => {
    const r = new FileReader();
    r.onload = () => resolve(String(r.result));
    r.onerror = () => reject(r.error);
    r.readAsText(f);
  });
}

export function BackupPanel() {
  const notify = useToast();
  const file = useRef<HTMLInputElement>(null);
  const [pending, setPending] = useState<{ name: string; data: Record<string, unknown> }>();
  const [busy, setBusy] = useState(false);
  const download = async (secrets: boolean) => {
    try {
      const b = await api.backup(secrets);
      const blob = new Blob([JSON.stringify(b, null, 2)], { type: 'application/json' });
      const a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = `nightjar-backup-${new Date().toISOString().slice(0, 10)}${secrets ? '-with-secrets' : ''}.json`;
      a.click();
      setTimeout(() => URL.revokeObjectURL(a.href), 1000);
    } catch (e) {
      notify({ tone: 'error', title: 'Could not make the backup', body: (e as Error).message });
    }
  };
  const pick = async (f: File | undefined) => {
    if (!f) return;
    try {
      const data = JSON.parse(await readText(f)) as Record<string, unknown>;
      if (!data.nightjar) throw new Error('That is not a Nightjar backup.');
      setPending({ name: f.name, data });
    } catch (e) {
      notify({ tone: 'error', title: 'Could not read the file', body: (e as Error).message });
    }
    if (file.current) file.current.value = '';
  };
  const restore = async () => {
    if (!pending) return;
    setBusy(true);
    try {
      const r = await api.restoreBackup(pending.data);
      notify({
        tone: 'ok',
        title: 'Backup restored',
        body: `${r.restored.checks ?? 0} checks, ${r.restored.settings ?? 0} settings and ${r.restored.parts ?? 0} other parts.${r.restarting ? ' Nightjar is restarting to put them in force.' : ''}`,
      });
      setPending(undefined);
    } catch (e) {
      notify({ tone: 'error', title: 'Could not restore it', body: (e as Error).message });
    } finally {
      setBusy(false);
    }
  };
  const checks = Array.isArray(pending?.data.checks) ? (pending!.data.checks as unknown[]).length : 0;
  return (
    <SettingsGroup id="backup" name="Backup">
      <div className="backup-row">
        <div className="min0">
          <strong>Download a backup</strong>
          <span className="form-help">Checks, settings made here, alert rules, maintenance, the appearance, the status page, other servers and plugin settings. Not the history.</span>
        </div>
        <span className="inline-fields">
          <button type="button" className="btn-quiet" onClick={() => download(false)}>
            Without secrets
          </button>
          <button type="button" className="btn-quiet" onClick={() => download(true)} title="Passwords, tokens and API keys too: keep this file safe">
            With secrets
          </button>
        </span>
      </div>
      <div className="backup-row">
        <div className="min0">
          <strong>Restore one</strong>
          <span className="form-help">Its checks are added or replaced, others are left. Its settings and the rest replace what is here, and Nightjar restarts.</span>
        </div>
        <span className="inline-fields">
          <input ref={file} type="file" accept="application/json,.json" aria-label="Backup file" hidden onChange={(e) => pick(e.target.files?.[0])} />
          <button type="button" className="btn-quiet" onClick={() => file.current?.click()}>
            Choose a file…
          </button>
        </span>
      </div>
      {pending && (
        <div className="backup-confirm">
          <span>
            Restore <strong>{pending.name}</strong>
            {typeof pending.data.made === 'number' ? `, made ${new Date((pending.data.made as number) * 1000).toLocaleString()}` : ''}, with {checks} checks?
          </span>
          <span className="inline-fields">
            <button type="button" className="btn-quiet" onClick={() => setPending(undefined)}>
              Cancel
            </button>
            <button type="button" className="btn" onClick={restore} disabled={busy}>
              {busy ? 'Restoring…' : 'Restore'}
            </button>
          </span>
        </div>
      )}
    </SettingsGroup>
  );
}
