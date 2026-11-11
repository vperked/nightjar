import { useCallback } from 'react';
import { Link } from 'react-router';
import { api } from '../api/client';
import { formatAgo, formatDuration } from '../format';
import { useNow } from '../hooks/useNow';
import { usePoll } from '../hooks/usePoll';

// LatestIncident is the overview's line about the newest incident of the last three days.
export function LatestIncident() {
  const fetch = useCallback(() => api.incidents(3), []);
  const { data } = usePoll(fetch, 60000);
  const now = useNow(30000);
  const inc = data?.incidents[0];
  if (!inc) return null;
  const ongoing = inc.ended === null;
  // Whole minutes, as the line moves on only every half minute: seconds would jump by 30 at a time.
  const lasted = now - inc.started;
  const sofar = lasted < 60 ? 'under a minute so far' : `${formatDuration(Math.floor(lasted / 60) * 60)} so far`;
  return (
    <Link to="/incidents" className={`latest-incident ${ongoing ? 'ongoing' : ''}`}>
      <span className={`inc-state ${ongoing ? 'open' : 'closed'}`}>{ongoing ? 'Ongoing' : 'Last incident'}</span>
      <span className="min0">
        <strong>{inc.title}</strong>
        <span className="latest-why">{inc.cause ? `${inc.cause.confidence === 'likely' ? 'Likely' : 'Possibly'}: ${inc.cause.text}` : 'No other cause found on the machine'}</span>
      </span>
      <span className="latest-when num">{ongoing ? sofar : formatAgo(inc.ended, now)} →</span>
    </Link>
  );
}
