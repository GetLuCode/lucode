/**
 * Lazy Monaco setup — imported only when the diff/editor is first opened.
 * Bundles monaco-editor locally to avoid CDN (jsDelivr is blocked by some
 * browser tracking-prevention policies).
 */
import { loader } from '@monaco-editor/react'
import * as monaco from 'monaco-editor'

// Workers via Vite's ?worker syntax — bundled as separate Worker chunks.
// monaco-editor >= 0.57 maps "./*" to "./esm/vs/*.js", so no esm/vs/ prefix here.
import EditorWorker from 'monaco-editor/editor/editor.worker?worker'
import JsonWorker from 'monaco-editor/language/json/json.worker?worker'
import CssWorker from 'monaco-editor/language/css/css.worker?worker'
import HtmlWorker from 'monaco-editor/language/html/html.worker?worker'
import TsWorker from 'monaco-editor/language/typescript/ts.worker?worker'

// MonacoEnvironment must be set before loader initialises Monaco.
;(self as typeof self & { MonacoEnvironment?: { getWorker: (id: string, label: string) => Worker } }).MonacoEnvironment = {
  getWorker(_id: string, label: string): Worker {
    if (label === 'json')                                         return new JsonWorker()
    if (label === 'css' || label === 'scss' || label === 'less') return new CssWorker()
    if (label === 'html' || label === 'handlebars')              return new HtmlWorker()
    if (label === 'typescript' || label === 'javascript')        return new TsWorker()
    return new EditorWorker()
  },
}

// Tell @monaco-editor/react to use the locally-bundled instance instead of CDN.
loader.config({ monaco })

export { monaco }
