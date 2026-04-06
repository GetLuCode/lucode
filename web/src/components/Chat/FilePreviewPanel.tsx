import { lazy, Suspense, useEffect, useRef, useState } from 'react'
import { useStore } from '../../store'
import { FilePath } from '../Hook/HookToolInput'

// Lazy-load Monaco + local setup together (avoids CDN, loads only on first open)
const MonacoDiffEditor = lazy(async () => {
  await import('../../lib/monacoSetup')
  const { DiffEditor } = await import('@monaco-editor/react')
  return { default: DiffEditor }
})
const MonacoEditor = lazy(async () => {
  await import('../../lib/monacoSetup')
  const { Editor } = await import('@monaco-editor/react')
  return { default: Editor }
})

const MONACO_THEME = 'vs-dark'

// Wrapper that resets the DiffEditor model before unmount, preventing:
// "TextModel got disposed before DiffEditorWidget model got reset"
function SafeDiffEditor({ lang, original, modified }: { lang: string; original: string; modified: string }) {
  const editorRef = useRef<{ setModel: (m: null) => void } | null>(null)

  useEffect(() => {
    return () => {
      editorRef.current?.setModel(null)
    }
  }, [])

  return (
    <MonacoDiffEditor
      height="100%"
      language={lang}
      original={original}
      modified={modified}
      theme={MONACO_THEME}
      onMount={(editor) => { editorRef.current = editor }}
      options={{
        ...BASE_OPTIONS,
        renderSideBySide: true,
        enableSplitViewResizing: true,
        originalEditable: false,
        ignoreTrimWhitespace: false,
      }}
    />
  )
}

const BASE_OPTIONS = {
  readOnly: true,
  minimap: { enabled: false },
  scrollBeyondLastLine: false,
  folding: true,
  lineNumbers: 'on' as const,
  renderLineHighlight: 'line' as const,
  scrollbar: { vertical: 'auto' as const, horizontal: 'auto' as const },
  fontSize: 13,
  fontFamily: 'var(--font-mono, monospace)',
  padding: { top: 10, bottom: 10 },
  wordWrap: 'off' as const,
}

export function FilePreviewPanel() {
  const preview = useStore(s => s.filePreview)
  const closeFilePreview = useStore(s => s.closeFilePreview)
  const panelRef = useRef<HTMLDivElement>(null)
  const [width, setWidth] = useState(520)
  const isDragging = useRef(false)
  const dragStart = useRef(0)
  const widthAtDrag = useRef(0)

  // Close on Escape
  useEffect(() => {
    if (!preview) return
    const handler = (e: KeyboardEvent) => { if (e.key === 'Escape') closeFilePreview() }
    window.addEventListener('keydown', handler)
    return () => window.removeEventListener('keydown', handler)
  }, [preview, closeFilePreview])

  // Resize drag handle
  const onMouseDown = (e: React.MouseEvent) => {
    e.preventDefault()
    isDragging.current = true
    dragStart.current = e.clientX
    widthAtDrag.current = width

    const onMove = (ev: MouseEvent) => {
      if (!isDragging.current) return
      const delta = dragStart.current - ev.clientX
      setWidth(Math.max(320, Math.min(1100, widthAtDrag.current + delta)))
    }
    const onUp = () => {
      isDragging.current = false
      window.removeEventListener('mousemove', onMove)
      window.removeEventListener('mouseup', onUp)
    }
    window.addEventListener('mousemove', onMove)
    window.addEventListener('mouseup', onUp)
  }

  if (!preview) return null
  const display = preview

  return (
    <div className="file-preview" style={{ width }} ref={panelRef}>
      {/* Drag handle on the left edge */}
      <div className="file-preview__drag-handle" onMouseDown={onMouseDown} />

      <div className="file-preview__header">
        <FilePath path={display.filePath} />
        <div className="file-preview__header-right">
          <span className="file-preview__mode-badge">
            {display.mode === 'diff' ? 'modifications' : 'content'}
          </span>
          <button
            className="file-preview__close"
            onClick={closeFilePreview}
            aria-label="Close preview"
          >
            ✕
          </button>
        </div>
      </div>

      <div className="file-preview__body">
        <Suspense fallback={<div className="file-preview__loading">Loading editor…</div>}>
          {display.mode === 'diff' ? (
            <SafeDiffEditor
              lang={display.lang}
              original={display.original ?? ''}
              modified={display.modified ?? ''}
            />
          ) : (
            <MonacoEditor
              height="100%"
              language={display.lang}
              value={display.content ?? ''}
              theme={MONACO_THEME}
              options={BASE_OPTIONS}
            />
          )}
        </Suspense>
      </div>
    </div>
  )
}
