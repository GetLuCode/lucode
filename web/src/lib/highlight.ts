import hljs from 'highlight.js/lib/core'
import bash from 'highlight.js/lib/languages/bash'
import c from 'highlight.js/lib/languages/c'
import cpp from 'highlight.js/lib/languages/cpp'
import csharp from 'highlight.js/lib/languages/csharp'
import css from 'highlight.js/lib/languages/css'
import diff from 'highlight.js/lib/languages/diff'
import dockerfile from 'highlight.js/lib/languages/dockerfile'
import go from 'highlight.js/lib/languages/go'
import ini from 'highlight.js/lib/languages/ini'
import java from 'highlight.js/lib/languages/java'
import javascript from 'highlight.js/lib/languages/javascript'
import json from 'highlight.js/lib/languages/json'
import kotlin from 'highlight.js/lib/languages/kotlin'
import markdown from 'highlight.js/lib/languages/markdown'
import php from 'highlight.js/lib/languages/php'
import powershell from 'highlight.js/lib/languages/powershell'
import python from 'highlight.js/lib/languages/python'
import ruby from 'highlight.js/lib/languages/ruby'
import rust from 'highlight.js/lib/languages/rust'
import sql from 'highlight.js/lib/languages/sql'
import swift from 'highlight.js/lib/languages/swift'
import typescript from 'highlight.js/lib/languages/typescript'
import xml from 'highlight.js/lib/languages/xml'
import yaml from 'highlight.js/lib/languages/yaml'

// Only the languages people paste into a chat, so the bundle stays small. highlight.js
// knows the usual aliases (js, py, sh, html, yml...) of each one.
const LANGUAGES = { bash, c, cpp, csharp, css, diff, dockerfile, go, ini, java, javascript, json, kotlin, markdown, php, powershell, python, ruby, rust, sql, swift, typescript, xml, yaml }
for (const [name, def] of Object.entries(LANGUAGES)) hljs.registerLanguage(name, def)
// a few names models like to use that highlight.js has no alias for
hljs.registerAliases(['shell', 'zsh', 'console', 'sh-session'], { languageName: 'bash' })
hljs.registerAliases(['ps1', 'pwsh'], { languageName: 'powershell' })
hljs.registerAliases(['jsx'], { languageName: 'javascript' })
hljs.registerAliases(['tsx'], { languageName: 'typescript' })
hljs.registerAliases(['toml'], { languageName: 'ini' })
hljs.registerAliases(['html', 'svg', 'vue'], { languageName: 'xml' })
hljs.registerAliases(['golang'], { languageName: 'go' })
hljs.registerAliases(['patch'], { languageName: 'diff' })

const escapeHtml = (s: string) =>
  s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;')

// HTML of a code block with its tokens wrapped in <span class="hljs-…">. Anything it cannot
// highlight (no language, an unknown one, or an error) comes back plain but escaped.
export function highlightCode(code: string, lang?: string): { html: string; language: string } {
  const name = (lang ?? '').trim().split(/\s+/)[0].toLowerCase()
  if (name && hljs.getLanguage(name)) {
    try {
      const r = hljs.highlight(code, { language: name, ignoreIllegals: true })
      return { html: r.value, language: r.language ?? name }
    } catch { /* fall through to plain */ }
  }
  return { html: escapeHtml(code), language: '' }
}
