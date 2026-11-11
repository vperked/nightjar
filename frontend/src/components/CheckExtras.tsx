import { useCallback, useEffect, useState } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import { useCanEdit } from '../api/ConfigContext';
import type { Service } from '../api/types';
import { formatAgo, formatDuration } from '../format';
import { usePoll } from '../hooks/usePoll';
import { CodeBlock } from './Markdown';
import { Section } from './ui';
import { useToast } from './Toasts';

// The parts of a check's page that only some checks have: a heartbeat's ping URL and what it has
// heard, and the repairs Nightjar made.

// pingURL is where a heartbeat's job pings.
export const pingURL = (token: string) => `${window.location.origin}/ping/${token}`;

const when = (ts: number) => new Date(ts * 1000).toLocaleString(undefined, { weekday: 'short', hour: 'numeric', minute: '2-digit' });

// runtime is how long a heartbeat's last run took, when it pinged both ends.
export function runtime(svc: Service): number | null {
  const p = svc.pings;
  return p && p.start > 0 && p.ok >= p.start ? p.ok - p.start : null;
}

// usePingToken is a heartbeat's token. The services list leaves it out, since viewers read it too and
// whoever has it can report the job's runs; the admin gets it from the check itself.
function usePingToken(svc: Service): string | undefined {
  const canEdit = useCanEdit();
  const [token, setToken] = useState<string>();
  useEffect(() => {
    if (!canEdit || svc.target) return;
    let live = true;
    api.checks().then(
      (r) => live && setToken(r.checks.find((c) => c.name === svc.name)?.target),
      () => {},
    );
    return () => {
      live = false;
    };
  }, [canEdit, svc.name, svc.target]);
  return svc.target || (canEdit ? token : undefined);
}

export function HeartbeatPanel({ svc, now }: { svc: Service; now: number }) {
  const canEdit = useCanEdit();
  const token = usePingToken(svc);
  const url = token ? pingURL(token) : '';
  const p = svc.pings;
  const failed = p && p.fail > 0 && p.fail >= p.ok;
  return (
    <Section label="Heartbeat" aside={<span className="small ink-3 mono">{svc.schedule}</span>}>
      <div className="hb-grid">
        <div className="min0">
          {url ? (
            <>
              <p className="hb-lead">
                The job calls this URL when it finishes. Add <code>/start</code> when it begins, to time it and catch a run that hangs, and <code>/fail</code> or its exit code when it goes wrong.
                Anything it sends is kept with the ping.
              </p>
              <CodeBlock lang="bash" text={`curl -fsS -m 10 --retry 3 ${url}`} />
              <CodeBlock
                lang="bash"
                text={`# time the job, and report how it ended with its last lines\ncurl -fsS -m 10 ${url}/start\nout=$(/usr/local/bin/backup.sh 2>&1); code=$?\necho "$out" | tail -n 20 |\n  curl -fsS -m 10 --data-binary @- ${url}/$code`}
              />
            </>
          ) : (
            <p className="hb-lead">
              {canEdit ? 'Loading the ping URL…' : 'The job calls its ping URL when it finishes. The URL is hidden: whoever has it can report the job’s runs, so only the admin sees it.'}
            </p>
          )}
        </div>
        <dl className="kv hb-pings">
          <dt>Last started</dt>
          <dd>{p?.start ? `${when(p.start)} · ${formatAgo(p.start, now)}` : 'never'}</dd>
          <dt>Last finished</dt>
          <dd>{p?.ok ? `${when(p.ok)} · ${formatAgo(p.ok, now)}` : 'never'}</dd>
          <dt>Took</dt>
          <dd>{runtime(svc) !== null ? formatDuration(runtime(svc) as number) : '—'}</dd>
          <dt>Last failure</dt>
          <dd className={failed ? 'pct-poor' : undefined}>{p?.fail ? `${when(p.fail)}${p.exit > 1 ? ` · exit ${p.exit}` : ''}` : 'none'}</dd>
        </dl>
      </div>
      {p?.output && (
        <details className="hb-output" open={!!failed}>
          <summary>What the job sent last</summary>
          <pre>{p.output}</pre>
        </details>
      )}
    </Section>
  );
}

const WHAT: Record<string, string> = { restart: 'Restart', container: 'Restart container', script: 'Run' };

export function RepairsPanel({ svc, now }: { svc: Service; now: number }) {
  const fetch = useCallback(() => api.repairs(svc.name), [svc.name]);
  const { data, refresh } = usePoll(fetch, 30000);
  const [running, setRunning] = useState(false);
  const notify = useToast();
  const canEdit = useCanEdit();
  const list = data?.repairs ?? [];
  if (!svc.repair && list.length === 0) return null;
  const r = svc.repair;
  const tryNow = async () => {
    setRunning(true);
    try {
      const { repair } = await api.repairCheck(svc.name);
      notify({ tone: repair.ok ? 'ok' : 'error', title: repair.ok ? 'Repair ran' : 'Repair failed', body: repair.detail });
      refresh();
    } catch (e) {
      notify({ tone: 'error', title: 'Could not run the repair', body: (e as Error).message });
    } finally {
      setRunning(false);
    }
  };
  const fixed = list.filter((x) => x.fixed).length;
  return (
    <Section
      label="Repairs"
      aside={
        r &&
        canEdit && (
          <button type="button" className="btn-ghost" onClick={tryNow} disabled={running}>
            {running ? 'Running…' : 'Try it now'}
          </button>
        )
      }
    >
      <p className="repair-plan">
        {r ? (
          <>
            When it goes down, Nightjar will <strong>{r.action === 'restart' ? `restart ${r.unit}` : r.action === 'container' ? `restart the ${r.container} container` : `run ${r.script}`}</strong>, up
            to {r.max === 1 ? 'once' : `${r.max} times`} {r.wait < 3600 ? `${Math.round(r.wait / 60)} min` : formatDuration(r.wait)} apart, then check it again.{' '}
          </>
        ) : (
          'This check no longer has a repair. '
        )}
        {list.length > 0 && (
          <span className="ink-3">
            {fixed} of {list.length} {list.length === 1 ? 'attempt' : 'attempts'} brought it back.
          </span>
        )}
      </p>
      {list.length > 0 ? (
        <ul className="repairs">
          {list.slice(0, 12).map((x) => (
            <li key={x.id} className={x.fixed ? 'fixed' : x.ok ? 'ok' : 'fail'}>
              <span className="repair-when num">{formatAgo(x.ts, now)}</span>
              <span className="repair-what">
                {WHAT[x.action] ?? x.action} <span className="mono">{x.target}</span>
                <span className="repair-detail">{x.detail}</span>
                {x.output && (
                  <details>
                    <summary>Output</summary>
                    <pre>{x.output}</pre>
                  </details>
                )}
              </span>
              <span className="repair-result">{x.fixed ? 'fixed it' : x.ok ? 'ran' : 'failed'}</span>
            </li>
          ))}
        </ul>
      ) : (
        <p className="note" style={{ padding: 0 }}>
          No repairs yet. <Link to="/docs/uptime-checks#repairs">How repairs work →</Link>
        </p>
      )}
    </Section>
  );
}
