import { useRef, useState } from 'react'
import { DayPicker, type DateRange } from 'react-day-picker'
import { format } from 'date-fns'
import { IconArchive, IconBolt, IconBookmark, IconBug, IconCircleCheck, IconPlayerPlay, IconChecklist, IconCategory, IconFilterOff, IconFlask, IconTag, IconTool, IconSearch, IconUserOff, IconUsers, IconX } from '@tabler/icons-react'
import 'react-day-picker/style.css'
import './KanbanFilters.css'
import type { Agent, KanbanCard } from '../../types'
import { AgentAvatar } from '../Agents/AgentAvatar'
import { Select, SelectContent, SelectItem, SelectTrigger } from '@/components/ui/select'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'

export interface KanbanFilterState {
  q: string                        // free text: title, description, tags
  type: string                     // 'all' | card type
  agent: string                    // 'all' | 'none' | agent id
  started: DateRange | undefined   // real start (entered In Progress)
  finished: DateRange | undefined  // real end (entered Done)
}

export const NO_FILTERS: KanbanFilterState = {
  q: '', type: 'all', agent: 'all', started: undefined, finished: undefined,
}

const DAY = 86_400_000
const startOfDay = (d: Date) => new Date(d.getFullYear(), d.getMonth(), d.getDate())
const lastDays = (n: number): DateRange => ({ from: startOfDay(new Date(Date.now() - (n - 1) * DAY)), to: startOfDay(new Date()) })

const PRESETS: [string, () => DateRange][] = [
  ['Today', () => lastDays(1)],
  ['Last 7 days', () => lastDays(7)],
  ['Last 30 days', () => lastDays(30)],
  ['This month', () => { const n = new Date(); return { from: new Date(n.getFullYear(), n.getMonth(), 1), to: startOfDay(n) } }],
  ['Last month', () => { const n = new Date(); return { from: new Date(n.getFullYear(), n.getMonth() - 1, 1), to: new Date(n.getFullYear(), n.getMonth(), 0) } }],
]

function inRange(ts: string, r: DateRange | undefined) {
  if (!r?.from) return true
  const t = ts ? new Date(ts).getTime() : NaN
  if (isNaN(t)) return false // never started / not finished yet
  const from = startOfDay(r.from).getTime()
  const to = startOfDay(r.to ?? r.from).getTime() + DAY - 1
  return t >= from && t <= to
}

export function matchesFilters(card: KanbanCard, f: KanbanFilterState) {
  const q = f.q.trim().toLowerCase()
  if (q && !`${card.title} ${card.description} ${(card.tags ?? []).join(' ')}`.toLowerCase().includes(q)) return false
  if (f.type !== 'all' && card.card_type !== f.type) return false
  if (f.agent === 'none' ? card.agent_id !== '' : f.agent !== 'all' && card.agent_id !== f.agent) return false
  return inRange(card.started_at, f.started) && inRange(card.finished_at, f.finished)
}

export function countActiveFilters(f: KanbanFilterState) {
  return (f.q.trim() ? 1 : 0) + (f.type !== 'all' ? 1 : 0) + (f.agent !== 'all' ? 1 : 0)
    + (f.started?.from ? 1 : 0) + (f.finished?.from ? 1 : 0)
}

function rangeLabel(r: DateRange | undefined) {
  if (!r?.from) return 'Any date'
  if (!r.to || r.from.getTime() === r.to.getTime()) return format(r.from, 'MMM d, yyyy')
  const sameYear = r.from.getFullYear() === r.to.getFullYear()
  return `${format(r.from, sameYear ? 'MMM d' : 'MMM d, yyyy')} – ${format(r.to, 'MMM d, yyyy')}`
}

const TYPE_INFO: Record<string, { icon: typeof IconTag; hint: string }> = {
  task: { icon: IconChecklist, hint: 'A unit of work' },
  story: { icon: IconBookmark, hint: 'A user-facing outcome' },
  epic: { icon: IconBolt, hint: 'A group of related cards' },
  bug: { icon: IconBug, hint: 'Something that is broken' },
  spike: { icon: IconFlask, hint: 'Time-boxed research' },
  chore: { icon: IconTool, hint: 'Maintenance, no user impact' },
}

