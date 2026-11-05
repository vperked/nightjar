// The status page owner's own HTML and CSS. The HTML is cleaned before it is shown: scripts,
// frames, forms and event handlers are taken out, and links may only go to web, mail or page
// addresses. The CSS styles the public page; in the designer it is scoped to the preview so it
// never touches the dashboard.

// Elements kept as they are. Anything else not dropped below keeps its content but loses its tag.
const KEEP = new Set(
  (
    'a abbr address article aside b bdi bdo blockquote br caption cite code col colgroup dd del details dfn div dl dt em figcaption figure footer ' +
    'h1 h2 h3 h4 h5 h6 header hr i img ins kbd li main mark nav ol p picture pre q s samp section small source span strong sub summary sup ' +
    'table tbody td tfoot th thead time tr u ul var wbr'
  ).split(' '),
);

// Elements dropped with everything in them.
const DROP = new Set(
  'script style iframe frame frameset object embed applet form input button textarea select option template noscript link meta base svg math audio video canvas dialog portal'.split(' '),
);

const ATTRS = new Set('class id title alt width height loading decoding target rel style role lang dir colspan rowspan open datetime cite media sizes srcset type'.split(' '));

// safeURL allows web, mail and phone links, addresses on this site and, for images, data images.
function safeURL(value: string, image: boolean): boolean {
  const v = value.trim().replace(/[\u0000-\u001f\s]+/g, '');
  if (!/^[a-z][a-z0-9+.-]*:/i.test(v)) return true; // relative, #anchor or //host
  if (/^(https?|mailto|tel):/i.test(v)) return true;
  return image && /^data:image\/(png|gif|jpe?g|webp|avif);/i.test(v);
}

