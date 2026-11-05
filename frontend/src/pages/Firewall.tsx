import { useEffect, useMemo, useState } from 'react';
import { useSearchParams } from 'react-router';
import { api } from '../api/client';
import type { RemoteHost } from '../api/types';
import { FirewallManager, hostFirewall, localFirewall } from '../components/Firewall';
import { Select } from '../components/Select';
import { Skeleton } from '../components/Skeleton';
import { NetworkTabs } from '../components/ui';

// FirewallPage manages the whole firewall of this machine, or of a server through its agent
// (?server=<id>): what happens to traffic no rule decides, the rules, what they dropped.
export function FirewallPage() {
  const [params, setParams] = useSearchParams();
  const server = params.get('server') || 'local';
  const [hosts, setHosts] = useState<RemoteHost[]>();
  useEffect(() => {
    api.hosts().then(
      (r) => setHosts(r.hosts.filter((h) => h.kind === 'agent')),
      () => setHosts([]),
    );
  }, []);
  const host = hosts?.find((h) => h.id === server);
  const target = useMemo(() => (server === 'local' ? localFirewall : host ? hostFirewall(host) : undefined), [server, host]);
  const pick = (v: string) => setParams(v === 'local' ? {} : { server: v }, { replace: true });

  return (
    <main>
      <div className="page-title">
        <div className="min0">
          <h1>Firewall</h1>
          <div className="page-sub">What may reach {target ? target.name : 'the machine'}, what it may send, and what was dropped.</div>
          <NetworkTabs current="/network/firewall" />
        </div>
        {!!hosts?.length && (
          <div className="toolbar">
            <Select label="Machine" value={server} onChange={pick} options={[{ value: 'local', label: 'This machine' }, ...hosts.map((h) => ({ value: h.id, label: h.name }))]} />
          </div>
        )}
      </div>
      {target ? <FirewallManager key={target.key} target={target} /> : hosts ? <p className="note">No server with an agent by that name. Pick one above.</p> : <Skeleton kind="list" />}
    </main>
  );
}