// Symbol of a card type on a circle tinted with the type's color.
function TypeBadge({ type, color, large }: { type: string; color: string; large?: boolean }) {
  const Icon = TYPE_INFO[type]?.icon ?? IconTag
  return (
    <span className={`kf-option__icon kf-option__icon--type${large ? ' kf-option__icon--lg' : ''}`} style={{ '--c': color } as React.CSSProperties}>
      <Icon size={large ? 14 : 15} />
    </span>
  )
}

// A date-range filter: a pill that opens a popover with presets and a two-month calendar.
function DateRangeFilter({ label, value, onChange }: {
  label: string; value: DateRange | undefined; onChange: (r: DateRange | undefined) => void
}) {
  const [open, setOpen] = useState(false)
  const on = !!value?.from
  return (
    <div className="kf-field">
      <Popover open={open} onOpenChange={setOpen}>
        <div className={`kf-pill${on ? ' kf-pill--on' : ''}`}>
          <PopoverTrigger asChild>
            <button type="button" className="kf-pill__main" aria-label={`${label} date range`}>
              {label === 'Started' ? <IconPlayerPlay size={13} /> : <IconCircleCheck size={14} />}
              <span>{on ? rangeLabel(value) : label}</span>
            </button>
          </PopoverTrigger>
          {on && (
            <button type="button" className="kf-pill__clear" onClick={() => onChange(undefined)} aria-label={`Clear ${label}`}>
              <IconX size={12} />
            </button>
          )}
        </div>
        <PopoverContent align="start" className="kf-popover">
          <div className="kf-range">
            <div className="kf-range__presets">
              {PRESETS.map(([name, get]) => (
                <button key={name} type="button" className="kf-range__preset" onClick={() => { onChange(get()); setOpen(false) }}>
                  {name}
                </button>
              ))}
              <button type="button" className="kf-range__preset kf-range__preset--clear" onClick={() => { onChange(undefined); setOpen(false) }}>
                Any date
              </button>
            </div>
            <DayPicker
              mode="range"
              numberOfMonths={2}
              weekStartsOn={1}
              selected={value}
              onSelect={r => onChange(r)}
              defaultMonth={value?.from ?? new Date(new Date().getFullYear(), new Date().getMonth() - 1, 1)}
              className="kf-daypicker"
            />
          </div>
        </PopoverContent>
      </Popover>
    </div>
  )
}

