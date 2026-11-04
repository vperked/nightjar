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

// scopeCSS limits CSS to what is inside root, for the designer's preview. Rules for html, body
// and :root apply to the preview's frame instead, as they do to the page.
export function scopeCSS(css: string, root: string): string {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, '');
  const hoisted: string[] = [];
  const kept: string[] = [];
  let depth = 0;
  let start = 0;
  let quote = '';
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (quote) {
      if (c === '\\') i++;
      else if (c === quote) quote = '';
      continue;
    }
    if (c === '"' || c === "'") quote = c;
    else if (c === '{') depth++;
    else if (c === '}' || (c === ';' && depth === 0)) {
      if (c === '}') depth = Math.max(0, depth - 1);
      if (depth === 0) {
        const rule = text.slice(start, i + 1).trim();
        if (rule && rule !== ';') (HOIST.test(rule) ? hoisted : kept).push(rule);
        start = i + 1;
      }
    }
  }
  const rest = text.slice(start).trim();
  if (rest) kept.push(rest);
  const body = kept
    .join('\n')
    .replace(/(^|[\s,{}>+~(])(?:html|body|:root)(?=[\s,.:#[{>+~)]|$)/g, '$1:scope')
    .replace(/:scope(\s+:scope)+/g, ':scope');
  return `${hoisted.join('\n')}\n@scope (${root}) {\n${body}\n}`;
}
