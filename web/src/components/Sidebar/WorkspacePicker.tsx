import { IconChevronRight, IconFolder, IconX } from '@tabler/icons-react'
import { motion } from 'framer-motion'
import { useEffect, useRef, useState } from 'react'
import * as api from '../../api'
import type { AIProfile, Runner } from '../../types'

interface Props {
  isOpen: boolean
  onClose: () => void
  onConfirm: (name: string, path: string, env: { runnerId: string; aiProfileId: string }) => void
}

export function WorkspacePicker({ isOpen, onClose, onConfirm }: Props) {
  const [currentPath, setCurrentPath] = useState('')
  const [root, setRoot] = useState('')
  const [dirs, setDirs] = useState<string[]>([])
  const [name, setName] = useState('')
  const [runners, setRunners] = useState<Runner[]>([])
  const [runnerId, setRunnerId] = useState('') // '' = this instance
  const [aiProfiles, setAiProfiles] = useState<AIProfile[]>([])
  const [aiProfileId, setAiProfileId] = useState('')
  const [loading, setLoading] = useState(false)
  const [error, setError] = useState('')
  const nameInputRef = useRef<HTMLInputElement>(null)

  // Runners offered for the project (this instance first).
  useEffect(() => {
    if (!isOpen) return
    setRunnerId('')
    api.listRunners().then(setRunners).catch(() => setRunners([]))
  }, [isOpen])

  // Load the AIs and the starting directory of the chosen runner.
  useEffect(() => {
    if (!isOpen) return

    setError('')
    setLoading(true)
    setAiProfileId('')
    api.listAIProfiles(runnerId || 'local').then(setAiProfiles).catch(() => setAiProfiles([]))
    // A peer lists its own root when no path is given; this instance starts at the configured one.
    const start = runnerId
      ? Promise.resolve('')
      : api.getConfig().then(config => config.working_dir || '')
    start
      .then(dir => api.listDirs(dir, runnerId || undefined))
      .then(result => {
        setRoot(result.path)
        setCurrentPath(result.path)
        setDirs(result.dirs || [])
      })
      .catch(err => {
        console.error('Failed to load dirs:', err)
        setRoot('')
        setCurrentPath('')
        setDirs([])
        setError(err.message || 'Failed to load directories')
      })
      .finally(() => setLoading(false))
  }, [isOpen, runnerId])

  // Calculate segments BEFORE using them in effects
  const relativePath = root && currentPath.startsWith(root)
    ? currentPath.slice(root.length).replace(/^\//, '') // Remove leading slash
    : ''
  const segments = relativePath ? relativePath.split('/').filter(Boolean) : []

  // Update name when path changes (last segment of relative path)
  useEffect(() => {
    if (currentPath && root) {
      const lastSegment = segments[segments.length - 1] || root.split('/').filter(Boolean).pop() || 'workspace'
      setName(lastSegment)
    }
  }, [currentPath, segments, root])

  // Focus name input when modal opens
  useEffect(() => {
    if (isOpen && nameInputRef.current) {
      setTimeout(() => nameInputRef.current?.focus(), 50)
    }
  }, [isOpen])

  async function loadDir(path: string) {
    setLoading(true)
    setError('')
    try {
      const result = await api.listDirs(path, runnerId || undefined)
      setCurrentPath(result.path)
      setDirs(result.dirs || [])
    } catch (err) {
      console.error('Failed to load directory:', err)
      setError((err as Error).message || 'Failed to load directory')
    } finally {
      setLoading(false)
    }
  }

  async function handleNavigate(dirName: string) {
    const newPath = currentPath.endsWith('/') ? `${currentPath}${dirName}` : `${currentPath}/${dirName}`
    await loadDir(newPath)
  }

  async function handleCreate() {
    if (!name.trim()) {
      setError('Name is required')
      return
    }
    onConfirm(name.trim(), currentPath, { runnerId, aiProfileId })
    onClose()
  }

  if (!isOpen) return null

  return (
    <motion.div
      className="workspace-picker-overlay"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      onClick={onClose}
    >
      <motion.div
        className="workspace-picker-modal"
        initial={{ scale: 0.95, opacity: 0 }}
        animate={{ scale: 1, opacity: 1 }}
        exit={{ scale: 0.95, opacity: 0 }}
        onClick={e => e.stopPropagation()}
      >
        {/* Header */}
        <div className="workspace-picker__header">
          <h2>New Workspace</h2>
          <button className="workspace-picker__close" onClick={onClose} aria-label="Close workspace picker">
            <IconX size={18} />
          </button>
        </div>

        {/* Breadcrumb */}
        <div className="workspace-picker__breadcrumb">
          {/* Root element (Home) */}
          {root && (
            <div className="breadcrumb-item">
              <button
                className={`breadcrumb-segment${segments.length === 0 ? ' breadcrumb-segment--current' : ''}`}
                onClick={() => segments.length > 0 && loadDir(root)}
                disabled={segments.length === 0}
              >
                Home
              </button>
              {segments.length > 0 && <IconChevronRight size={16} className="breadcrumb-slash" />}
            </div>
          )}

          {/* Relative path segments */}
          {segments.map((seg, i) => {
            const pathUpToHere = root ? root + '/' + segments.slice(0, i + 1).join('/') : ''
            const isLast = i === segments.length - 1
            return (
              <div key={i} className="breadcrumb-item">
                <button
                  className={`breadcrumb-segment${isLast ? ' breadcrumb-segment--current' : ''}`}
                  onClick={() => !isLast && loadDir(pathUpToHere)}
                  disabled={isLast}
                >
                  {seg}
                </button>
                {!isLast && <IconChevronRight size={16} className="breadcrumb-slash" />}
              </div>
            )
          })}
        </div>

        {/* Directory list */}
        <div className="workspace-picker__list">
          {loading ? (
            <div className="workspace-picker__message">Loading...</div>
          ) : error ? (
            <div className="workspace-picker__message error">{error}</div>
          ) : dirs.length === 0 ? (
            <div className="workspace-picker__message">No subdirectories</div>
          ) : (
            dirs.map(dir => (
              <button
                key={dir}
                className="workspace-picker__dir-item"
                onClick={() => handleNavigate(dir)}
              >
                <IconFolder size={16} />
                <span>{dir}</span>
              </button>
            ))
          )}
        </div>

        {/* Name input */}
        <div className="workspace-picker__name-input">
          <label>Name</label>
          <input
            ref={nameInputRef}
            type="text"
            placeholder="Workspace name"
            value={name}
            onChange={e => setName(e.target.value)}
            onKeyDown={e => {
              if (e.key === 'Enter') handleCreate()
              if (e.key === 'Escape') onClose()
            }}
            className="input"
          />
        </div>

        {/* Runner: the machine the project works on */}
        {runners.length > 1 && (
          <div className="workspace-picker__name-input">
            <label>Runner</label>
            <select className="input" value={runnerId} onChange={e => setRunnerId(e.target.value)}>
              {runners.map(r => (
                <option key={r.id} value={r.self ? '' : r.id}>
                  {r.hostname}{r.self ? ' (this)' : ''}
                </option>
              ))}
            </select>
          </div>
        )}

        {/* Preconfigured AI of the chosen runner */}
        <div className="workspace-picker__name-input">
          <label>AI</label>
          <select className="input" value={aiProfileId} onChange={e => setAiProfileId(e.target.value)}>
            <option value="">Default account</option>
            {aiProfiles.map(p => (
              <option key={p.id} value={p.id}>
                {p.name}{p.email ? ` — ${p.email}` : ''}
              </option>
            ))}
          </select>
        </div>

        {/* Buttons */}
        <div className="workspace-picker__footer">
          <button className="btn btn--secondary" onClick={onClose}>
            Cancel
          </button>
          <button className="btn btn--primary" onClick={handleCreate} disabled={loading || !name.trim()}>
            Create
          </button>
        </div>
      </motion.div>
    </motion.div>
  )
}