function clean(node: Element) {
  for (const child of [...node.children]) {
    const tag = child.tagName.toLowerCase();
    if (DROP.has(tag)) {
      child.remove();
      continue;
    }
    clean(child);
    if (!KEEP.has(tag)) {
      child.replaceWith(...child.childNodes);
      continue;
    }
    for (const { name, value } of [...child.attributes]) {
      const n = name.toLowerCase();
      const url = n === 'href' || n === 'src';
      const ok =
        (ATTRS.has(n) || n.startsWith('aria-') || n.startsWith('data-') || url) &&
        !(url && !safeURL(value, n === 'src')) &&
        !(n === 'srcset' && value.split(',').some((part) => !safeURL(part.trim().split(/\s+/)[0] ?? '', true))) &&
        !(n === 'style' && /expression\(|javascript:|url\(\s*['"]?\s*javascript:/i.test(value));
      if (!ok) child.removeAttribute(name);
    }
    // Links that open a new tab never hand the page to the site they open.
    if (tag === 'a' && child.getAttribute('target')) child.setAttribute('rel', 'noopener noreferrer');
  }
  // Comments go too: nothing in them is shown, and they can hide markup from a reader.
  for (const n of [...node.childNodes]) if (n.nodeType === 8) n.remove();
}

// sanitizeHTML is the owner's HTML with everything that could run code taken out.
export function sanitizeHTML(html: string): string {
  if (!html.trim()) return '';
  const doc = new DOMParser().parseFromString(`<!doctype html><body>${html}`, 'text/html');
  clean(doc.body);
  return doc.body.innerHTML;
}

// Top-level at-rules that cannot sit inside @scope: they stay global.
const HOIST = /^@(import|charset|namespace|font-face|keyframes|-webkit-keyframes|property|counter-style|font-feature-values|font-palette-values)\b/i;

// Of those, the ones that would reach the whole dashboard however they are written: the preview
// goes without them (an imported sheet, a registered custom property, features for a family).
const GLOBAL = /^@(import|property|font-feature-values)\b/i;

// Animations and fonts the owner names are renamed in the preview, so one called like the
// dashboard's own (pulse, IBM Plex Sans) does not replace it.
const PREVIEW = 'nj-preview-';

const reEscape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');

// topLevel splits CSS into its top-level rules the way a browser reads it: braces in strings, after
// a backslash or in url() do not count, a '}' that closes nothing is dropped, and whatever is left
// open at the end is closed.
function topLevel(css: string): string[] {
  // A comment becomes a space, so taking it out cannot join what was either side into a new one.
  const text = css.replace(/\/\*[\s\S]*?(?:\*\/|$)/g, ' ');
  const rules: string[] = [];
  let depth = 0;
  let start = 0;
  let quote = '';
  let url = false;
  // The name being read, its escapes worked out as a browser does, so u\rl( and \75 rl( are url( too.
  let name = '';
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c === '\\') {
      const next = text[i + 1] ?? '';
      const hex = quote || url ? null : /^[0-9a-f]{1,6}(?:\r\n|[ \t\n\r\f])?/i.exec(text.slice(i + 1, i + 9));
      if (hex) {
        const code = parseInt(hex[0], 16);
        name += code <= 0x10ffff ? String.fromCodePoint(code) : '\ufffd';
        i += hex[0].length;
      } else {
        // A backslash before a line break escapes nothing and ends the name.
        name = /[\n\r\f]/.test(next) ? '' : name + next;
        i++;
      }
      continue;
    }
    if (quote) {
      // A string ends at its quote or, left open, at the end of the line.
      if (c === quote || c === '\n' || c === '\r' || c === '\f') quote = '';
      continue;
    }
    if (url) {
      if (c === ')') url = false;
      continue;
    }
    if (/[-\w\u0080-\uffff]/.test(c)) {
      name += c;
      continue;
    }
    const read = name;
    name = '';
    if (c === '"' || c === "'") quote = c;
    else if (c === '(' && read.toLowerCase() === 'url') {
      // url( without quotes runs to its ')' whatever is in it.
      url = !/^\s*["']/.test(text.slice(i + 1));
    } else if (c === '{') depth++;
    else if (c === '}' && depth === 0) {
      // Closes nothing: dropped, with whatever came before it, so it cannot close the preview's scope.
      start = i + 1;
    } else if (c === '}' || (c === ';' && depth === 0)) {
      if (c === '}') depth--;
      if (depth === 0) {
        const rule = text.slice(start, i + 1).trim();
        if (rule && rule !== ';') rules.push(rule);
        start = i + 1;
      }
    }
  }
  let rest = text.slice(start).trim();
  if (rest) {
    // A lone backslash at the very end would escape what closes the rule.
    if (/(^|[^\\])(\\\\)*\\$/.test(rest)) rest = rest.slice(0, -1);
    rules.push(rest + quote + (url ? ')' : '') + (depth ? '\n' + '}'.repeat(depth) : ''));
  }
  return rules;
}

// scopeCSS limits CSS to what is inside root, for the designer's preview. Rules for html, body
// and :root apply to the preview's frame instead, as they do to the page.
export function scopeCSS(css: string, root: string): string {
  const hoisted: string[] = [];
  const kept: string[] = [];
  const animations = new Set<string>();
  const fonts = new Set<string>();
  for (const rule of topLevel(css)) {
    // An at-rule whose name is written with escapes could be one that has to be hoisted or renamed.
    if (/^@[-\w\u0080-\uffff]*\\/.test(rule)) continue;
    if (!HOIST.test(rule)) kept.push(rule);
    else if (GLOBAL.test(rule)) continue;
    else if (/^@(-webkit-)?keyframes\b/i.test(rule)) {
      // Renamed only when the name is plainly written; otherwise the preview goes without it.
      const m = /^(@(?:-webkit-)?keyframes)(?:\s*(["'])([-\w\u0080-\uffff]+)\2|(\s+)([-\w\u0080-\uffff]+))(?=\s*\{)/i.exec(rule);
      if (!m) continue;
      const name = m[3] ?? m[5];
      animations.add(name);
      hoisted.push(m[1] + (m[2] ? ` "${PREVIEW}${name}"` : m[4] + PREVIEW + name) + rule.slice(m[0].length));
    } else if (/^@font-face\b/i.test(rule)) {
      // The same for a font: one font-family, a plain name, nothing escaped. Its name can then hold
      // nothing that would open or close a string or a block once it is quoted.
      const m = /([{;]\s*font-family\s*:\s*)("[^"'\\\n\r\f{}()[\];]*"|'[^"'\\\n\r\f{}()[\];]*'|[-\w\u0080-\uffff]+(?:\s+[-\w\u0080-\uffff]+)*)(?=\s*(?:[;}]|$))/i.exec(rule);
      if (!m || rule.includes('\\') || rule.match(/font-family/gi)?.length !== 1) continue;
      const name = m[2]
        .replace(/^(["'])(.*)\1$/, '$2')
        .trim()
        .replace(/\s+/g, ' ');
      if (!name) continue;
      fonts.add(name);
      hoisted.push(rule.slice(0, m.index) + m[1] + `"${PREVIEW}${name}"` + rule.slice(m.index + m[0].length));
    } else hoisted.push(rule);
  }
  let body = kept
    .join('\n')
    .replace(/(^|[\s,{}>+~(])(?:html|body|:root)(?=[\s,.:#[{>+~)]|$)/g, '$1:scope')
    .replace(/:scope(\s+:scope)+/g, ':scope');
  if (animations.size)
    body = body.replace(
      /((?:^|[\s;{])(?:-webkit-)?animation(?:-name)?\s*:)([^;}]*)/gi,
      (_, prop: string, value: string) => prop + value.replace(/(["']?)([-\w]+)\1/g, (word: string, _q: string, name: string) => (animations.has(name) ? PREVIEW + name : word)),
    );
  for (const name of fonts) {
    const words = name.split(/\s+/).map(reEscape).join('\\s+');
    const quoted = new RegExp(`(["'])\\s*${words}\\s*\\1`, 'gi');
    const bare = new RegExp(`(^|[\\s,(])${words}(?=\\s*(?:[,)!]|$))`, 'gi');
    const renamed = `"${PREVIEW}${name}"`;
    body = body.replace(/((?:^|[\s;{])(?:font|font-family|--[-\w]+)\s*:)([^;}]*)/gi, (_, prop: string, value: string) => prop + value.replace(quoted, renamed).replace(bare, `$1${renamed}`));
  }
  return `${hoisted.join('\n')}\n@scope (${root}) {\n${body}\n}`;
}