export function KanbanFilters({ filters, onChange, types, typeColor, agents, showArchived, onToggleArchived, shown, total }: {
  filters: KanbanFilterState
  onChange: (f: KanbanFilterState) => void
  types: string[]
  typeColor: (t: string) => string
  agents: Agent[]
  showArchived: boolean
  onToggleArchived: () => void
  shown: number
  total: number
}) {
  const set = (patch: Partial<KanbanFilterState>) => onChange({ ...filters, ...patch })
  const active = countActiveFilters(filters)
  const [searchOpen, setSearchOpen] = useState(false)
  const searchRef = useRef<HTMLInputElement>(null)
  const selectedAgent = agents.find(a => a.id === filters.agent)
  const agentTitle = filters.agent === 'all' ? 'All agents' : filters.agent === 'none' ? 'Unassigned' : (selectedAgent?.name ?? 'Agent')

  return (
    <section className="kf" aria-label="Board filters">
      <div className="kf-row">
        <div className={`kf-search${searchOpen || filters.q ? ' kf-search--open' : ''}`}>
          <button
            type="button"
            className="kf-search__icon"
            aria-label="Search cards"
            title="Search"
            onClick={() => { setSearchOpen(true); setTimeout(() => searchRef.current?.focus(), 0) }}
          >
            <IconSearch size={15} />
          </button>
          <input
            ref={searchRef}
            value={filters.q}
            onChange={e => set({ q: e.target.value })}
            onBlur={() => { if (!filters.q) setSearchOpen(false) }}
            onKeyDown={e => { if (e.key === 'Escape') { set({ q: '' }); setSearchOpen(false); searchRef.current?.blur() } }}
            placeholder="Search title, description, tag…"
            aria-label="Search cards"
            tabIndex={searchOpen || filters.q ? 0 : -1}
          />
          {filters.q && (
            <button type="button" className="kf-search__clear" onClick={() => { set({ q: '' }); searchRef.current?.focus() }} aria-label="Clear search">
              <IconX size={13} />
            </button>
          )}
        </div>

        <div className="kf-field">
          <Select value={filters.type} onValueChange={v => set({ type: v })}>
            <SelectTrigger
              className={`kf-select kf-select--icon${filters.type !== 'all' ? ' kf-select--on' : ''}`}
              aria-label="Card type"
              title={filters.type === 'all' ? 'All types' : filters.type}
            >
              {filters.type === 'all'
                ? <span className="kf-option__icon kf-option__icon--lg"><IconCategory size={14} /></span>
                : <TypeBadge type={filters.type} color={typeColor(filters.type)} large />}
            </SelectTrigger>
            <SelectContent className="kf-select-content">
              <SelectItem value="all" className="kf-option">
                <span className="kf-option__row">
                  <span className="kf-option__icon"><IconCategory size={15} /></span>
                  <span className="kf-option__text"><b>All types</b><small>No type filter</small></span>
                </span>
              </SelectItem>
              {types.map(t => {
                return (
                  <SelectItem key={t} value={t} className="kf-option">
                    <span className="kf-option__row">
                      <TypeBadge type={t} color={typeColor(t)} />
                      <span className="kf-option__text"><b className="kf-cap">{t}</b><small>{TYPE_INFO[t]?.hint ?? 'Custom type'}</small></span>
                    </span>
                  </SelectItem>
                )
              })}
            </SelectContent>
          </Select>
        </div>

        <div className="kf-field">
          <Select value={filters.agent} onValueChange={v => set({ agent: v })}>
            <SelectTrigger
              className={`kf-select kf-select--icon${filters.agent !== 'all' ? ' kf-select--on' : ''}`}
              aria-label="Agent"
              title={agentTitle}
            >
              {filters.agent === 'none'
                ? <span className="kf-option__icon kf-option__icon--lg"><IconUserOff size={14} /></span>
                : selectedAgent
                  ? <AgentAvatar agent={selectedAgent} size={26} />
                  : agents.length === 0
                    ? <span className="kf-option__icon kf-option__icon--lg"><IconUsers size={14} /></span>
                    : (
                      <span className="kf-avatar-group">
                        {agents.slice(0, 3).map(a => <AgentAvatar key={a.id} agent={a} size={26} />)}
                        {agents.length > 3 && <span className="kf-avatar-group__more">+{agents.length - 3}</span>}
                      </span>
                    )}
            </SelectTrigger>
            <SelectContent className="kf-select-content">
              <SelectItem value="all" className="kf-option">
                <span className="kf-option__row">
                  <span className="kf-option__icon"><IconUsers size={15} /></span>
                  <span className="kf-option__text"><b>All agents</b><small>No agent filter</small></span>
                </span>
              </SelectItem>
              <SelectItem value="none" className="kf-option">
                <span className="kf-option__row">
                  <span className="kf-option__icon"><IconUserOff size={15} /></span>
                  <span className="kf-option__text"><b>Unassigned</b><small>Cards without an agent</small></span>
                </span>
              </SelectItem>
              {agents.map(a => (
                <SelectItem key={a.id} value={a.id} className="kf-option">
                  <span className="kf-option__row">
                    <AgentAvatar agent={a} />
                    <span className="kf-option__text"><b>{a.name}</b><small>{a.model || a.executor}</small></span>
                  </span>
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>

        <DateRangeFilter label="Started" value={filters.started} onChange={r => set({ started: r })} />
        <DateRangeFilter label="Finished" value={filters.finished} onChange={r => set({ finished: r })} />

        <div className="kf-end">
          <span className="kf-count"><b>{shown}</b> of {total} cards</span>
          <button type="button" role="switch" aria-checked={showArchived} className={`kf-switch${showArchived ? ' kf-switch--on' : ''}`} onClick={onToggleArchived}>
            <span className="kf-switch__track"><span className="kf-switch__thumb" /></span>
            <IconArchive size={13} /> Archived
          </button>
          {active > 0 && (
            <button type="button" className="kf-clear" onClick={() => onChange(NO_FILTERS)}>
              <IconFilterOff size={13} /> Clear <span className="kf-clear__n">{active}</span>
            </button>
          )}
        </div>
      </div>
    </section>
  )
}
