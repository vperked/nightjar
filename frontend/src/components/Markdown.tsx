import { Fragment, useRef, useState, type ReactNode } from 'react';
import { Link } from 'react-router';

// Markdown renders the small Markdown the docs are written in: headings, paragraphs, bullet and
// numbered lists (with indented blocks inside items), tables, fenced code, quotes, and inline
// code, bold, italics, links and <kbd> keys. It builds React elements only, never HTML, so nothing in a
// document can run as markup.

// italic is _a phrase_ between underscores, but not snake_case inside a word.
const italic = /(?<![\w\\])_(?=\S)([^_\n]*?\S)_(?!\w)/g;

// slug turns a heading into its anchor: "When it won't connect" → "when-it-won-t-connect".
export const slug = (text: string) =>
  text
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, '-')
    .replace(/^-|-$/g, '');

// plain is a line's text without Markdown, for search and anchors.
export const plain = (text: string) =>
  text
    .replace(/<kbd>([^<]*)<\/kbd>/g, '$1')
    .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
    .replace(/\*\*([^*]+)\*\*/g, '$1')
    .replace(italic, '$1')
    .replace(/`([^`]+)`/g, '$1');

type Block =
  | { kind: 'heading'; level: 1 | 2 | 3; text: string }
  | { kind: 'para'; text: string }
  | { kind: 'code'; lang: string; text: string }
  | { kind: 'list'; ordered: boolean; items: Block[][] }
  | { kind: 'table'; head: string[]; rows: string[][] }
  | { kind: 'quote'; blocks: Block[] };

const cells = (line: string) =>
  line
    .trim()
    .replace(/^\||\|$/g, '')
    .split('|')
    .map((c) => c.trim());

const listItem = /^(\s*)([-*]|\d+\.)\s+(.*)$/;

// parse splits a document into blocks.
export function parse(source: string): Block[] {
  const lines = source.replace(/\r\n/g, '\n').split('\n');
  const blocks: Block[] = [];
  let i = 0;
  while (i < lines.length) {
    const line = lines[i];
    if (!line.trim()) {
      i++;
      continue;
    }
    const fence = line.match(/^\s*```(\w*)/);
    if (fence) {
      const indent = line.match(/^\s*/)![0].length;
      const body: string[] = [];
      i++;
      while (i < lines.length && !/^\s*```/.test(lines[i])) body.push(lines[i++].slice(indent));
      i++;
      blocks.push({ kind: 'code', lang: fence[1], text: body.join('\n') });
      continue;
    }
    const heading = line.match(/^(#{1,3})\s+(.*)$/);
    if (heading) {
      blocks.push({ kind: 'heading', level: heading[1].length as 1 | 2 | 3, text: heading[2].trim() });
      i++;
      continue;
    }
    if (line.trim().startsWith('|') && i + 1 < lines.length && /^\s*\|?\s*:?-{3,}/.test(lines[i + 1])) {
      const head = cells(line);
      const rows: string[][] = [];
      i += 2;
      while (i < lines.length && lines[i].trim().startsWith('|')) rows.push(cells(lines[i++]));
      blocks.push({ kind: 'table', head, rows });
      continue;
    }
    if (line.startsWith('>')) {
      const body: string[] = [];
      while (i < lines.length && lines[i].startsWith('>')) body.push(lines[i++].replace(/^>\s?/, ''));
      blocks.push({ kind: 'quote', blocks: parse(body.join('\n')) });
      continue;
    }
    const item = line.match(listItem);
    if (item && item[1].length === 0) {
      const ordered = /\d/.test(item[2]);
      const items: Block[][] = [];
      while (i < lines.length) {
        const m = lines[i].match(listItem);
        if (!m || m[1].length > 0 || /\d/.test(m[2]) !== ordered) break;
        // The item's own text, then any lines indented under it (or blank lines followed by indented ones).
        const body = [m[3]];
        i++;
        while (i < lines.length && (/^\s{2,}\S/.test(lines[i]) || (!lines[i].trim() && i + 1 < lines.length && /^\s{2,}\S/.test(lines[i + 1])))) {
          body.push(lines[i].replace(/^\s{2,3}/, ''));
          i++;
        }
        items.push(parse(body.join('\n')));
        // A blank line between items keeps the list going.
        if (i < lines.length && !lines[i].trim() && i + 1 < lines.length && listItem.test(lines[i + 1]) && !lines[i + 1].match(listItem)![1]) i++;
      }
      blocks.push({ kind: 'list', ordered, items });
      continue;
    }
    const para: string[] = [];
    while (i < lines.length && lines[i].trim() && !/^(#{1,3}\s|\s*```|>|\|)/.test(lines[i]) && !(listItem.test(lines[i]) && !lines[i].match(listItem)![1])) para.push(lines[i++].trim());
    blocks.push({ kind: 'para', text: para.join(' ') });
  }
  return blocks;
}

