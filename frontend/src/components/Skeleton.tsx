// Placeholders shaped like the page that is loading, so it does not jump when the data arrives.

type Kind = 'dashboard' | 'list' | 'form';

const block = (w: string, h: number, key?: number) => <span key={key} className="sk" style={{ width: w, height: h }} />;

export function Skeleton({ kind = 'list', rows = 6 }: { kind?: Kind; rows?: number }) {
  return (
    <div className="skeleton" role="status" aria-busy="true" aria-live="polite">
      <span className="sr-only">Loading…</span>
      {kind === 'dashboard' && (
        <>
          <div className="sk-cards">
            {[0, 1, 2, 3].map((i) => (
              <div key={i} className="sk-card">
                {block('40%', 11)}
                {block('55%', 26)}
                {block('70%', 10)}
              </div>
            ))}
          </div>
          <div className="sk-card sk-tall">
            {block('22%', 13)}
            {block('100%', 150)}
          </div>
        </>
      )}
      {kind === 'list' && (
        <div className="sk-card sk-list">
          {Array.from({ length: rows }, (_, i) => (
            <div key={i} className="sk-row">
              {block('72px', 20)}
              <span className="sk-stack">
                {block(`${40 + ((i * 17) % 35)}%`, 12)}
                {block(`${25 + ((i * 13) % 25)}%`, 10)}
              </span>
              {block('64px', 12)}
            </div>
          ))}
        </div>
      )}
      {kind === 'form' &&
        [0, 1].map((g) => (
          <div key={g} className="sk-card sk-list">
            {block('18%', 13)}
            {Array.from({ length: 3 }, (_, i) => (
              <div key={i} className="sk-row">
                <span className="sk-stack">
                  {block('30%', 12)}
                  {block('55%', 10)}
                </span>
                {block('200px', 32)}
              </div>
            ))}
          </div>
        ))}
    </div>
  );
}

// Working is a short message beside a spinner, for waits like a restart.
export function Working({ text }: { text: string }) {
  return (
    <p className="working" role="status">
      <span className="spinner" aria-hidden="true" />
      {text}
    </p>
  );
}
