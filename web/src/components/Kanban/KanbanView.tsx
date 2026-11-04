import { useEffect, useRef, useState } from 'react'
import { useNavigate } from 'react-router-dom'
import {
  IconPlus, IconX, IconTrash, IconArchive,
  IconArchiveOff, IconRobot, IconCalendar, IconLink,
  IconTag, IconGripVertical, IconRefresh,
} from '@tabler/icons-react'
import { useStore } from '../../store'
import type { Agent, CardComment, KanbanCard, KanbanList } from '../../types'
import * as api from '../../api'
import { backdropClose } from '../../lib/backdropClose'
import { KanbanFilters, NO_FILTERS, matchesFilters, type KanbanFilterState } from './KanbanFilters'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { AgentAvatar } from '../Agents/AgentAvatar'
import { OrchestratorOpenButton } from '../Orchestrator/OrchestratorOpenButton'

// ---- pasted images -----------------------------------------------------------

const MAX_IMAGE_BYTES = 550 * 1024 // the server accepts up to 600 KB
const MAX_IMAGE_SIDE = 1600

function blobToBase64(blob: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const r = new FileReader()
    r.onload = () => resolve(String(r.result).split(',')[1] ?? '')
    r.onerror = () => reject(r.error)
    r.readAsDataURL(blob)
  })
}

// Small images go up as they are; bigger ones are shrunk to JPEG until they fit,
// because the agent receives them through a size-limited channel.
async function fitImage(file: Blob): Promise<{ type: string; blob: Blob }> {
  const okType = ['image/png', 'image/jpeg', 'image/webp', 'image/gif'].includes(file.type)
  if (okType && file.size <= MAX_IMAGE_BYTES) return { type: file.type, blob: file }
  const bmp = await createImageBitmap(file)
  const base = Math.min(1, MAX_IMAGE_SIDE / Math.max(bmp.width, bmp.height))
  for (const shrink of [1, 0.7, 0.5, 0.35, 0.25]) {
    const scale = base * shrink
    const canvas = document.createElement('canvas')
    canvas.width = Math.max(1, Math.round(bmp.width * scale))
    canvas.height = Math.max(1, Math.round(bmp.height * scale))
    const ctx = canvas.getContext('2d')!
    ctx.fillStyle = '#fff' // JPEG has no transparency
    ctx.fillRect(0, 0, canvas.width, canvas.height)
    ctx.drawImage(bmp, 0, 0, canvas.width, canvas.height)
    for (const quality of [0.85, 0.7, 0.55]) {
      const blob = await new Promise<Blob | null>(res => canvas.toBlob(res, 'image/jpeg', quality))
      if (blob && blob.size <= MAX_IMAGE_BYTES) return { type: 'image/jpeg', blob }
    }
  }
  throw new Error('image too large')
}

// ---- constants ---------------------------------------------------------------

const CARD_TYPES = ['task', 'story', 'epic', 'bug', 'spike', 'chore']

const TYPE_COLORS: Record<string, string> = {
  task:  '#60a5fa',
  story: '#4ade80',
  epic:  '#a78bfa',
  bug:   '#f87171',
  spike: '#facc15',
  chore: '#94a3b8',
}

// ---- helpers -----------------------------------------------------------------

function typeColor(t: string) {
  return TYPE_COLORS[t] ?? '#6b7280'
}

function formatDate(s: string) {
  if (!s) return ''
  const d = new Date(s)
  if (isNaN(d.getTime())) return s
  return d.toLocaleDateString(undefined, { month: 'short', day: 'numeric' })
}

// ---- CardModal ---------------------------------------------------------------

interface CardModalProps {
  card: KanbanCard | null
  lists: KanbanList[]
  agents: Agent[]
  allCards: KanbanCard[]
  onClose: () => void
  onSave: (id: string, patch: Partial<KanbanCard>) => Promise<void>
  onDelete: (id: string) => Promise<void>
  onArchive: (id: string, archived: boolean) => Promise<void>
  onOpenChat: (id: string, patch: Partial<KanbanCard>) => Promise<void>
}

