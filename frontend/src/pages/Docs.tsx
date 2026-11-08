import { useEffect, useMemo, useRef, useState, type RefObject } from 'react';
import { Link, useLocation, useNavigate, useParams } from 'react-router';
import { Markdown, plain, slug as anchor } from '../components/Markdown';
import { DOC_GROUPS, DOCS, type DocEntry } from '../docs';

// The docs: how to do everything, written as Markdown in src/docs and built into the dashboard, so
// they match this version and work without the internet.

const BODIES = import.meta.glob<string>('../docs/*.md', { query: '?raw', import: 'default', eager: true });

export const docBody = (s: string) => BODIES[`../docs/${s}.md`];

// hashId is the section a link's #fragment names. A malformed escape ("#%") is taken as written
// rather than thrown, which would blank the page.
const hashId = (hash: string) => {
  try {
    return decodeURIComponent(hash.slice(1));
  } catch {
    return hash.slice(1);
  }
};

interface Section {
  doc: DocEntry;
  heading: string; // "" for the part before the first heading
  id: string;
  text: string;
}

// sections splits every page at its ## headings, for search, as plain text: no Markdown marks,
// table rules or fences, but the commands inside code blocks, which are worth finding.
const SECTIONS: Section[] = DOCS.flatMap((doc) => {
  const out: Section[] = [];
  let cur: Section = { doc, heading: '', id: '', text: '' };
  for (const line of (docBody(doc.slug) ?? '').split('\n')) {
    const h = line.match(/^##\s+(.*)$/);
    if (h) {
      out.push(cur);
      cur = { doc, heading: plain(h[1]), id: anchor(plain(h[1])), text: '' };
      continue;
    }
    if (line.startsWith('# ') || /^\s*```/.test(line) || /^\s*\|?\s*:?-{3,}/.test(line)) continue;
    const text = plain(line)
      .replace(/^\s*(?:[-*>]|\d+\.)\s+/, '')
      .replace(/^#{3}\s+/, '')
      .replace(/\s*\|\s*/g, ' · ')
      .replace(/^ · | · $/g, '');
    cur.text += ' ' + text;
  }
  out.push(cur);
  // A page that opens with a heading has nothing before it to find.
  return out.map((s) => ({ ...s, text: s.text.replace(/\s+/g, ' ').trim() })).filter((s) => s.text);
});

interface Hit {
  section: Section;
  score: number;
  snippet: string;
}

// search finds the sections containing every word of q, title and heading matches first.
export function searchDocs(q: string): Hit[] {
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return [];
  const hits: Hit[] = [];
  for (const s of SECTIONS) {
    const title = `${s.doc.title} ${s.heading}`.toLowerCase();
    const body = `${s.text} ${s.doc.words}`.toLowerCase();
    if (!words.every((w) => title.includes(w) || body.includes(w))) continue;
    const score = words.reduce((n, w) => n + (title.includes(w) ? 3 : 0) + (body.includes(w) ? 1 : 0), 0) + (s.heading ? 0 : 0.5);
    const at = Math.max(0, s.text.toLowerCase().indexOf(words[0]));
    const start = Math.max(0, at - 50);
    const snippet = (start > 0 ? '…' : '') + s.text.slice(start, start + 150) + (start + 150 < s.text.length ? '…' : '');
    hits.push({ section: s, score, snippet });
  }
  return hits.sort((a, b) => b.score - a.score).slice(0, 20);
}

// Mark highlights the words of q in text.
function Mark({ text, q }: { text: string; q: string }) {
  const words = q.toLowerCase().split(/\s+/).filter(Boolean);
  if (!words.length) return <>{text}</>;
  const re = new RegExp(`(${words.map((w) => w.replace(/[.*+?^${}()|[\]\\]/g, '\\$&')).join('|')})`, 'gi');
  return (
    <>
      {/* Splitting on a captured pattern puts the matches at the odd places. */}
      {text.split(re).map((part, i) => (i % 2 === 1 ? <mark key={i}>{part}</mark> : part))}
    </>
  );
}

// Each group of guides has an icon, for the sidebar and the docs home.
const GROUP_ICON: Record<string, string> = {
  'Getting started': 'M5 12h14M13 6l6 6-6 6',
  'This server': 'M4 5h16v10H4zM8 19h8M12 15v4',
  'Other servers': 'M4 4h16v6H4zM4 14h16v6H4zM8 7h.01M8 17h.01',
  Alerts: 'M6 16V11a6 6 0 1 1 12 0v5l2 2H4zM10 20a2 2 0 0 0 4 0',
  'Apps and media': 'M4 4h6v6H4zM14 4h6v6h-6zM4 14h6v6H4zM14 17h6M17 14v6',
  Administration:
    'M12 15a3 3 0 1 0 0-6 3 3 0 0 0 0 6zM19.4 15a1.7 1.7 0 0 0 .3 1.9l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.9-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.6 1.7 1.7 0 0 0-1.9.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.9 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.6-1.1 1.7 1.7 0 0 0-.3-1.9l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.9.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.9-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.9V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z',
};

function GroupIcon({ name }: { name: string }) {
  return (
    <svg className="docs-icon" viewBox="0 0 24 24" aria-hidden="true">
      <path d={GROUP_ICON[name] ?? GROUP_ICON['Getting started']} />
    </svg>
  );
}

function Nav({ current, onPick }: { current: string; onPick?: () => void }) {
  return (
    <nav className="docs-nav" aria-label="Docs">
      <Link to="/docs" className={`docs-nav-home ${current === '' ? 'active' : ''}`} aria-current={current === '' ? 'page' : undefined} onClick={onPick}>
        Docs home
      </Link>
      {DOC_GROUPS.map((g) => (
        <div key={g.name} className="docs-nav-group">
          <span className="docs-nav-label">
            <GroupIcon name={g.name} />
            {g.name}
          </span>
          {g.pages.map((p) => (
            <Link key={p.slug} to={`/docs/${p.slug}`} className={p.slug === current ? 'active' : ''} aria-current={p.slug === current ? 'page' : undefined} onClick={onPick}>
              {p.title}
            </Link>
          ))}
        </div>
      ))}
    </nav>
  );
}

function Results({ q, onPick }: { q: string; onPick: () => void }) {
  const hits = useMemo(() => searchDocs(q), [q]);
  return (
    <section className="docs-results" aria-label="Search results">
      <h1>Search</h1>
      <p className="page-sub">{hits.length ? `${hits.length === 20 ? 'The best 20' : hits.length} ${hits.length === 1 ? 'match' : 'matches'} for “${q}”` : `Nothing matches “${q}”.`}</p>
      <ul>
        {hits.map((h) => (
          <li key={`${h.section.doc.slug}#${h.section.id}`}>
            <Link to={`/docs/${h.section.doc.slug}${h.section.id ? `#${h.section.id}` : ''}`} onClick={onPick}>
              <strong>
                <Mark text={h.section.heading || h.section.doc.title} q={q} />
              </strong>
              <span className="docs-result-where">{h.section.heading ? h.section.doc.title : h.section.doc.group}</span>
              <span className="docs-result-text">
                <Mark text={h.snippet} q={q} />
              </span>
            </Link>
          </li>
        ))}
      </ul>
    </section>
  );
}

// readingTime is about how long a page takes to read, at 220 words a minute.
export function readingTime(body: string): number {
  return Math.max(1, Math.round(plain(body).split(/\s+/).filter(Boolean).length / 220));
}

// useSection follows the section in view, for the outline.
function useSection(ids: string[], key: string): string {
  const [current, setCurrent] = useState('');
  useEffect(() => {
    setCurrent('');
    if (typeof IntersectionObserver === 'undefined' || !ids.length) return;
    const seen = new Map<string, boolean>();
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) seen.set(e.target.id, e.isIntersecting);
        // The first heading on screen, or failing that the last one passed.
        const onScreen = ids.find((id) => seen.get(id));
        if (onScreen) setCurrent(onScreen);
      },
      { rootMargin: '-80px 0px -55% 0px' },
    );
    for (const id of ids) {
      const el = document.getElementById(id);
      if (el) io.observe(el);
    }
    return () => io.disconnect();
  }, [key]);
  return current;
}

