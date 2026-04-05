import { motion, AnimatePresence } from 'framer-motion'
import type { PendingHook } from '../../types'

interface Props {
  hook: PendingHook | null
  queueLength: number
  onApprove: () => void
  onDeny: () => void
}

export function ToolApprovalModal({ hook, queueLength, onApprove, onDeny }: Props) {
  return (
    <AnimatePresence>
      {hook && (
        <motion.div
          className="modal-overlay"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
        >
          <motion.div
            className="modal"
            initial={{ opacity: 0, scale: 0.95, y: 12 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={{ opacity: 0, scale: 0.95, y: 12 }}
            transition={{ duration: 0.18, ease: 'easeOut' }}
          >
            <div className="modal__header">
              <div className="modal__icon">⚙</div>
              <div>
                <div className="modal__title">Tool Use Request</div>
                <div className="modal__subtitle">
                  PreToolUse{queueLength > 1 ? ` · ${queueLength} pending` : ''}
                </div>
              </div>
            </div>

            <div className="modal__tool-name">
              <span className="modal__tool-badge">{hook.tool_name}</span>
            </div>

            <div className="modal__body">
              <ToolInputRenderer toolName={hook.tool_name} input={hook.tool_input} />
            </div>

            <div className="modal__actions">
              <button className="btn btn--deny" onClick={onDeny}>Deny</button>
              <button className="btn btn--approve" onClick={onApprove}>Approve</button>
            </div>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  )
}

// ---------------------------------------------------------------------------
// Tool-specific renderers
// ---------------------------------------------------------------------------

function ToolInputRenderer({ toolName, input }: { toolName: string; input: unknown }) {
  const data = input as Record<string, unknown>

  switch (toolName) {
    case 'Bash':
      return <BashInput data={data} />
    case 'Write':
      return <WriteInput data={data} />
    case 'Edit':
      return <EditInput data={data} />
    case 'Read':
    case 'Glob':
    case 'Grep':
    case 'LS':
      return <FilePathInput toolName={toolName} data={data} />
    default:
      return <JsonFallback input={input} />
  }
}

function BashInput({ data }: { data: Record<string, unknown> }) {
  const command     = String(data.command     ?? '')
  const description = String(data.description ?? '')
  return (
    <div className="modal__fields">
      <pre className="modal__cmd"><span className="modal__cmd-prompt">$</span> {command}</pre>
      {description && <p className="modal__field-caption">{description}</p>}
    </div>
  )
}

function WriteInput({ data }: { data: Record<string, unknown> }) {
  const filePath = String(data.file_path ?? '')
  const content  = String(data.content   ?? '')
  return (
    <div className="modal__fields">
      <FilePath path={filePath} />
      <pre className="modal__code">{content}</pre>
    </div>
  )
}

function EditInput({ data }: { data: Record<string, unknown> }) {
  const filePath  = String(data.file_path  ?? '')
  const oldString = String(data.old_string ?? '')
  const newString = String(data.new_string ?? '')
  return (
    <div className="modal__fields">
      <FilePath path={filePath} />
      {oldString && (
        <div className="modal__diff">
          <span className="modal__diff-label modal__diff-label--remove">−</span>
          <pre className="modal__diff-block modal__diff-block--remove">{oldString}</pre>
        </div>
      )}
      {newString && (
        <div className="modal__diff">
          <span className="modal__diff-label modal__diff-label--add">+</span>
          <pre className="modal__diff-block modal__diff-block--add">{newString}</pre>
        </div>
      )}
    </div>
  )
}

function FilePathInput({ toolName, data }: { toolName: string; data: Record<string, unknown> }) {
  const path    = String(data.file_path ?? data.path ?? data.pattern ?? '')
  const pattern = toolName === 'Grep' ? String(data.pattern ?? '') : ''
  const query   = toolName === 'Grep' ? String(data.path    ?? '') : ''
  return (
    <div className="modal__fields">
      {pattern && <pre className="modal__cmd"><span className="modal__cmd-prompt">/</span> {pattern}</pre>}
      <FilePath path={path || query} />
    </div>
  )
}

function JsonFallback({ input }: { input: unknown }) {
  return <pre className="modal__code">{JSON.stringify(input, null, 2)}</pre>
}

function FilePath({ path }: { path: string }) {
  const parts = path.split('/')
  const file  = parts.pop() ?? ''
  const dir   = parts.join('/') || '/'
  return (
    <div className="modal__filepath">
      <span className="modal__filepath-dir">{dir}/</span>
      <span className="modal__filepath-file">{file}</span>
    </div>
  )
}