function CardModal({ card, lists, agents, allCards, onClose, onSave, onDelete, onArchive, onOpenChat }: CardModalProps) {
  const [form, setForm] = useState<Partial<KanbanCard>>({})
  const [tagInput, setTagInput] = useState('')
  const [saving, setSaving] = useState(false)
  const [activeTab, setActiveTab] = useState<'details' | 'relations'>('details')
  const [comments, setComments] = useState<CardComment[]>([])
  const boardVersion = useStore(s => s.boardVersion)
  const [menuOpen, setMenuOpen] = useState(false)
  const [images, setImages] = useState<api.CardImage[]>([])
  const [imageError, setImageError] = useState('')
  const descRef = useRef<HTMLTextAreaElement>(null)

  useEffect(() => {
    if (card) setForm({ ...card })
    setMenuOpen(false)
    setImageError('')
    setImages([])
    if (card) api.listCardImages(card.id).then(setImages).catch(() => setImages([]))
  }, [card?.id])

  // Insert text at the cursor of the description (or at its end).
  function insertInDescription(text: string) {
    const el = descRef.current
    const cur = form.description ?? ''
    const start = el?.selectionStart ?? cur.length
    const end = el?.selectionEnd ?? cur.length
    const pad = start > 0 && !/\s$/.test(cur.slice(0, start)) ? ' ' : ''
    const next = cur.slice(0, start) + pad + text + ' ' + cur.slice(end)
    setForm(f => ({ ...f, description: next }))
    const pos = start + pad.length + text.length + 1
    requestAnimationFrame(() => { el?.focus(); el?.setSelectionRange(pos, pos) })
  }

  // Pasting an image into the description attaches it to the task and writes its
  // @imageN reference at the cursor.
  async function handlePaste(e: React.ClipboardEvent<HTMLTextAreaElement>) {
    if (!card) return
    const files = Array.from(e.clipboardData.items)
      .filter(i => i.kind === 'file' && i.type.startsWith('image/'))
      .map(i => i.getAsFile())
      .filter((f): f is File => !!f)
    if (files.length === 0) return
    e.preventDefault()
    setImageError('')
    for (const f of files) {
      try {
        const { type, blob } = await fitImage(f)
        const saved = await api.addCardImage(card.id, type, await blobToBase64(blob))
        setImages(list => [...list, saved])
        insertInDescription('@' + saved.name)
      } catch (err) {
        setImageError(err instanceof Error ? err.message : 'Could not attach the image')
      }
    }
  }

  async function removeImage(name: string) {
    if (!card) return
    await api.deleteCardImage(card.id, name)
    setImages(list => list.filter(i => i.name !== name))
  }

  // Detach the task from its session(s): the old chats stay as history and the
  // next run starts a new conversation.
  async function handleReset() {
    if (!card) return
    setMenuOpen(false)
    if (!confirm('Reset this task? Its current session is detached (kept as history) and the next run starts a new one.')) return
    await api.resetKanbanCard(card.id)
    useStore.setState(s => ({
      sessions: s.sessions.map(x => x.card_id === card.id ? { ...x, card_id: '', status: x.status === 'waiting_hook' ? 'idle' : x.status } : x),
      boardVersion: s.boardVersion + 1,
    }))
  }

  // Notes the agent posted on this task; re-read when the board changes.
  useEffect(() => {
    if (!card) return
    api.listCardComments(card.id).then(setComments).catch(() => setComments([]))
  }, [card?.id, boardVersion])

  if (!card) return null

  function patch<K extends keyof KanbanCard>(k: K, v: KanbanCard[K]) {
    setForm(f => ({ ...f, [k]: v }))
  }

  function addTag() {
    const t = tagInput.trim()
    if (!t) return
    const tags = [...(form.tags ?? []), t]
    patch('tags', tags)
    setTagInput('')
  }

  function removeTag(t: string) {
    patch('tags', (form.tags ?? []).filter(x => x !== t))
  }

  function toggleDepend(id: string) {
    const deps = form.depends_on ?? []
    patch('depends_on', deps.includes(id) ? deps.filter(x => x !== id) : [...deps, id])
  }

  async function handleSave() {
    if (!card) return
    setSaving(true)
    try { await onSave(card.id, form) } finally { setSaving(false) }
    onClose()
  }

  async function handleOpenChat() {
    if (!card) return
    setSaving(true)
    try { await onOpenChat(card.id, form) } finally { setSaving(false) }
    onClose()
  }

  const otherCards = allCards.filter(c => c.id !== card.id && !c.archived)
  const epicCards = allCards.filter(c => c.card_type === 'epic' && c.id !== card.id)

  return (
    <div className="kmodal-backdrop" {...backdropClose(onClose)}>
      <div className="kmodal">
        {/* header */}
        <div className="kmodal__header">
          <div className="kmodal__type-row">
            <select
              className="kmodal__type-select"
              value={form.card_type ?? card.card_type}
              onChange={e => patch('card_type', e.target.value)}
              style={{ '--type-color': typeColor(form.card_type ?? card.card_type) } as React.CSSProperties}
            >
              {CARD_TYPES.map(t => <option key={t} value={t}>{t}</option>)}
              {/* allow existing custom type to stay selectable */}
              {!CARD_TYPES.includes(card.card_type) && (
                <option value={card.card_type}>{card.card_type}</option>
              )}
            </select>
            <select
              className="kmodal__list-select"
              value={form.list_id ?? card.list_id}
              onChange={e => patch('list_id', e.target.value)}
            >
              {lists.map(l => <option key={l.id} value={l.id}>{l.name}</option>)}
            </select>
          </div>
          <div className="kmodal__header-actions">
            <div style={{ position: 'relative' }}>
              <button
                className="kmodal__action-btn"
                title="More"
                onClick={() => setMenuOpen(v => !v)}
              >⋯</button>
              {menuOpen && (
                <div className="nav-tree__dropdown" style={{ right: 0, left: 'auto' }}>
                  <button className="nav-tree__menu-item" onClick={handleReset}>
                    <IconRefresh size={14} />
                    <span>Reset</span>
                  </button>
                </div>
              )}
            </div>
            <button
              className="kmodal__action-btn"
              title={card.archived ? 'Unarchive' : 'Archive'}
              onClick={() => { onArchive(card.id, !card.archived); onClose() }}
            >
              {card.archived ? <IconArchiveOff size={15} /> : <IconArchive size={15} />}
            </button>
            <button
              className="kmodal__action-btn kmodal__action-btn--danger"
              title="Delete card"
              onClick={() => { if (confirm('Delete this card?')) { onDelete(card.id); onClose() } }}
            >
              <IconTrash size={15} />
            </button>
            <button className="kmodal__action-btn" onClick={onClose} title="Close">
              <IconX size={15} />
            </button>
          </div>
        </div>

        {/* title */}
        <input
          className="kmodal__title-input"
          value={form.title ?? ''}
          onChange={e => patch('title', e.target.value)}
          placeholder="Card title"
        />

        {/* tabs */}
        <div className="kmodal__tabs">
          <button
            className={`kmodal__tab${activeTab === 'details' ? ' kmodal__tab--active' : ''}`}
            onClick={() => setActiveTab('details')}
          >Details</button>
          <button
            className={`kmodal__tab${activeTab === 'relations' ? ' kmodal__tab--active' : ''}`}
            onClick={() => setActiveTab('relations')}
          >Relations</button>
        </div>

        <div className="kmodal__body">
          {activeTab === 'details' && (
            <>
              {/* description */}
              <div className="kmodal__field">
                <label className="kmodal__label">Description</label>
                {images.length > 0 && (
                  <div className="kmodal__images">
                    {images.map(im => (
                      <figure key={im.name} className="kmodal__image">
                        <img
                          src={`data:${im.media_type};base64,${im.data}`}
                          alt={im.name}
                          title={`Click to insert @${im.name}`}
                          onClick={() => insertInDescription('@' + im.name)}
                        />
                        <figcaption>@{im.name}</figcaption>
                        <button
                          type="button"
                          className="kmodal__image-remove"
                          title="Remove image"
                          onClick={() => removeImage(im.name)}
                        >×</button>
                      </figure>
                    ))}
                  </div>
                )}
                <textarea
                  ref={descRef}
                  className="kmodal__textarea"
                  rows={4}
                  value={form.description ?? ''}
                  onChange={e => patch('description', e.target.value)}
                  onPaste={handlePaste}
                  placeholder="Add a description… paste an image to attach it (referenced as @image1)"
                />
                {imageError && <p className="kmodal__empty-hint" style={{ color: '#ef4444' }}>{imageError}</p>}
              </div>

              {/* dates */}
              <div className="kmodal__row">
                <div className="kmodal__field">
                  <label className="kmodal__label"><IconCalendar size={12} /> Start date</label>
                  <input
                    type="date"
                    className="kmodal__input"
                    value={form.start_date ?? ''}
                    onChange={e => patch('start_date', e.target.value)}
                  />
                </div>
                <div className="kmodal__field">
                  <label className="kmodal__label"><IconCalendar size={12} /> End date</label>
                  <input
                    type="date"
                    className="kmodal__input"
                    value={form.end_date ?? ''}
                    onChange={e => patch('end_date', e.target.value)}
                  />
                </div>
              </div>

              {/* agent */}
              <div className="kmodal__field">
                <label className="kmodal__label"><IconRobot size={12} /> Assigned agent</label>
                <select
                  className="kmodal__select"
                  value={form.agent_id ?? ''}
                  onChange={e => patch('agent_id', e.target.value)}
                >
                  <option value="">Unassigned</option>
                  {agents.map(a => <option key={a.id} value={a.id}>{a.name}</option>)}
                </select>
              </div>

              {/* epic parent */}
              {(form.card_type ?? card.card_type) !== 'epic' && epicCards.length > 0 && (
                <div className="kmodal__field">
                  <label className="kmodal__label">Parent epic</label>
                  <select
                    className="kmodal__select"
                    value={form.parent_id ?? ''}
                    onChange={e => patch('parent_id', e.target.value)}
                  >
                    <option value="">None</option>
                    {epicCards.map(c => <option key={c.id} value={c.id}>{c.title}</option>)}
                  </select>
                </div>
              )}

              {/* tags */}
              <div className="kmodal__field">
                <label className="kmodal__label"><IconTag size={12} /> Tags</label>
                <div className="kmodal__tags">
                  {(form.tags ?? []).map(t => (
                    <span key={t} className="kmodal__tag">
                      {t}
                      <button className="kmodal__tag-remove" onClick={() => removeTag(t)}>×</button>
                    </span>
                  ))}
                </div>
                <div className="kmodal__tag-input-row">
                  <input
                    className="kmodal__input"
                    value={tagInput}
                    onChange={e => setTagInput(e.target.value)}
                    onKeyDown={e => { if (e.key === 'Enter') { e.preventDefault(); addTag() } }}
                    placeholder="Add tag…"
                  />
                  <button className="kmodal__add-tag-btn" onClick={addTag} type="button">Add</button>
                </div>
              </div>

              {/* custom type */}
              <div className="kmodal__field">
                <label className="kmodal__label">Custom type</label>
                <input
                  className="kmodal__input"
                  value={CARD_TYPES.includes(form.card_type ?? '') ? '' : (form.card_type ?? '')}
                  onChange={e => patch('card_type', e.target.value || 'task')}
                  placeholder="e.g. spike, chore, research…"
                />
              </div>
            </>
          )}

          {activeTab === 'relations' && (
            <>
              <div className="kmodal__field">
                <label className="kmodal__label"><IconLink size={12} /> Depends on</label>
                <div className="kmodal__dep-list">
                  {otherCards.map(c => (
                    <label key={c.id} className="kmodal__dep-row">
                      <input
                        type="checkbox"
                        checked={(form.depends_on ?? []).includes(c.id)}
                        onChange={() => toggleDepend(c.id)}
                      />
                      <span
                        className="kmodal__dep-type"
                        style={{ color: typeColor(c.card_type) }}
                      >{c.card_type}</span>
                      <span className="kmodal__dep-title">{c.title}</span>
                    </label>
                  ))}
                  {otherCards.length === 0 && (
                    <p className="kmodal__empty-hint">No other cards yet.</p>
                  )}
                </div>
              </div>

              {/* subtasks when this is an epic */}
              {(form.card_type ?? card.card_type) === 'epic' && (
                <div className="kmodal__field">
                  <label className="kmodal__label">Subtasks</label>
                  <div className="kmodal__dep-list">
                    {allCards.filter(c => c.parent_id === card.id).map(c => (
                      <div key={c.id} className="kmodal__dep-row">
                        <span className="kmodal__dep-type" style={{ color: typeColor(c.card_type) }}>{c.card_type}</span>
                        <span className="kmodal__dep-title">{c.title}</span>
                      </div>
                    ))}
                    {allCards.filter(c => c.parent_id === card.id).length === 0 && (
                      <p className="kmodal__empty-hint">No subtasks assigned to this epic.</p>
                    )}
                  </div>
                </div>
              )}
            </>
          )}
        </div>

        {comments.length > 0 && (
          <div className="kmodal__activity">
            <div className="kmodal__activity-title">Activity</div>
            {comments.map(c => (
              <div key={c.id} className={`kmodal__comment kmodal__comment--${c.kind}`}>
                <div className="kmodal__comment-head">
                  <span>{c.author}</span>
                  {c.kind !== 'comment' && <span className="kmodal__comment-kind">{c.kind}</span>}
                  <span className="kmodal__comment-time">{new Date(c.created_at).toLocaleString()}</span>
                </div>
                <div className="kmodal__comment-body">{c.body}</div>
              </div>
            ))}
          </div>
        )}

        <div className="kmodal__footer">
          <button
            className="kmodal__btn kmodal__btn--cancel"
            onClick={handleOpenChat}
            disabled={saving || !form.agent_id}
            title={form.agent_id ? "Open this task's chat with its agent" : 'Assign an agent first'}
          >
            Open chat
          </button>
          <button className="kmodal__btn kmodal__btn--cancel" onClick={onClose}>Cancel</button>
          <button className="kmodal__btn kmodal__btn--save" onClick={handleSave} disabled={saving}>
            {saving ? 'Saving…' : 'Save'}
          </button>
        </div>
      </div>
    </div>
  )
}