const token = /(`[^`]+`)|(\*\*(?:[^*]|\*(?!\*))+\*\*)|(\[[^\]]+\]\([^)\s]+\))|(<kbd>[^<]+<\/kbd>)|((?<![\w\\])_(?=\S)[^_\n]*?\S_(?!\w))/g;

// DocLink opens dashboard pages and other docs in the app, and the web in a new tab.
function DocLink({ href, children }: { href: string; children: ReactNode }) {
  if (href.startsWith('/')) return <Link to={href}>{children}</Link>;
  if (href.startsWith('#')) return <a href={href}>{children}</a>;
  if (/^https?:\/\//.test(href))
    return (
      <a href={href} target="_blank" rel="noreferrer">
        {children}
      </a>
    );
  return <>{children}</>; // anything else (javascript: and so on) is not a link
}

// Inline renders a line's code, bold, links and keys.
export function Inline({ text }: { text: string }) {
  const out: ReactNode[] = [];
  let last = 0;
  for (const m of text.matchAll(token)) {
    if (m.index > last) out.push(text.slice(last, m.index));
    const [whole, code, bold, link, kbd, em] = m;
    const key = m.index;
    if (code) out.push(<code key={key}>{code.slice(1, -1)}</code>);
    else if (bold)
      out.push(
        <strong key={key}>
          <Inline text={bold.slice(2, -2)} />
        </strong>,
      );
    else if (link) {
      const [, label, href] = link.match(/^\[([^\]]+)\]\(([^)]+)\)$/)!;
      out.push(
        <DocLink key={key} href={href}>
          <Inline text={label} />
        </DocLink>,
      );
    } else if (kbd) out.push(<kbd key={key}>{kbd.slice(5, -6)}</kbd>);
    else if (em)
      out.push(
        <em key={key}>
          <Inline text={em.slice(1, -1)} />
        </em>,
      );
    last = m.index + whole.length;
  }
  if (last < text.length) out.push(text.slice(last));
  return <>{out}</>;
}

// ---------- code ----------

// A light touch of colour for shell and config: comments, strings, flags, variables, and the
// command at the start of a line. Everything stays text; nothing is interpreted.
const codeToken = /(#.*$)|("(?:[^"\\]|\\.)*"|'[^']*')|((?<=\s)--?[A-Za-z][\w-]*(?:=\S*)?)|(\$\{?[A-Za-z_]\w*\}?)/g;

function CodeLine({ line, lang }: { line: string; lang: string }) {
  const out: ReactNode[] = [];
  let last = 0;
  // The command a shell line runs (after sudo, ssh -t …) reads first.
  const cmd = lang === 'bash' || lang === 'sh' ? line.match(/^(\s*)((?:sudo\s+)?[\w./-]+)/) : lang === 'nginx' ? line.match(/^(\s*)([a-z_]+)/) : null;
  if (cmd && !cmd[2].startsWith('#')) {
    out.push(
      cmd[1],
      <span key="c" className="tok-cmd">
        {cmd[2]}
      </span>,
    );
    last = cmd[0].length;
  }
  const rest = line.slice(last);
  let at = 0;
  for (const m of rest.matchAll(codeToken)) {
    if (m.index > at) out.push(rest.slice(at, m.index));
    const cls = m[1] ? 'tok-comment' : m[2] ? 'tok-string' : m[3] ? 'tok-flag' : 'tok-var';
    out.push(
      <span key={last + m.index} className={cls}>
        {m[0]}
      </span>,
    );
    at = m.index + m[0].length;
  }
  if (at < rest.length) out.push(rest.slice(at));
  return <>{out}</>;
}

const LANGS: Record<string, string> = { bash: 'Shell', sh: 'Shell', nginx: 'nginx', json: 'JSON', yaml: 'YAML', env: '.env' };

// CodeBlock is a fenced block with its language and a copy button.
export function CodeBlock({ lang, text }: { lang: string; text: string }) {
  const [copied, setCopied] = useState(false);
  const code = useRef<HTMLElement>(null);
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1600);
    } catch {
      // Plain http has no clipboard: select it, so Ctrl+C copies.
      const range = document.createRange();
      if (code.current) range.selectNodeContents(code.current);
      window.getSelection()?.removeAllRanges();
      window.getSelection()?.addRange(range);
    }
  };
  const lines = text.split('\n');
  return (
    <figure className="doc-code">
      <figcaption>
        <span className="doc-code-lang">{LANGS[lang] ?? (lang || 'Text')}</span>
        <button type="button" className={`doc-code-copy ${copied ? 'done' : ''}`} onClick={copy} aria-label={copied ? 'Copied' : 'Copy to clipboard'}>
          <svg viewBox="0 0 24 24" aria-hidden="true">
            {copied ? <path d="M5 12.5l4.2 4.2L19 7" /> : <path d="M9 9h10v11H9zM5 15V4h10" />}
          </svg>
          {copied ? 'Copied' : 'Copy'}
        </button>
      </figcaption>
      <pre>
        <code ref={code}>
          {lines.map((l, i) => (
            <Fragment key={i}>
              <CodeLine line={l} lang={lang} />
              {i < lines.length - 1 ? '\n' : ''}
            </Fragment>
          ))}
        </code>
      </pre>
    </figure>
  );
}

// ---------- notes ----------

// A quote that opens with **Tip:**, **Note:** or **Warning:** becomes that kind of note.
const NOTE = /^\*\*(Tip|Note|Warning|Important):\*\*\s*/;
const NOTE_ICON: Record<string, string> = {
  tip: 'M9 18h6M10 21h4M12 3a6 6 0 0 0-3.5 10.9c.6.5 1 1.2 1 2V16h5v-.1c0-.8.4-1.5 1-2A6 6 0 0 0 12 3z',
  note: 'M12 8v.01M11 12h1v5h1M12 3a9 9 0 1 0 0 18 9 9 0 0 0 0-18z',
  warning: 'M12 9v4M12 17v.01M10.3 3.9 2.2 18a2 2 0 0 0 1.7 3h16.2a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z',
};

function Note({ blocks }: { blocks: Block[] }) {
  const first = blocks[0];
  const m = first?.kind === 'para' ? first.text.match(NOTE) : null;
  const kind = (m?.[1] ?? 'Note').toLowerCase();
  const tone = kind === 'warning' || kind === 'important' ? 'warning' : kind === 'tip' ? 'tip' : 'note';
  const rest = m && first.kind === 'para' ? [{ ...first, text: first.text.replace(NOTE, '') }, ...blocks.slice(1)] : blocks;
  return (
    <aside className={`doc-note ${tone}`}>
      <svg viewBox="0 0 24 24" aria-hidden="true">
        <path d={NOTE_ICON[tone]} />
      </svg>
      <div className="min0">
        {m && <strong className="doc-note-label">{m[1]}</strong>}
        <Blocks blocks={rest} />
      </div>
    </aside>
  );
}

function Blocks({ blocks }: { blocks: Block[] }) {
  return (
    <>
      {blocks.map((b, i) => {
        switch (b.kind) {
          case 'heading': {
            const id = slug(plain(b.text));
            const Tag = `h${b.level}` as 'h1' | 'h2' | 'h3';
            return (
              <Tag key={i} id={b.level > 1 ? id : undefined}>
                <Inline text={b.text} />
                {b.level > 1 && (
                  <a className="doc-anchor" href={`#${id}`} aria-label={`Link to ${plain(b.text)}`}>
                    #
                  </a>
                )}
              </Tag>
            );
          }
          case 'para': {
            // The paragraph under the page title leads, a size up.
            const prev = blocks[i - 1];
            return (
              <p key={i} className={prev?.kind === 'heading' && prev.level === 1 ? 'doc-lead' : undefined}>
                <Inline text={b.text} />
              </p>
            );
          }
          case 'code':
            return <CodeBlock key={i} lang={b.lang} text={b.text} />;
          case 'quote':
            return <Note key={i} blocks={b.blocks} />;
          case 'table':
            return (
              <div key={i} className="doc-table">
                <table className="table">
                  <thead>
                    <tr>
                      {b.head.map((h, j) => (
                        <th key={j}>
                          <Inline text={h} />
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody>
                    {b.rows.map((r, j) => (
                      <tr key={j}>
                        {r.map((c, k) => (
                          <td key={k}>
                            <Inline text={c} />
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
            );
          case 'list': {
            const List = b.ordered ? 'ol' : 'ul';
            return (
              <List key={i} className={b.ordered ? 'doc-steps' : undefined}>
                {b.items.map((item, j) => (
                  <li key={j}>
                    {/* A one-paragraph item is drawn without its paragraph, so lists stay tight. */}
                    {item.length === 1 && item[0].kind === 'para' ? <Inline text={item[0].text} /> : <Blocks blocks={item} />}
                  </li>
                ))}
              </List>
            );
          }
        }
        return <Fragment key={i} />;
      })}
    </>
  );
}

export function Markdown({ source }: { source: string }) {
  return (
    <div className="doc-body">
      <Blocks blocks={parse(source)} />
    </div>
  );
}
