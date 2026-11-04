import { useState } from 'react'
import { IconCopy, IconCheck } from '@tabler/icons-react'

// Copies an id (session or task) to the clipboard; the icon turns into a check for a moment.
export function CopyId({ id, label }: { id: string; label: string }) {
  const [copied, setCopied] = useState(false)

  const copy = async () => {
    try {
      await navigator.clipboard.writeText(id)
    } catch {
      // Clipboard API unavailable (insecure context): fall back to a hidden textarea.
      const ta = document.createElement('textarea')
      ta.value = id
      document.body.appendChild(ta)
      ta.select()
      document.execCommand('copy')
      ta.remove()
    }
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <button
      className="claude-proc__menu-btn"
      onClick={copy}
      title={copied ? 'Copied' : `Copy ${label} id (${id})`}
      aria-label={`Copy ${label} id`}
    >
      {copied ? <IconCheck size={14} /> : <IconCopy size={14} />}
    </button>
  )
}

export function CopySessionId({ sessionId }: { sessionId: string }) {
  return <CopyId id={sessionId} label="session" />
}