// ---- KanbanCardItem ----------------------------------------------------------

interface CardItemProps {
  card: KanbanCard
  agents: Agent[]
  onClick: () => void
  onDragStart: (e: React.DragEvent) => void
  onDragOver: (e: React.DragEvent) => void
  onDrop: (e: React.DragEvent) => void
}

const STATUS_BADGE: Record<string, { label: string; color: string }> = {
  running:      { label: '● running',        color: '#22c55e' },
  waiting_hook: { label: '◆ needs approval', color: '#f59e0b' },
  error:        { label: '✕ error',          color: '#ef4444' },
}

function KanbanCardItem({ card, agents, onClick, onDragStart, onDragOver, onDrop }: CardItemProps) {
  const agent = agents.find(a => a.id === card.agent_id)
  // Live state of the agent working on this task (its chat session).
  const runStatus = useStore(s => {
    const sess = s.sessions.find(x => x.card_id === card.id)
    return sess ? (s.sessionStatus[sess.id] ?? sess.status) : undefined
  })
  const badge = runStatus ? STATUS_BADGE[runStatus] : undefined
  const hasDates = card.start_date || card.end_date
  const isOverdue = card.end_date && new Date(card.end_date) < new Date()

  return (
    <div
      className="kcard"
      draggable
      onDragStart={onDragStart}
      onDragOver={e => { e.preventDefault(); onDragOver(e) }}
      onDrop={onDrop}
      onClick={onClick}
    >
      <div className="kcard__top">
        <span className="kcard__type" style={{ color: typeColor(card.card_type) }}>
          {card.card_type}
        </span>
        <IconGripVertical size={12} className="kcard__grip" />
      </div>
      <p className="kcard__title">{card.title}</p>
      {card.description && (
        <p className="kcard__desc">{card.description}</p>
      )}
      <div className="kcard__meta">
        {hasDates && (
          <span className={`kcard__date${isOverdue ? ' kcard__date--overdue' : ''}`}>
            <IconCalendar size={11} />
            {card.end_date ? formatDate(card.end_date) : formatDate(card.start_date)}
          </span>
        )}
        {card.tags.slice(0, 3).map(t => (
          <span key={t} className="kcard__tag">{t}</span>
        ))}
        {badge && (
          <span className="kcard__status" style={{ color: badge.color }}>{badge.label}</span>
        )}
        {card.depends_on.length > 0 && (
          <span className="kcard__deps" title={`${card.depends_on.length} dependency`}>
            <IconLink size={11} />{card.depends_on.length}
          </span>
        )}
        {agent && <CardAgent agent={agent} />}
      </div>
    </div>
  )
}

