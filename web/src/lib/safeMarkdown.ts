import { Marked, type Tokens } from 'marked'
import { safeHttpUrl } from './plugins.ts'

// Markdown written by a plugin, drawn on its page. marked passes raw HTML straight through and
// keeps any link scheme, so this instance turns raw HTML into visible text, makes only http and
// https into links, and shows an image as a link instead of fetching it. The result still goes
// through sanitizeHtml.ts before it reaches the page.

const escapeHtml = (s: string) =>
  s.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;').replace(/"/g, '&quot;').replace(/'/g, '&#39;')

const anchor = (href: string, inner: string) => `<a href="${escapeHtml(href)}" target="_blank" rel="noreferrer noopener">${inner}</a>`

const strict = new Marked({
  gfm: true,
  breaks: false,
  renderer: {
    html({ text }: Tokens.HTML | Tokens.Tag) {
      return escapeHtml(text)
    },
    link({ href, tokens }: Tokens.Link) {
      const inner = this.parser.parseInline(tokens)
      const safe = safeHttpUrl(href)
      return safe ? anchor(safe, inner) : inner
    },
    image({ href, text }: Tokens.Image) {
      const safe = safeHttpUrl(href)
      return safe ? anchor(safe, escapeHtml(text || safe)) : escapeHtml(text)
    },
    code({ text }: Tokens.Code) {
      return `<pre><code>${escapeHtml(text)}</code></pre>`
    },
  },
})

export function pluginMarkdownToHtml(source: string): string {
  return strict.parse(source, { async: false }) as string
}
