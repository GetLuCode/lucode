import { useState } from 'react'
import { IconSettings, IconCheck } from '@tabler/icons-react'
import { Popover, PopoverContent, PopoverTrigger } from '@/components/ui/popover'
import { cn } from '@/lib/utils'

export interface EnvOption {
  value: string
  label: string
  hint?: string // secondary text (e.g. an account email)
}

function Section({ title, value, options, onChange }: {
  title: string
  value: string
  options: EnvOption[]
  onChange: (value: string) => void
}) {
  return (
    <div className="env-picker__section">
      <div className="env-picker__title">{title}</div>
      {options.map(o => (
        <button
          key={o.value}
          type="button"
          className={cn('env-picker__item', o.value === value && 'env-picker__item--active')}
          onClick={() => onChange(o.value)}
        >
          <span className="env-picker__item-text">
            <span>{o.label}</span>
            {o.hint && <span className="env-picker__hint">{o.hint}</span>}
          </span>
          {o.value === value && <IconCheck size={13} className="env-picker__check" />}
        </button>
      ))}
    </div>
  )
}

// A gear button that opens a small floating panel (no overlay) to choose the runner
// and the preconfigured AI the orchestrator runs with.
export function EnvPicker({ runner, runners, onRunner, ai, profiles, onAI, model, models, onModel }: {
  runner: string
  runners: EnvOption[]
  onRunner: (value: string) => void
  ai: string
  profiles: EnvOption[]
  onAI: (value: string) => void
  model: string
  models: EnvOption[]
  onModel: (value: string) => void
}) {
  const [open, setOpen] = useState(false)
  const runnerLabel = runners.find(o => o.value === runner)?.label ?? runner
  const aiLabel = profiles.find(o => o.value === ai)?.label ?? ai
  const modelLabel = models.find(o => o.value === model)?.label ?? model
  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverTrigger asChild>
        <button
          type="button"
          className="env-picker__trigger"
          title={`Runner: ${runnerLabel} · AI: ${aiLabel} · Model: ${modelLabel}`}
          aria-label="Runner, AI and model"
        >
          <IconSettings size={16} />
        </button>
      </PopoverTrigger>
      <PopoverContent side="top" align="start" className="env-picker">
        <Section title="Runner" value={runner} options={runners} onChange={onRunner} />
        <div className="env-picker__divider" />
        <Section title="AI" value={ai} options={profiles} onChange={onAI} />
        <div className="env-picker__divider" />
        <Section title="Model" value={model} options={models} onChange={onModel} />
      </PopoverContent>
    </Popover>
  )
}