// The agent of a card: just its avatar, on the right; hovering shows its name.
function CardAgent({ agent }: { agent: Agent }) {
  const [open, setOpen] = useState(false)
  return (
    <Popover open={open}>
      <PopoverTrigger asChild>
        <span
          className="kcard__agent"
          tabIndex={0}
          aria-label={`Agent: ${agent.name}`}
          onMouseEnter={() => setOpen(true)}
          onMouseLeave={() => setOpen(false)}
          onFocus={() => setOpen(true)}
          onBlur={() => setOpen(false)}
        >
          <AgentAvatar agent={agent} size={22} />
        </span>
      </PopoverTrigger>
      <PopoverContent side="top" align="end" sideOffset={6} className="kcard-agent-pop" onOpenAutoFocus={e => e.preventDefault()}>
        {agent.name}
      </PopoverContent>
    </Popover>
  )
}

// ---- KanbanColumn ------------------------------------------------------------

interface ColumnProps {
  list: KanbanList
  cards: KanbanCard[]
  agents: Agent[]
  onCardClick: (c: KanbanCard) => void
  onAddCard: (listId: string, title: string) => void
  onRenameList: (id: string, name: string) => void
  onDeleteList: (id: string) => void
  onCardDragStart: (cardId: string, fromListId: string) => void
  onCardDrop: (toListId: string, afterCardId?: string) => void
}

