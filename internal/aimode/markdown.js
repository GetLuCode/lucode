// Turns the last answer of Google's AI Mode into Markdown. It runs inside the page (the
// text of an answer loses its structure in innerText), so it reads what is rendered:
// headings, bold, italics, lists, code blocks, tables and links keep their form.
// The expression is a function call so Runtime.evaluate returns its value.
(() => {
  const turns = [...document.querySelectorAll('[data-scope-id="turn"]')];
  const turn = turns[turns.length - 1];
  const root = turn && turn.querySelector('[data-container-id="main-col"]');
  if (!root) return '';

  const SKIP_TAGS = new Set(['BUTTON', 'SVG', 'STYLE', 'SCRIPT', 'TEXTAREA', 'ASIDE', 'NOSCRIPT', 'H1', 'INPUT']);
  const SKIP_ROLES = new Set(['status', 'alert', 'dialog', 'region', 'progressbar', 'tooltip', 'button']);
  // notices the page adds around the answer
  const NOISE = /^(usa el código con precaución\.?|use code with caution\.?|la ia puede cometer errores.*|ai (responses )?can make mistakes.*|ai responses may include mistakes.*)$/i;
  const sources = []; // source pills ("Wikipedia +3") are gathered at the end instead of inline

  const decodeEntities = (s) => {
    const t = document.createElement('textarea');
    t.innerHTML = s;
    return t.value;
  };

  // The links of the page go through google.com/goto; the real address is in a comment.
  const urlOf = (a) => {
    for (const n of a.childNodes) {
      if (n.nodeType === 8) {
        // The page writes the quotes of that JSON as &quot; inside the comment, so both forms
        // are accepted, and the address itself may carry &amp; and the like.
        const m = /^TgQPHd\|\|\|\[\[(?:"|&quot;)(https?:.+?)(?:"|&quot;)/.exec(n.data);
        if (m) return decodeEntities(m[1]);
      }
    }
    // if the exact shape of that comment changes: any quoted address in the comments of the
    // link or of the element that holds it
    for (const holder of [a, a.parentElement]) {
      if (!holder) continue;
      for (const n of holder.childNodes) {
        if (n.nodeType !== 8) continue;
        const m = /(?:"|&quot;)(https?:\/\/.+?)(?:"|&quot;)/.exec(n.data);
        if (m) return decodeEntities(m[1]);
      }
    }
    const href = a.getAttribute('href') || '';
    return /^https?:/.test(href) && !/^https?:\/\/(www\.)?google\.[a-z.]+\/goto/.test(href) ? href : '';
  };

  const isInline = (el) => {
    const d = getComputedStyle(el).display;
    return d.startsWith('inline') || d === 'contents';
  };
  // what the page keeps in the DOM but does not show (feedback forms, dialogs)
  const hiddenEl = (el) => {
    const cs = getComputedStyle(el);
    if (cs.display === 'none' || cs.visibility === 'hidden') return true;
    return cs.display !== 'contents' && el.getClientRects().length === 0;
  };
  // the small block above a code block that names its language ("python")
  const isCodeLabel = (el) => {
    const next = el.nextElementSibling;
    return !!next && !el.querySelector('pre') && !!next.querySelector('pre') && /^[\w+#.-]{1,20}$/.test((el.innerText || '').trim());
  };
  // only what would turn into formatting; underscores stay, so URLs and snake_case survive
  const esc = (s) => s.replace(/([\\`*])/g, '\\$1');

  // text of an inline run, with the spaces kept outside the markers
  const wrap = (marker, inner) => {
    const m = /^(\s*)([\s\S]*?)(\s*)$/.exec(inner);
    return m[2] ? m[1] + marker + m[2] + marker + m[3] : inner;
  };

  const children = (node, ctx) => [...node.childNodes].map((c) => md(c, ctx)).join('');

  const listItems = (list, ordered, ctx) => {
    const out = [];
    let i = 0;
    for (const li of list.children) {
      if (li.tagName !== 'LI') continue;
      const body = children(li, ctx).replace(/^\s+/, '').replace(/\n{3,}/g, '\n\n').trimEnd();
      if (!body) continue; // only source pills or buttons
      i++;
      const marker = ordered ? i + '. ' : '- ';
      const lines = body.split('\n');
      out.push(marker + lines[0] + lines.slice(1).map((l) => (l ? '\n' + ' '.repeat(marker.length) + l : '\n')).join(''));
    }
    return '\n\n' + out.join('\n') + '\n\n';
  };

  const table = (t) => {
    const rows = [...t.querySelectorAll('tr')].map((tr) =>
      [...tr.children].filter((c) => c.tagName === 'TH' || c.tagName === 'TD').map((c) =>
        children(c, {}).replace(/\s*\n\s*/g, ' ').replace(/\|/g, '\\|').trim()));
    if (!rows.length) return '';
    const width = Math.max(...rows.map((r) => r.length));
    const line = (r) => '| ' + Array.from({ length: width }, (_, i) => r[i] || '').join(' | ') + ' |';
    return '\n\n' + [line(rows[0]), '| ' + Array(width).fill('---').join(' | ') + ' |', ...rows.slice(1).map(line)].join('\n') + '\n\n';
  };

  const codeBlock = (pre) => {
    // the language is a label in the block above the <pre>
    let lang = '';
    for (let up = pre.parentElement, depth = 0; up && depth < 3 && !lang; up = up.parentElement, depth++) {
      const prev = up.previousElementSibling;
      const label = prev && prev.innerText ? prev.innerText.trim() : '';
      if (/^[\w+#.-]{1,20}$/.test(label)) lang = label.toLowerCase();
    }
    const code = (pre.innerText || pre.textContent || '').replace(/\s+$/, '');
    return '\n\n```' + lang + '\n' + code + '\n```\n\n';
  };

  function md(node, ctx) {
    if (node.nodeType === 3) {
      const t = node.data.replace(/ /g, ' ').replace(/\s+/g, ' ');
      return NOISE.test(t.trim()) ? '' : esc(t);
    }
    if (node.nodeType !== 1) return '';
    const tag = node.tagName.toUpperCase();
    const role = node.getAttribute('role') || '';
    if (SKIP_TAGS.has(tag) || SKIP_ROLES.has(role) || node.getAttribute('aria-hidden') === 'true' || node.hidden || hiddenEl(node) || isCodeLabel(node)) {
      return '';
    }
    if (tag === 'PRE') return codeBlock(node);
    if (tag === 'UL') return listItems(node, false, ctx);
    if (tag === 'OL') return listItems(node, true, ctx);
    if (tag === 'TABLE') return table(node);
    if (tag === 'BR') return '\n';
    if (tag === 'HR') return '\n\n---\n\n';
    if (tag === 'STRONG' || tag === 'B') return wrap('**', children(node, ctx));
    if (tag === 'EM' || tag === 'I') return wrap('*', children(node, ctx));
    if (tag === 'CODE') return '`' + (node.textContent || '').replace(/`/g, "'") + '`';
    if (tag === 'A') {
      const url = urlOf(node);
      const text = children(node, ctx).replace(/\s+/g, ' ').trim();
      if (!text) return '';
      // a source pill: "Wikipedia +3" — kept for the list of sources, not in the text
      if (/\+\d+\s*$/.test(node.innerText || '') && !ctx.inText) {
        if (url) sources.push([text.replace(/\s*\+\d+$/, ''), url]);
        return '';
      }
      return url ? '[' + text + '](' + url + ')' : text;
    }
    const level = role === 'heading' ? parseInt(node.getAttribute('aria-level') || '3', 10) : (/^H[2-6]$/.test(tag) ? parseInt(tag[1], 10) : 0);
    if (level) {
      const text = children(node, ctx).replace(/\s+/g, ' ').trim();
      // Google's answers use h4/h5 for what are really sections: shown as h2/h3
      const shown = level === 4 ? 2 : level === 5 ? 3 : level;
      return text ? '\n\n' + '#'.repeat(Math.min(Math.max(shown, 1), 6)) + ' ' + text + '\n\n' : '';
    }
    const inner = children(node, ctx);
    if (isInline(node)) return inner;
    return inner.trim() ? '\n\n' + inner.trim() + '\n\n' : '';
  }

  const body = md(root, {})
    .replace(/[ \t]+\n/g, '\n')
    .replace(/\n{3,}/g, '\n\n')
    .trim();
  const seen = new Set();
  const extra = sources.filter(([, u]) => !seen.has(u) && seen.add(u));
  return extra.length ? body + '\n\nFuentes:\n' + extra.map(([t, u]) => '- [' + t + '](' + u + ')').join('\n') : body;
})()
