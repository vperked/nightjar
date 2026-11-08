import { Fragment, type ReactNode } from 'react';
import type { Finding } from '../api/types';

// What went wrong, read out of the logs: each finding says what happened, what it means and what
// to do, with the log line it came from. The first recognised one is the likely cause.

const MARK: Record<Finding['severity'], string> = { critical: 'M8 8l8 8M16 8l-8 8', warning: 'M12 7v6M12 16.5v.5' };

// withCode turns `backticked` parts of a fix into inline code.
function withCode(text: string): ReactNode {
  return text.split(/(`[^`]+`)/).map((part, i) => (part.startsWith('`') && part.endsWith('`') && part.length > 2 ? <code key={i}>{part.slice(1, -1)}</code> : <Fragment key={i}>{part}</Fragment>));
}

function Card({ f, likely }: { f: Finding; likely: boolean }) {
  return (
    <li className={`dx ${f.severity} ${f.known ? '' : 'grouped'}`}>
      <span className="dx-mark" aria-hidden="true">
        <svg viewBox="0 0 24 24">
          <path d={MARK[f.severity]} />
        </svg>
      </span>
      <div className="dx-body min0">
        {likely && <span className="dx-likely">Likely cause</span>}
        <div className="dx-title">
          <strong>{f.title}</strong>
          {f.count > 1 && <span className="dx-count num">×{f.count}</span>}
        </div>
        <p className="dx-explain">{f.explain}</p>
        {f.fix && (
          <p className="dx-fix">
            <span className="dx-fix-label">Fix</span> {withCode(f.fix)}
          </p>
        )}
        {f.line && (
          <details className="dx-line">
            <summary>The log line</summary>
            <pre className="mono">{f.line}</pre>
          </details>
        )}
      </div>
    </li>
  );
}

export function Diagnosis({ findings, label = 'What went wrong' }: { findings: Finding[]; label?: string }) {
  if (!findings.length) return null;
  const likely = findings.findIndex((f) => f.known);
  return (
    <section className="dx-panel" aria-label={label}>
      <span className="dx-panel-label">{label}</span>
      <ul className="dx-list">
        {findings.map((f, i) => (
          <Card key={`${f.id}${f.title}`} f={f} likely={i === likely && likely === 0} />
        ))}
      </ul>
    </section>
  );
}

// CauseLine is a finding in one line, for a list row.
export function CauseLine({ cause }: { cause: Finding }) {
  return (
    <span className={`dx-cause ${cause.severity}`} title={cause.explain}>
      {cause.title}
    </span>
  );
}
