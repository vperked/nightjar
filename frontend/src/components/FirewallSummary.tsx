import { useEffect, useState } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import type { FirewallStatus, RemoteHost } from '../api/types';
import { plural } from '../format';
import { TOOL } from './Firewall';
import { Section } from './ui';

// FirewallSummary is a server's firewall in a line, on its page, with the way to manage all of it.
export function FirewallSummary({ h }: { h: Pick<RemoteHost, 'id' | 'name'> }) {
  const canEdit = useCanEdit();
  const [st, setSt] = useState<FirewallStatus>();
  const [error, setError] = useState('');
  useEffect(() => {
    api.hostFirewall(h.id).then(
      (v) => setSt(v.firewall),
      (e) => setError((e as Error).message),
    );
  }, [h.id]);
  const to = `/network/firewall?server=${encodeURIComponent(h.id)}`;
  const inRules = st?.rules.filter((r) => r.direction !== 'out' && !r.paused).length ?? 0;
  const outRules = st?.rules.filter((r) => r.direction === 'out' && !r.paused).length ?? 0;
  const dropped = st?.drops?.length ?? 0;
  return (
    <Section label="Firewall" aside={<Link to={to}>{canEdit ? 'manage' : 'view'} →</Link>}>
      {error ? (
        <p className="note">{error}</p>
      ) : !st ? (
        <p className="note">Reading the firewall…</p>
      ) : (
        <p className="fw-summary">
          {TOOL[st.tool]}
          {st.default === 'drop' ? ' · drops what no rule allows' : st.policy ? ` · ${st.policy === 'DROP' ? 'drops' : 'lets in'} what no rule decides` : ''}
          {' · '}
          {inRules || outRules ? `${plural(inRules, 'incoming rule')}${outRules ? `, ${plural(outRules, 'outgoing')}` : ''}` : 'no rules set from here'}
          {dropped ? ` · ${plural(dropped, 'packet')} dropped lately` : ''}
          {st.pending && (
            <>
              {' · '}
              <Link to={to}>a change waits to be kept</Link>
            </>
          )}
        </p>
      )}
    </Section>
  );
}