// useReadingProgress stretches bar across the top of the window as article is read: empty at its
// top, full once its end is on screen. It writes the bar's style straight away rather than through
// state, so scrolling never re-renders the page.
function useReadingProgress(article: RefObject<HTMLElement>, bar: RefObject<HTMLElement>, key: string) {
  useEffect(() => {
    let frame = 0;
    const measure = () => {
      frame = 0;
      const a = article.current;
      const b = bar.current;
      if (!a || !b) return;
      const r = a.getBoundingClientRect();
      const total = r.height - window.innerHeight;
      const p = total <= 0 ? 1 : Math.min(1, Math.max(0, -r.top / total));
      b.style.transform = `scaleX(${p})`;
    };
    const soon = () => {
      if (!frame) frame = requestAnimationFrame(measure);
    };
    measure();
    window.addEventListener('scroll', soon, { passive: true });
    window.addEventListener('resize', soon);
    return () => {
      window.removeEventListener('scroll', soon);
      window.removeEventListener('resize', soon);
      if (frame) cancelAnimationFrame(frame);
    };
  }, [article, bar, key]);
}

// OnThisPage is the outline as a small menu beside the page's group, for screens too narrow for the
// outline column. Picking a section closes it.
function OnThisPage({ outline, current }: { outline: string[]; current: string }) {
  const box = useRef<HTMLDetailsElement>(null);
  // Like any menu, it closes on a press elsewhere and on Escape.
  useEffect(() => {
    const close = () => box.current?.removeAttribute('open');
    const press = (e: PointerEvent) => {
      if (box.current?.open && !box.current.contains(e.target as Node)) close();
    };
    const key = (e: KeyboardEvent) => {
      if (e.key === 'Escape' && box.current?.open) {
        close();
        box.current.querySelector('summary')?.focus();
      }
    };
    document.addEventListener('pointerdown', press);
    document.addEventListener('keydown', key);
    return () => {
      document.removeEventListener('pointerdown', press);
      document.removeEventListener('keydown', key);
    };
  }, []);
  return (
    <details className="docs-toc" ref={box}>
      <summary>
        <svg viewBox="0 0 24 24" aria-hidden="true">
          <path d="M4 6h16M4 12h10M4 18h13" />
        </svg>
        On this page
      </summary>
      <nav aria-label="On this page">
        {outline.map((h) => (
          <a key={h} href={`#${anchor(h)}`} className={current === anchor(h) ? 'active' : ''} onClick={() => box.current?.removeAttribute('open')}>
            {h}
          </a>
        ))}
      </nav>
    </details>
  );
}

