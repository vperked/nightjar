import { useEffect, useState } from 'react';
import { Link } from 'react-router';
import { api, errorText } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import type { RemoteHost, UpdatesResponse } from '../api/types';
import { formatAgo, plural } from '../format';
import { Section } from './ui';

// UpdatesSummary is a server's updates in a line, on its page, with the way to the Updates page
// picked to that server, where they are checked and installed.
export function UpdatesSummary({ h }: { h: Pick<RemoteHost, 'id' | 'name'> }) {
  const canEdit = useCanEdit();
  const [data, setData] = useState<UpdatesResponse>();
  const [error, setError] = useState('');
  useEffect(() => {
    api.hostUpdates(h.id).then(
      (v) => setData(v),
      (e) => setError(errorText(e)),
    );
  }, [h.id]);
  const to = `/updates?host=${encodeURIComponent(h.id)}`;
  const targets = data?.targets ?? [];
  const waiting = targets.filter((t) => t.available && !t.paused);
  const updating = targets.find((t) => t.busy === 'updating');
  const failed = targets.filter((t) => t.runs?.[0] && !t.runs[0].ok && t.available);
  return (
    <Section label="Updates" aside={<Link to={to}>{canEdit ? 'manage' : 'view'} →</Link>}>
      {error ? (
        <p className="note">{error}</p>
      ) : !data ? (
        <p className="note">Reading its updates…</p>
      ) : (
        <p className="fw-summary">
          {updating ? (
            <>Updating {updating.name} now</>
          ) : waiting.length ? (
            <>
              {plural(waiting.length, 'update')} waiting: {waiting.map((t) => t.name).join(', ')}
            </>
          ) : (
            'Up to date'
          )}
          {failed.length > 0 && ` · ${failed.map((t) => t.name).join(', ')} failed last time`}
          {' · '}
          {data.last_check ? `checked ${formatAgo(data.last_check)}` : 'not checked yet'}
          {data.enabled ? ` · auto-updates ${data.window.label}` : ' · checks off'}
          {canEdit && waiting.length > 0 && !updating && (
            <>
              {' · '}
              <Link to={to}>install</Link>
            </>
          )}
        </p>
      )}
    </Section>
  );
}
