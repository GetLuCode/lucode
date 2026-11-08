import DOMPurify from 'dompurify'
import { makeSanitizers } from './htmlPolicy'

// The last gate before markdown turned into HTML reaches the page, which holds the auth token. A
// chat answer can come from a plugin's model, so every answer passes here, whoever wrote it. The
// rules are in htmlPolicy.ts.
export const { sanitizeHtml, sanitizeUntrustedHtml } = makeSanitizers(DOMPurify(window) as never, document)
