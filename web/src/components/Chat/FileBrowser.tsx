import { useCallback, useEffect, useState } from 'react'
import { useStore } from '../../store'
import { listFiles, readFile } from '../../api'
import type { FileEntry } from '../../api'
import { IconFolder, IconFile, IconChevronUp, IconFolderOpen, IconAlertCircle, IconRefresh } from '@tabler/icons-react'

// Tiny language detector — kept in sync with HookToolInput.detectLanguage.
function detectLanguage(filePath: string): string {
  const ext = filePath.split('.').pop()?.toLowerCase() ?? ''
  const map: Record<string, string> = {
    ts: 'typescript', tsx: 'typescript',
    js: 'javascript', jsx: 'javascript',
    go: 'go', py: 'python', rs: 'rust',
    css: 'css', scss: 'css', html: 'html',
    json: 'json', md: 'markdown',
    sh: 'shell', bash: 'shell',
    yaml: 'yaml', yml: 'yaml',
    toml: 'ini', xml: 'xml', sql: 'sql',
    c: 'c', cpp: 'cpp', h: 'cpp',
  }
  return map[ext] ?? 'plaintext'
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(1)} GB`
}

export function FileBrowser() {
  const activeProject = useStore(s =>
    s.projects.find(p => p.id === s.activeProjectId) ?? null
  )
  const workDir = activeProject?.work_dirs?.[0] ?? null
  const openFilePreview = useStore(s => s.openFilePreview)
  const closeFileBrowser = useStore(s => s.closeFileBrowser)

  const [currentPath, setCurrentPath] = useState<string>('')
  const [parent, setParent] = useState<string>('')
  const [entries, setEntries] = useState<FileEntry[]>([])
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState<string | null>(null)

  // When the project changes, reset to the workDir root.
  useEffect(() => {
    if (workDir) setCurrentPath(workDir)
  }, [workDir])

  const load = useCallback(async (path: string) => {
    if (!activeProject) return
    setLoading(true)
    setError(null)
    try {
      const resp = await listFiles(path, activeProject.id)
      setCurrentPath(resp.path)
      setParent(resp.parent)
      setEntries(resp.entries)
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    } finally {
      setLoading(false)
    }
  }, [activeProject])

  useEffect(() => {
    if (currentPath && activeProject) {
      load(currentPath)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [currentPath, activeProject?.id])

  const handleOpenFile = async (entry: FileEntry) => {
    if (!activeProject) return
    try {
      const resp = await readFile(entry.path, activeProject.id)
      openFilePreview({
        filePath: entry.path,
        lang: detectLanguage(entry.path),
        mode: 'content',
        content: resp.content,
      })
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
    }
  }

  if (!activeProject) {
    return (
      <div className="file-browser">
        <div className="file-browser__header">
          <span className="file-browser__title"><IconFolderOpen size={14} /> File browser</span>
        </div>
        <div className="file-browser__empty">Select a project to browse its files.</div>
      </div>
    )
  }

  if (!workDir) {
    return (
      <div className="file-browser">
        <div className="file-browser__header">
          <span className="file-browser__title"><IconFolderOpen size={14} /> File browser</span>
          <button
            className="file-browser__close"
            onClick={closeFileBrowser}
            aria-label="Close file browser"
            title="Close"
          >✕</button>
        </div>
        <div className="file-browser__empty">
          This project has no working directory. Add one in project settings to browse files.
        </div>
      </div>
    )
  }

  // Dirs first, then files, alphabetical inside each group.
  const sorted = [...entries].sort((a, b) => {
    if (a.isDir !== b.isDir) return a.isDir ? -1 : 1
    return a.name.localeCompare(b.name)
  })

  return (
    <div className="file-browser">
      <div className="file-browser__header">
        <span className="file-browser__title">
          <IconFolderOpen size={14} /> File browser
        </span>
        <button
          className="file-browser__close"
          onClick={() => load(currentPath)}
          aria-label="Refresh"
          title="Refresh"
        ><IconRefresh size={13} /></button>
        <button
          className="file-browser__close"
          onClick={closeFileBrowser}
          aria-label="Close file browser"
          title="Close"
        >✕</button>
      </div>

      <div className="file-browser__path" title={currentPath}>
        {currentPath}
      </div>

      {error && (
        <div className="file-browser__error">
          <IconAlertCircle size={12} /> {error}
        </div>
      )}

      <div className="file-browser__list">
        {loading && entries.length === 0 ? (
          <div className="file-browser__empty">Loading…</div>
        ) : (
          <>
            {parent && (
              <div
                className="file-browser__row file-browser__row--up"
                onClick={() => setCurrentPath(parent)}
              >
                <span className="file-browser__icon"><IconChevronUp size={14} /></span>
                <span className="file-browser__name">..</span>
              </div>
            )}
            {sorted.length === 0 && !loading ? (
              <div className="file-browser__empty">This directory is empty.</div>
            ) : (
              sorted.map(entry => (
                <div
                  key={entry.path}
                  className="file-browser__row"
                  onClick={() => entry.isDir ? setCurrentPath(entry.path) : handleOpenFile(entry)}
                >
                  <span className={`file-browser__icon${entry.isDir ? ' file-browser__icon--dir' : ' file-browser__icon--file'}`}>
                    {entry.isDir ? <IconFolder size={14} /> : <IconFile size={14} />}
                  </span>
                  <span className="file-browser__name">{entry.name}</span>
                  {!entry.isDir && <span className="file-browser__size">{formatSize(entry.size)}</span>}
                </div>
              ))
            )}
          </>
        )}
      </div>
    </div>
  )
}