// SearchBox is the docs' search, in the sidebar and on the home page; Enter opens the best match.
function SearchBox({ query, setQuery, onPick, big = false }: { query: string; setQuery: (q: string) => void; onPick: () => void; big?: boolean }) {
  const navigate = useNavigate();
  return (
    <label className={`docs-search ${big ? 'big' : ''}`}>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <circle cx="11" cy="11" r="7" />
        <path d="M20 20l-3.5-3.5" />
      </svg>
      <input
        type="search"
        placeholder={big ? 'Search every guide…' : 'Search the docs…'}
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        onKeyDown={(e) => {
          if (e.key === 'Enter') {
            const first = searchDocs(query)[0];
            if (first) {
              navigate(`/docs/${first.section.doc.slug}${first.section.id ? `#${first.section.id}` : ''}`);
              onPick();
            }
          }
        }}
        aria-label="Search the docs"
      />
    </label>
  );
}

const START = [
  { to: '/docs/getting-started', title: 'Install Nightjar', text: 'Build it, run it as a service and claim the dashboard.' },
  { to: '/docs/servers', title: 'Watch another server', text: 'Temperatures, disks and drives with the agent, and the services on each.' },
  { to: '/docs/alerts', title: 'Get alerts on your phone', text: 'Discord, ntfy, Telegram, Slack or email, and when each one hears.' },
];

function DocsHome({ query, setQuery, onPick }: { query: string; setQuery: (q: string) => void; onPick: () => void }) {
  return (
    <div className="docs-home">
      <header className="docs-hero">
        <span className="docs-eyebrow">Nightjar docs</span>
        <h1>Everything Nightjar does, and how to make it yours.</h1>
        <p className="doc-lead">Guides for watching this server and your others, getting the alerts you want, and running it all safely.</p>
        <SearchBox query={query} setQuery={setQuery} onPick={onPick} big />
      </header>
      <div className="docs-start">
        {START.map((c, i) => (
          <Link key={c.to} to={c.to} className="docs-start-card">
            <span className="docs-start-n">{i + 1}</span>
            <strong>{c.title}</strong>
            <span>{c.text}</span>
          </Link>
        ))}
      </div>
      {DOC_GROUPS.map((g) => (
        <section key={g.name} className="docs-home-group">
          <h2>
            <GroupIcon name={g.name} />
            {g.name}
          </h2>
          <div className="docs-cards">
            {g.pages.map((p) => (
              <Link key={p.slug} to={`/docs/${p.slug}`} className="docs-card">
                <strong>{p.title}</strong>
                <span>{p.summary}</span>
              </Link>
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

export function DocsPage() {
  const { slug = '' } = useParams();
  const location = useLocation();
  const [query, setQuery] = useState('');
  const [menu, setMenu] = useState(false);
  const article = useRef<HTMLElement>(null);
  const progress = useRef<HTMLDivElement>(null);
  const home = slug === '';
  const index = DOCS.findIndex((d) => d.slug === slug);
  const doc = DOCS[index];
  const body = doc && docBody(doc.slug);
  const outline = useMemo(
    () =>
      (body ?? '')
        .split('\n')
        .filter((l) => /^##\s/.test(l))
        .map((l) => plain(l.replace(/^##\s+/, ''))),
    [body],
  );
  const searching = !!query.trim();
  const inView = useSection(searching ? [] : outline.map(anchor), `${slug}:${searching}`);

  // A link to a section lands on it; a new page starts at its top.
  useEffect(() => {
    if (searching) return;
    const id = hashId(location.hash);
    const target = id ? document.getElementById(id) : null;
    if (target) target.scrollIntoView?.({ block: 'start' });
    else (document.scrollingElement ?? document.documentElement).scrollTo?.({ top: 0 });
  }, [location.pathname, location.hash, searching]);

  // Results start at their top, wherever the page was scrolled to when the search began.
  useEffect(() => {
    if (searching) (document.scrollingElement ?? document.documentElement).scrollTo?.({ top: 0 });
  }, [searching]);
  useReadingProgress(article, progress, `${slug}:${searching}`);

  const pick = () => {
    setQuery('');
    setMenu(false);
  };
  const current = inView || hashId(location.hash);

  return (
    <main className={`docs ${home && !searching ? 'is-home' : ''}`}>
      <aside className={`docs-side ${menu ? 'open' : ''}`}>
        <SearchBox query={query} setQuery={setQuery} onPick={pick} />
        <button type="button" className="btn-quiet docs-menu-toggle" aria-expanded={menu} onClick={() => setMenu((m) => !m)}>
          {menu ? 'Hide contents' : `Contents · ${doc?.title ?? 'Docs home'}`}
        </button>
        <Nav current={slug} onPick={pick} />
      </aside>

      <article className="docs-main" ref={article}>
        {searching ? (
          <Results q={query.trim()} onPick={pick} />
        ) : home ? (
          <DocsHome query={query} setQuery={setQuery} onPick={pick} />
        ) : !doc || !body ? (
          <div className="docs-missing">
            <h1>No such page</h1>
            <p>
              <Link to="/docs">Back to the docs home</Link>, or search the docs.
            </p>
          </div>
        ) : (
          <>
            <div className="docs-progress" ref={progress} aria-hidden="true" />
            <div className="docs-meta">
              <span className="docs-eyebrow">
                <GroupIcon name={doc.group} />
                {doc.group}
              </span>
              <span>{readingTime(body)} min read</span>
              {outline.length > 1 && <OnThisPage outline={outline} current={current} />}
            </div>
            <Markdown source={body} />
            <nav className="docs-pager" aria-label="Previous and next page">
              {index > 0 ? (
                <Link to={`/docs/${DOCS[index - 1].slug}`} className="docs-pager-prev">
                  <span>← Previous</span>
                  {DOCS[index - 1].title}
                </Link>
              ) : (
                <Link to="/docs" className="docs-pager-prev">
                  <span>← Back to</span>
                  Docs home
                </Link>
              )}
              {index < DOCS.length - 1 && (
                <Link to={`/docs/${DOCS[index + 1].slug}`} className="docs-pager-next">
                  <span>Next →</span>
                  {DOCS[index + 1].title}
                </Link>
              )}
            </nav>
          </>
        )}
      </article>

      {!searching && !home && outline.length > 1 && (
        <aside className="docs-outline" aria-label="On this page">
          <span className="docs-nav-label">On this page</span>
          {outline.map((h) => (
            <a key={h} href={`#${anchor(h)}`} className={current === anchor(h) ? 'active' : ''}>
              {h}
            </a>
          ))}
        </aside>
      )}
    </main>
  );
}
