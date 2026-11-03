import { IconCheck, IconPlus, IconTrash, IconX, IconNotes } from '@tabler/icons-react'
import { motion } from 'framer-motion'
import { useEffect, useRef, useState } from 'react'
import { createIdea, deleteIdea, fetchIdeas, toggleIdea, type Idea } from '../../lib/ideasStorage'
import { backdropClose } from '../../lib/backdropClose'

interface Props {
  isOpen: boolean
  projectId: string | null
  onClose: () => void
  onPendingChange: (count: number) => void
}

type Tab = 'project' | 'global'

export function IdeasModal({ isOpen, projectId, onClose, onPendingChange }: Props) {
  const [tab, setTab] = useState<Tab>('project')
  const [ideas, setIdeas] = useState<Idea[]>([])
  const [input, setInput] = useState('')
  const [loading, setLoading] = useState(false)
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const textareaRef = useRef<HTMLTextAreaElement>(null)

  const scope = tab === 'global' ? 'global' : (projectId ?? null)

  // Notify parent of pending count whenever ideas change
  useEffect(() => {
    console.log('[IdeasModal] ideas:', ideas, 'isOpen:', isOpen)
    const pending = (ideas ?? []).filter(i => !i.done).length
    onPendingChange(pending)
  }, [ideas, onPendingChange])

  // Load ideas whenever modal opens or tab/project changes
  useEffect(() => {
    if (!isOpen) return
    if (!scope) { setIdeas([]); return }
    setLoading(true)
    fetchIdeas(scope)
      .then(setIdeas)
      .catch(() => {})
      .finally(() => setLoading(false))
  }, [isOpen, scope])

  // Focus textarea when modal opens or tab changes
  useEffect(() => {
    if (isOpen) setTimeout(() => textareaRef.current?.focus(), 50)
  }, [isOpen, tab])

  // Auto-resize textarea
  function autoResize(el: HTMLTextAreaElement) {
    el.style.height = 'auto'
    el.style.height = `${el.scrollHeight}px`
  }

  if (!isOpen) return null

  const disabled = tab === 'project' && !projectId

  function updateIdeas(next: Idea[]) {
    setIdeas(next)
  }

  async function handleAdd() {
    const text = input.trim()
    if (!text || !scope) return
    const idea = await createIdea(scope, text)
    updateIdeas([...ideas, idea])
    setInput('')
    if (textareaRef.current) {
      textareaRef.current.style.height = 'auto'
      textareaRef.current.focus()
    }
  }

  async function handleToggle(idea: Idea) {
    await toggleIdea(idea.id, !idea.done)
    updateIdeas(ideas.map(i => i.id === idea.id ? { ...i, done: !i.done } : i))
  }

  async function handleDelete(id: string) {
    await deleteIdea(id)
    updateIdeas(ideas.filter(i => i.id !== id))
    if (expandedId === id) setExpandedId(null)
  }

  function handleKeyDown(e: React.KeyboardEvent<HTMLTextAreaElement>) {
    if (e.key === 'Enter' && (e.ctrlKey || e.metaKey)) {
      e.preventDefault()
      handleAdd()
    }
    if (e.key === 'Escape') onClose()
  }

  function toggleExpand(id: string) {
    setExpandedId(prev => prev === id ? null : id)
  }

  return (
    <>
      <motion.div
        className="ideas-modal-backdrop"
        {...backdropClose(onClose)}
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        exit={{ opacity: 0 }}
        transition={{ duration: 0.15 }}
      />
      <motion.div
        className="ideas-modal"
        initial={{ opacity: 0, scale: 0.95 }}
        animate={{ opacity: 1, scale: 1 }}
        exit={{ opacity: 0, scale: 0.95 }}
        transition={{ duration: 0.15, ease: 'easeOut' }}
      >
        {/* Header */}
        <div className="ideas-modal__header">
          <h3 className="ideas-modal__title"><IconNotes size={16} /> Notes</h3>
          <button className="voice-modal__close" onClick={onClose} title="Close" aria-label="Close ideas">
            <IconX size={18} />
          </button>
        </div>

        {/* Tabs */}
        <div className="ideas-modal__tabs">
          <button
            className={`ideas-modal__tab ${tab === 'project' ? 'ideas-modal__tab--active' : ''}`}
            onClick={() => setTab('project')}
          >
            Project
          </button>
          <button
            className={`ideas-modal__tab ${tab === 'global' ? 'ideas-modal__tab--active' : ''}`}
            onClick={() => setTab('global')}
          >
            Global
          </button>
        </div>

        {/* No-project hint */}
        {disabled && (
          <p className="ideas-modal__hint">Select a project to add project-specific ideas.</p>
        )}

        {/* List */}
        <ul className="ideas-modal__list">
          {loading && <li className="ideas-modal__empty">Loading…</li>}
          {!loading && !disabled && ideas.length === 0 && (
            <li className="ideas-modal__empty">No ideas yet. Add one below.</li>
          )}
          {!loading && ideas.map((idea) => {
            const isExpanded = expandedId === idea.id
            return (
              <li key={idea.id} className={`ideas-modal__item ${idea.done ? 'ideas-modal__item--done' : ''}`}>
                <button
                  className={`ideas-modal__check ${idea.done ? 'ideas-modal__check--done' : ''}`}
                  onClick={() => handleToggle(idea)}
                  title={idea.done ? 'Mark as pending' : 'Mark as done'}
                >
                  {idea.done && <IconCheck size={11} />}
                </button>
                <span
                  className={`ideas-modal__item-text ${isExpanded ? 'ideas-modal__item-text--expanded' : ''}`}
                  onClick={() => toggleExpand(idea.id)}
                  title={isExpanded ? 'Collapse' : 'Click to expand'}
                >
                  {idea.text}
                </span>
                <button
                  className="ideas-modal__remove"
                  onClick={() => handleDelete(idea.id)}
                  title="Delete"
                >
                  <IconTrash size={13} />
                </button>
              </li>
            )
          })}
        </ul>

        {/* Add row */}
        <div className="ideas-modal__add-row">
          <textarea
            ref={textareaRef}
            className="ideas-modal__input"
            placeholder="New idea… (Ctrl+Enter to add)"
            value={input}
            rows={2}
            onChange={(e) => {
              setInput(e.target.value)
              autoResize(e.target)
            }}
            onKeyDown={handleKeyDown}
            disabled={disabled}
          />
          <button
            className="ideas-modal__add-btn"
            onClick={handleAdd}
            disabled={disabled || !input.trim()}
            title="Add idea (Ctrl+Enter)"
          >
            <IconPlus size={16} />
          </button>
        </div>
      </motion.div>
    </>
  )
}
