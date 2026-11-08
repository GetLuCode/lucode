// What may reach the page from a chat answer, which a plugin's model can write. No imports, so the
// test can run this same text with the real DOMPurify inside a real Edge (tests/sanitize.test.ts).

const FORBID_TAGS = ['style', 'form', 'input', 'button', 'textarea', 'select', 'option', 'iframe', 'object', 'embed', 'base', 'meta', 'link', 'dialog']
// What loads by itself as soon as it is drawn, besides <img>, which becomes a link.
const LOADS_BY_ITSELF_TAGS = ['video', 'audio', 'source', 'track', 'picture']
const LOADS_BY_ITSELF_ATTRS = ['background', 'poster', 'srcset', 'ping', 'lowsrc']

// Only the highlighter's classes survive, so an answer cannot wear one of the app's own styles
// (a fixed overlay, a dialog). Those carry `hljs`, `language-x`, or end in `_` like `function_`.
const HIGHLIGHT_CLASS = /^(hljs(-[\w-]+)?|language-[\w+#.-]+|[a-z]+_+)$/

export function keepHighlightClasses(value: string): string {
  return value.split(/\s+/).filter(c => HIGHLIGHT_CLASS.test(c)).join(' ')
}

interface HookData { attrName: string; attrValue: string; keepAttr: boolean }
interface Purifier {
  addHook(name: 'uponSanitizeAttribute', fn: (node: Element, data: HookData) => void): void
  sanitize(html: string, config: Record<string, unknown>): string
}

export interface Sanitizers {
  // Any chat answer.
  sanitizeHtml(html: string): string
  // An answer from a plugin's model: on top of that, nothing loads unless the person clicks it, so
  // an answer cannot carry the board out to some address in the url of an image.
  sanitizeUntrustedHtml(html: string): string
}

// `purify` must be an instance of its own (DOMPurify(window)): a hook on the shared one would reach
// the DOMPurify inside mermaid.
export function makeSanitizers(purify: Purifier, doc: Document): Sanitizers {
  purify.addHook('uponSanitizeAttribute', (_node, data) => {
    if (data.attrName !== 'class') return
    const kept = keepHighlightClasses(data.attrValue)
    if (kept) data.attrValue = kept
    else data.keepAttr = false
  })
  const base = { USE_PROFILES: { html: true }, FORBID_ATTR: ['style', 'id', 'name'] }
  const sanitizeHtml = (html: string) => purify.sanitize(html, { ...base, FORBID_TAGS })
  const sanitizeUntrustedHtml = (html: string) => {
    const clean = purify.sanitize(html, {
      ...base,
      FORBID_TAGS: [...FORBID_TAGS, ...LOADS_BY_ITSELF_TAGS],
      FORBID_ATTR: [...base.FORBID_ATTR, ...LOADS_BY_ITSELF_ATTRS],
    })
    if (!/<img/i.test(clean)) return clean
    const box = doc.createElement('template')
    box.innerHTML = clean
    for (const img of [...box.content.querySelectorAll('img')]) {
      const src = img.getAttribute('src') ?? ''
      const words = img.getAttribute('alt') || src
      if (/^https?:\/\//i.test(src)) {
        const a = doc.createElement('a')
        a.setAttribute('href', src)
        a.setAttribute('target', '_blank')
        a.setAttribute('rel', 'noreferrer noopener')
        a.textContent = words
        img.replaceWith(a)
      } else {
        img.replaceWith(doc.createTextNode(img.getAttribute('alt') ?? ''))
      }
    }
    return box.innerHTML
  }
  return { sanitizeHtml, sanitizeUntrustedHtml }
}

// Mermaid's own defaults, plus what a diagram could otherwise set to write CSS into the page.
export const MERMAID_SECURE_KEYS = ['secure', 'securityLevel', 'startOnLoad', 'maxTextSize', 'suppressErrorRendering', 'maxEdges', 'themeCSS', 'fontFamily', 'altFontFamily']

// Mermaid loads the images in a diagram while it draws, even with securityLevel strict: an <img> in
// a label, and the img: of an image shape. So a diagram from a plugin's model that could do that, or
// could set its own config, is shown as its source instead of being drawn.
const MERMAID_ACTIVE = /<\s*img|<\s*image|\bimg\s*:|\bsrc\s*=|\bhref\s*=|\burl\s*\(|@import|%%\s*\{|^\s*---|\bclick\b|fa:/im

export const mermaidIsInert = (source: string): boolean => !MERMAID_ACTIVE.test(source)
