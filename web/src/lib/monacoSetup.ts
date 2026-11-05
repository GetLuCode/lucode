/**
 * Lazy Monaco setup — imported only when the diff/editor is first opened.
 * Bundles monaco-editor locally to avoid CDN (jsDelivr is blocked by some
 * browser tracking-prevention policies).
 */
import { loader } from '@monaco-editor/react'
import * as monaco from 'monaco-editor'

// Workers via Parcel's `new Worker(new URL(...))` syntax — bundled as separate Worker chunks.
const editorWorker = () => new Worker(new URL('../../node_modules/monaco-editor/esm/vs/editor/editor.worker.js', import.meta.url), { type: 'module' })
const jsonWorker = () => new Worker(new URL('../../node_modules/monaco-editor/esm/vs/language/json/json.worker.js', import.meta.url), { type: 'module' })
const cssWorker = () => new Worker(new URL('../../node_modules/monaco-editor/esm/vs/language/css/css.worker.js', import.meta.url), { type: 'module' })
const htmlWorker = () => new Worker(new URL('../../node_modules/monaco-editor/esm/vs/language/html/html.worker.js', import.meta.url), { type: 'module' })
const tsWorker = () => new Worker(new URL('../../node_modules/monaco-editor/esm/vs/language/typescript/ts.worker.js', import.meta.url), { type: 'module' })

// MonacoEnvironment must be set before loader initialises Monaco.
;(self as typeof self & { MonacoEnvironment?: { getWorker: (id: string, label: string) => Worker } }).MonacoEnvironment = {
  getWorker(_id: string, label: string): Worker {
    if (label === 'json')                                         return jsonWorker()
    if (label === 'css' || label === 'scss' || label === 'less') return cssWorker()
    if (label === 'html' || label === 'handlebars')              return htmlWorker()
    if (label === 'typescript' || label === 'javascript')        return tsWorker()
    return editorWorker()
  },
}

// Tell @monaco-editor/react to use the locally-bundled instance instead of CDN.
loader.config({ monaco })

export { monaco }