function KanbanColumn({
  list, cards, agents,
  onCardClick, onAddCard, onRenameList, onDeleteList,
  onCardDragStart, onCardDrop,
}: ColumnProps) {
  const [addingCard, setAddingCard] = useState(false)
  const [newCardTitle, setNewCardTitle] = useState('')
  const [editingName, setEditingName] = useState(false)
  const [listName, setListName] = useState(list.name)
  const inputRef = useRef<HTMLInputElement>(null)

  function handleAddCard() {
    const t = newCardTitle.trim()
    if (!t) { setAddingCard(false); return }
    onAddCard(list.id, t)
    setNewCardTitle('')
    setAddingCard(false)
  }

  function handleRename() {
    const n = listName.trim()
    if (n && n !== list.name) onRenameList(list.id, n)
    setEditingName(false)
  }

  return (
    <div
      className="kcolumn"
      onDragOver={e => e.preventDefault()}
      onDrop={() => onCardDrop(list.id)}
    >
      {/* column header */}
      <div className="kcolumn__header">
        {editingName ? (
          <input
            className="kcolumn__name-input"
            value={listName}
            onChange={e => setListName(e.target.value)}
            onBlur={handleRename}
            onKeyDown={e => { if (e.key === 'Enter') handleRename(); if (e.key === 'Escape') setEditingName(false) }}
            autoFocus
          />
        ) : (
          <button className="kcolumn__name" onClick={() => setEditingName(true)} title="Click to rename">
            {list.name}
            <span className="kcolumn__count">{cards.length}</span>
          </button>
        )}
        <button
          className="kcolumn__delete-btn"
          title="Delete list"
          onClick={() => { if (confirm(`Delete list "${list.name}"?`)) onDeleteList(list.id) }}
        >
          <IconTrash size={12} />
        </button>
      </div>

      {/* cards */}
      <div className="kcolumn__cards">
        {cards.map(card => (
          <KanbanCardItem
            key={card.id}
            card={card}
            agents={agents}
            onClick={() => onCardClick(card)}
            onDragStart={() => onCardDragStart(card.id, list.id)}
            onDragOver={e => e.preventDefault()}
            onDrop={() => onCardDrop(list.id, card.id)}
          />
        ))}

        {/* drop zone at bottom */}
        <div
          className="kcolumn__drop-zone"
          onDragOver={e => e.preventDefault()}
          onDrop={() => onCardDrop(list.id)}
        />
      </div>

      {/* add card */}
      {addingCard ? (
        <div className="kcolumn__add-form">
          <input
            ref={inputRef}
            className="kcolumn__add-input"
            value={newCardTitle}
            onChange={e => setNewCardTitle(e.target.value)}
            onKeyDown={e => { if (e.key === 'Enter') handleAddCard(); if (e.key === 'Escape') { setAddingCard(false); setNewCardTitle('') } }}
            placeholder="Describe the task…"
            autoFocus
          />
          <div className="kcolumn__add-actions">
            <button className="kcolumn__add-confirm" onClick={handleAddCard}>Add</button>
            <button className="kcolumn__add-cancel" onClick={() => { setAddingCard(false); setNewCardTitle('') }}>
              <IconX size={13} />
            </button>
          </div>
        </div>
      ) : (
        <button className="kcolumn__add-btn" onClick={() => setAddingCard(true)}>
          <IconPlus size={13} /> Add card
        </button>
      )}
    </div>
  )
}

