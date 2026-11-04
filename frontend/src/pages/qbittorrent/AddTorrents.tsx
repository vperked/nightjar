import { useEffect, useState, type FormEvent } from 'react';
import { ApiError, api } from '../../api/client';
import { Select } from '../../components/Select';
import { useToast } from '../../components/Toasts';
import { plural } from '../../format';

// AddTorrents takes magnet links or URLs, one per line.
export function AddTorrents({ onClose, onAdded }: { onClose: () => void; onAdded: () => void }) {
  const notify = useToast();
  const [urls, setUrls] = useState('');
  const [category, setCategory] = useState('');
  const [stopped, setStopped] = useState(false);
  const [categories, setCategories] = useState<string[]>([]);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  useEffect(() => {
    api
      .qbCategories()
      .then((r) => setCategories(r.categories))
      .catch(() => setCategories([]));
  }, []);
  const submit = async (e: FormEvent) => {
    e.preventDefault();
    setSaving(true);
    setError('');
    try {
      const { added } = await api.qbAdd(urls, category, stopped);
      notify({ tone: 'ok', title: `Added ${plural(added, 'torrent')}`, body: stopped ? 'They are stopped; start them when ready.' : 'Magnet links appear once their metadata arrives.' });
      onAdded();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : 'Could not add them.');
    } finally {
      setSaving(false);
    }
  };
  return (
    <form className="panel qb-add" onSubmit={submit} onKeyDown={(e) => e.key === 'Escape' && !e.defaultPrevented && onClose()}>
      <div className="section-head">
        <span className="label">Add torrents</span>
      </div>
      <textarea
        className="field mono"
        rows={3}
        aria-label="Magnet links or URLs"
        placeholder="magnet:?xt=urn:btih:…  or  https://…/file.torrent — one per line"
        value={urls}
        onChange={(e) => setUrls(e.target.value)}
        autoFocus
        required
      />
      <div className="qb-add-options">
        <span className="qb-add-category">
          <span className="label">Category</span>
          <Select
            className="short"
            label="Category"
            value={category}
            onChange={setCategory}
            options={[{ value: '', label: 'none', text: 'none' }, ...categories.map((c) => ({ value: c, label: c }))]}
          />
        </span>
        <label className="check">
          <input type="checkbox" checked={stopped} onChange={(e) => setStopped(e.target.checked)} /> Add stopped
        </label>
      </div>
      <div className="form-actions">
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <button type="button" className="btn-quiet" onClick={onClose}>
          Cancel
        </button>
        <button type="submit" className="btn" disabled={saving || !urls.trim()}>
          {saving ? 'Adding…' : 'Add'}
        </button>
      </div>
    </form>
  );
}
