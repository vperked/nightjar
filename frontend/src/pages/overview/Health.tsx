import { Link } from 'react-router';
import { useCanEdit } from '../../api/ConfigContext';
import { useServices } from '../../api/ServicesContext';
import type { DrivesResponse, Unit, UnitsResponse } from '../../api/types';
import { useWaitingUpdates } from '../../api/UpdatesContext';
import { isProblem, verdict as unitVerdict } from '../../components/unitState';
import { formatAgo, noun, plural } from '../../format';

// The overview's health at a glance: the one-line verdict under the server's name, and the card
// that appears when systemd services need attention.

// HealthSummary is the overview's one-line verdict, with a chip for each part that links to it.
export function HealthSummary({ units, drives }: { units: UnitsResponse | undefined; drives: DrivesResponse | undefined }) {
  const { data: checks } = useServices();
  // The same count as the Apps tab: every machine's, and the chip leads to where they wait.
  const updates = useWaitingUpdates();
  const list = checks?.services ?? [];
  const watched = list.filter((c) => c.status !== 'paused');
  const checksDown = watched.filter((c) => c.status === 'down').length;
  const unitProblems = (units?.units ?? []).filter(isProblem).length;
  const driveList = drives?.enabled ? drives.drives : [];
  const failing = driveList.filter((d) => d.health === 'failing').length;
  const warning = driveList.filter((d) => d.health === 'warning').length;
  const available = updates.total;
  const problems = checksDown + unitProblems + failing;
  const tone = problems ? 'crit' : warning ? 'warn' : 'good';
  const verdict = problems ? plural(problems, 'problem') : warning ? 'Running, with a warning' : 'All systems normal';
  return (
    <div className="health-summary">
      <span className={`health-verdict ${tone}`}>
        <span className="health-dot" aria-hidden="true" />
        {verdict}
      </span>
      {watched.length > 0 && (
        <Link className={`health-chip ${checksDown ? 'crit' : ''}`} to="/services">
          <span className="num">
            {watched.length - checksDown}/{watched.length}
          </span>{' '}
          checks up
        </Link>
      )}
      {units && (
        <Link className={`health-chip ${unitProblems ? 'crit' : ''}`} to="/system">
          {unitProblems ? (
            <>
              <span className="num">{unitProblems}</span> {noun(unitProblems, 'service')} down
            </>
          ) : (
            <>
              <span className="num">{units.units.length}</span> services running
            </>
          )}
        </Link>
      )}
      {driveList.length > 0 && (
        <a className={`health-chip ${failing ? 'crit' : warning ? 'warn' : ''}`} href="#drive-health">
          {failing ? `${plural(failing, 'drive')} failing` : warning ? plural(warning, 'drive warning') : 'drives healthy'}
        </a>
      )}
      {available > 0 && (
        <Link className="health-chip" to={updates.at}>
          <span className="num">{available}</span> {noun(available, 'update')}
        </Link>
      )}
    </div>
  );
}

// ServiceProblems lists systemd services that are down or restarting, when there are any.
export function ServiceProblems({ data }: { data: UnitsResponse | undefined }) {
  const canEdit = useCanEdit();
  const now = Date.now() / 1000;
  const down = (data?.units ?? []).filter(isProblem);
  if (!down.length) return null;
  const detail = (u: Unit) => {
    if (u.flapping) return `${plural(u.flapping, 'restart')} in 10 min`;
    if (u.sub === 'auto-restart') return u.restarts ? plural(u.restarts, 'restart') : 'restarting';
    return u.since ? `for ${formatAgo(u.since, now).replace(' ago', '')}` : '';
  };
  // The section is an alert, so what is said in it is read out whenever it changes. "for 12m"
  // moves on by itself, so screen readers get the time it went down instead, which stays put.
  const since = (u: Unit) => (!u.flapping && u.sub !== 'auto-restart' && u.since ? `since ${new Date(u.since * 1000).toLocaleTimeString(undefined, { hour: 'numeric', minute: '2-digit' })}` : '');
  return (
    <section className="attention" role="alert" aria-label="Services that need attention">
      <div className="attention-head">
        <span className="attention-icon" aria-hidden="true">
          <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="3" strokeLinecap="round">
            <path d="M12 6.5v6.5M12 17.5v.01" />
          </svg>
        </span>
        <div className="min0">
          <strong>{down.length === 1 ? 'A system service needs attention' : `${down.length} system services need attention`}</strong>
          <div className="attention-sub">Alerts went out when they stopped. Open one for its log{canEdit ? ' and a restart button' : ''}.</div>
        </div>
        <Link className="btn-quiet attention-all" to="/system">
          System services →
        </Link>
      </div>
      <div className="attention-chips">
        {down.map((u) => {
          const [tone, word] = unitVerdict(u);
          return (
            <Link key={u.name} className="attention-chip" to={`/system?open=${encodeURIComponent(u.name)}`}>
              <span className={`state ${tone}`}>{word}</span>
              <span className="attention-name">{u.name.replace(/\.service$/, '')}</span>
              {since(u) ? (
                <>
                  <span className="attention-meta num" aria-hidden="true">
                    {detail(u)}
                  </span>
                  <span className="sr-only">{since(u)}</span>
                </>
              ) : (
                <span className="attention-meta num">{detail(u)}</span>
              )}
            </Link>
          );
        })}
      </div>
    </section>
  );
}