// ---- KanbanView (main) -------------------------------------------------------

export function KanbanView() {
  const activeProjectId = useStore(s => s.activeProjectId)
  const openTaskSession = useStore(s => s.openTaskSession)
  const navigate = useNavigate()

  const [lists, setLists] = useState<KanbanList[]>([])
  const [cards, setCards] = useState<KanbanCard[]>([])
  const [agents, setAgents] = useState<Agent[]>([])
  const [showArchived, setShowArchived] = useState(false)
  const [filters, setFilters] = useState<KanbanFilterState>(NO_FILTERS)
  const [selectedCard, setSelectedCard] = useState<KanbanCard | null>(null)
  const [addingList, setAddingList] = useState(false)
  const [newListName, setNewListName] = useState('')
  const [loading, setLoading] = useState(false)

  // drag state
  const dragCardId = useRef<string | null>(null)
  const dragFromListId = useRef<string | null>(null)

  useEffect(() => {
    if (!activeProjectId) return
    setLoading(true)
    Promise.all([
      api.listKanbanLists(activeProjectId),
      api.listKanbanCards(activeProjectId, showArchived),
      api.listAgents(activeProjectId),
    ]).then(([ls, cs, ags]) => {
      setLists(ls)
      setCards(cs)
      setAgents(ags)
    }).catch(console.error)
      .finally(() => setLoading(false))
  }, [activeProjectId, showArchived])

  // An agent finished, blocked or commented on a task: re-read the board.
  // (Hooks must run on every render, so this stays above the early return below.)
  const boardVersion = useStore(s => s.boardVersion)
  useEffect(() => {
    if (boardVersion > 0) reloadBoard().catch(console.error)
  }, [boardVersion])

  // Mouse wheel: vertical scroll inside a card list, horizontal everywhere else on the board.
  const boardRef = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const board = boardRef.current
    if (!board) return
    const onWheel = (e: WheelEvent) => {
      if (e.ctrlKey || e.deltaX !== 0 || e.deltaY === 0) return
      const list = (e.target as HTMLElement).closest<HTMLElement>('.kcolumn__cards')
      if (list) {
        const canUp = e.deltaY < 0 && list.scrollTop > 0
        const canDown = e.deltaY > 0 && list.scrollTop + list.clientHeight < list.scrollHeight - 1
        if (canUp || canDown) return // let the list scroll natively
      }
      if (board.scrollWidth <= board.clientWidth) return
      e.preventDefault()
      board.scrollLeft += e.deltaY
    }
    board.addEventListener('wheel', onWheel, { passive: false })
    return () => board.removeEventListener('wheel', onWheel)
  }, [loading, activeProjectId])

  if (!activeProjectId) {
    return <div className="kanban-view kanban-view--empty">Select a project to view the board.</div>
  }

  // ---- list ops ----

  async function handleAddList() {
    const n = newListName.trim()
    if (!n) { setAddingList(false); return }
    const pos = lists.length
    const l = await api.createKanbanList(activeProjectId!, { name: n, position: pos })
    setLists(prev => [...prev, l])
    setNewListName('')
    setAddingList(false)
  }

  async function handleRenameList(id: string, name: string) {
    const l = lists.find(x => x.id === id)
    if (!l) return
    const updated = await api.updateKanbanList(id, { name, position: l.position })
    setLists(prev => prev.map(x => x.id === id ? updated : x))
  }

  async function handleDeleteList(id: string) {
    await api.deleteKanbanList(id)
    setLists(prev => prev.filter(x => x.id !== id))
    setCards(prev => prev.filter(c => c.list_id !== id))
  }

  // ---- card ops ----

  async function handleAddCard(listId: string, title: string) {
    const pos = cards.filter(c => c.list_id === listId).length
    const c = await api.createKanbanCard(activeProjectId!, {
      list_id: listId,
      description: title, // the server generates the title from the description
      card_type: 'task',
      tags: [],
      depends_on: [],
      position: pos,
    })
    setCards(prev => [...prev, c])
  }

  // Moving a card runs the workflow server-side (In Progress starts the agent or
  // parks the card in Blocked; Done releases dependents), so re-read the board.
  async function reloadBoard() {
    if (!activeProjectId) return
    const [cs, sess] = await Promise.all([
      api.listKanbanCards(activeProjectId, showArchived),
      api.listSessions(activeProjectId),
    ])
    setCards(cs)
    useStore.setState({ sessions: sess })
  }

  async function handleSaveCard(id: string, patch: Partial<KanbanCard>) {
    const before = cards.find(c => c.id === id)
    const updated = await api.updateKanbanCard(id, patch)
    setCards(prev => prev.map(c => c.id === id ? updated : c))
    // keep modal in sync
    setSelectedCard(updated)
    if (before && before.list_id !== updated.list_id) await reloadBoard()
  }

  // Save the card (so the agent assignment sticks), then open its chat session.
  async function handleOpenChat(id: string, patch: Partial<KanbanCard>) {
    const updated = await api.updateKanbanCard(id, patch)
    setCards(prev => prev.map(c => c.id === id ? updated : c))
    const sess = await openTaskSession(id)
    navigate(`/projects/${sess.project_id}/sessions/${sess.id}`)
  }

  async function handleDeleteCard(id: string) {
    await api.deleteKanbanCard(id)
    setCards(prev => prev.filter(c => c.id !== id))
  }

  async function handleArchiveCard(id: string, archived: boolean) {
    const updated = await api.updateKanbanCard(id, { archived })
    setCards(prev => showArchived ? prev.map(c => c.id === id ? updated : c) : prev.filter(c => c.id !== id))
  }

  // ---- drag & drop ----

  function handleCardDragStart(cardId: string, fromListId: string) {
    dragCardId.current = cardId
    dragFromListId.current = fromListId
  }

  async function handleCardDrop(toListId: string, afterCardId?: string) {
    const cid = dragCardId.current
    if (!cid) return
    dragCardId.current = null
    dragFromListId.current = null

    // reorder locally first for snappy feel
    setCards(prev => {
      const moving = prev.find(c => c.id === cid)
      if (!moving) return prev
      const withoutMoving = prev.filter(c => c.id !== cid)
      const toList = withoutMoving.filter(c => c.list_id === toListId)
      const otherLists = withoutMoving.filter(c => c.list_id !== toListId)

      let newToList: KanbanCard[]
      if (afterCardId) {
        const idx = toList.findIndex(c => c.id === afterCardId)
        newToList = [...toList.slice(0, idx + 1), { ...moving, list_id: toListId }, ...toList.slice(idx + 1)]
      } else {
        newToList = [...toList, { ...moving, list_id: toListId }]
      }
      // assign positions
      const reordered = newToList.map((c, i) => ({ ...c, position: i }))
      return [...otherLists, ...reordered]
    })

    // persist: update list and position for moved card + reorder all in target list
    const card = cards.find(c => c.id === cid)
    if (!card) return
    const newPos = afterCardId
      ? (cards.filter(c => c.list_id === toListId).findIndex(c => c.id === afterCardId) + 1)
      : cards.filter(c => c.list_id === toListId).length
    await api.updateKanbanCard(cid, { list_id: toListId, position: newPos })
    if (card.list_id !== toListId) await reloadBoard()
  }

  // ---- render ----

  const visibleCards = (showArchived ? cards : cards.filter(c => !c.archived))
    .filter(c => matchesFilters(c, filters))
  const cardTypes = [...new Set([...CARD_TYPES, ...cards.map(c => c.card_type)])]

  return (
    <div className="kanban-view">
      <KanbanFilters
        filters={filters}
        onChange={setFilters}
        types={cardTypes}
        typeColor={typeColor}
        agents={agents}
        showArchived={showArchived}
        onToggleArchived={() => setShowArchived(v => !v)}
        shown={visibleCards.length}
        total={cards.filter(c => showArchived || !c.archived).length}
      />
      {/* board */}
      <div className="kanban-board" ref={boardRef}>
        {loading && <div className="kanban-board__loading">Loading…</div>}

        {lists.map(list => (
          <KanbanColumn
            key={list.id}
            list={list}
            cards={visibleCards.filter(c => c.list_id === list.id).sort((a, b) => a.position - b.position)}
            agents={agents}
            onCardClick={setSelectedCard}
            onAddCard={handleAddCard}
            onRenameList={handleRenameList}
            onDeleteList={handleDeleteList}
            onCardDragStart={handleCardDragStart}
            onCardDrop={handleCardDrop}
          />
        ))}

        {/* add list inline */}
        {addingList ? (
          <div className="kcolumn kcolumn--new">
            <input
              className="kcolumn__name-input"
              value={newListName}
              onChange={e => setNewListName(e.target.value)}
              onKeyDown={e => { if (e.key === 'Enter') handleAddList(); if (e.key === 'Escape') { setAddingList(false); setNewListName('') } }}
              placeholder="List name…"
              autoFocus
            />
            <div className="kcolumn__add-actions">
              <button className="kcolumn__add-confirm" onClick={handleAddList}>Create</button>
              <button className="kcolumn__add-cancel" onClick={() => { setAddingList(false); setNewListName('') }}>
                <IconX size={13} />
              </button>
            </div>
          </div>
        ) : (
          !loading && (
            <button className="kcolumn kcolumn--add" onClick={() => setAddingList(true)}>
              <IconPlus size={14} />
              {lists.length === 0 ? 'Add first list' : 'Add another list'}
            </button>
          )
        )}
      </div>

      {/* board actions live under the board */}
      <div className="kanban-view__footer">
        <OrchestratorOpenButton />
      </div>

      {/* card modal */}
      {selectedCard && (
        <CardModal
          card={selectedCard}
          lists={lists}
          agents={agents}
          allCards={cards}
          onClose={() => setSelectedCard(null)}
          onSave={handleSaveCard}
          onDelete={handleDeleteCard}
          onArchive={handleArchiveCard}
          onOpenChat={handleOpenChat}
        />
      )}
    </div>
  )
}
