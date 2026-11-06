import { useMemo, useState } from 'react'
import { IconHourglass, IconCheck, IconChevronDown, IconBan, IconRobot } from '@tabler/icons-react'
import { toolDecisionLabel } from './toolDecision'
import './ToolExecutionMessage.css'

interface ToolUsage {
  input_tokens?: number
  output_tokens?: number
  cache_creation_input_tokens?: number
  cache_read_input_tokens?: number
}

interface ToolExecutionData {
  tool_name: string
  tool_input: Record<string, unknown>
  tool_response?: Record<string, unknown>
  tool_use_id: string
  timestamp: string
  usage?: ToolUsage
  decision?: string
  decided_by?: string
  reason?: string
}

export function ToolExecutionMessage({ content }: { content: string }) {
  const [isExpanded, setIsExpanded] = useState(false)

  const toolData = useMemo(() => {
    try {
      const data = JSON.parse(content) as ToolExecutionData
      return data
    } catch {
      return null
    }
  }, [content])

  if (!toolData) {
    return <pre className="msg__code">{content}</pre>
  }

  const isComplete = !!toolData.tool_response
  const toolName = toolData.tool_name
  const usage = toolData.usage

  return (
    <div className="tool-execution">
      <button
        className="tool-execution__header"
        onClick={() => setIsExpanded(!isExpanded)}
        type="button"
      >
        <div className="tool-execution__icon-wrapper">
          {toolData.decision === 'deny' ? (
            <IconBan size={18} className="tool-execution__icon tool-execution__icon--denied" />
          ) : toolData.decision === 'auto' ? (
            <IconRobot size={18} className="tool-execution__icon tool-execution__icon--complete" />
          ) : isComplete || toolData.decision ? (
            <IconCheck size={18} className="tool-execution__icon tool-execution__icon--complete" />
          ) : (
            <div className="tool-execution__loading">
              <IconHourglass size={18} className="tool-execution__icon tool-execution__icon--loading" />
            </div>
          )}
        </div>
        <div className="tool-execution__tool-name">{toolName}</div>
        {toolData.decision
          ? <span className="tool-execution__status">{toolDecisionLabel(toolData.decision, toolData.decided_by)}</span>
          : isComplete && <span className="tool-execution__status">Done</span>}
        <IconChevronDown
          size={16}
          className={`tool-execution__chevron ${isExpanded ? 'tool-execution__chevron--expanded' : ''}`}
        />
      </button>

      {isExpanded && (
        <>
          {toolData.reason && <div className="tool-input__description">{toolData.reason}</div>}
          <ToolInputDisplay toolName={toolName} input={toolData.tool_input} />

          {isComplete && toolData.tool_response && (
            <ToolOutputDisplay toolName={toolName} output={toolData.tool_response} input={toolData.tool_input} />
          )}
        </>
      )}
      {usage && <ToolUsageBadge usage={usage} />}
    </div>
  )
}

function fmtTok(n: number): string {
  return n >= 1000 ? `${(n / 1000).toFixed(1)}k` : String(n)
}

function ToolUsageBadge({ usage }: { usage: ToolUsage }) {
  const parts: string[] = []
  if (usage.input_tokens)                parts.push(`↑${fmtTok(usage.input_tokens)}`)
  if (usage.output_tokens)               parts.push(`↓${fmtTok(usage.output_tokens)}`)
  if (usage.cache_creation_input_tokens) parts.push(`cache✎${fmtTok(usage.cache_creation_input_tokens)}`)
  if (usage.cache_read_input_tokens)     parts.push(`cache↩${fmtTok(usage.cache_read_input_tokens)}`)
  if (parts.length === 0) return null
  return <div className="tool-execution__usage">{parts.join('  ·  ')}</div>
}

function ToolInputDisplay({ toolName, input }: { toolName: string; input: Record<string, unknown> }) {
  const getInputSummary = (): React.ReactNode => {
    switch (toolName) {
      case 'Bash':
        return (
          <div className="tool-input__bash">
            <div className="tool-input__label">Command:</div>
            <code className="tool-input__code">{String(input.command)}</code>
            {input.description ? (
              <div className="tool-input__description">{String(input.description)}</div>
            ) : null}
          </div>
        )
      case 'Grep':
        return (
          <div className="tool-input__grep">
            <div className="tool-input__label">Pattern:</div>
            <code className="tool-input__code">{String(input.pattern)}</code>
            {input.path ? (
              <>
                <div className="tool-input__label">Path:</div>
                <div className="tool-input__path">{String(input.path)}</div>
              </>
            ) : null}
          </div>
        )
      case 'Edit':
        return (
          <div className="tool-input__edit">
            <div className="tool-input__label">File:</div>
            <div className="tool-input__path">{String(input.file_path)}</div>
            {input.description ? (
              <div className="tool-input__description">{String(input.description)}</div>
            ) : null}
          </div>
        )
      case 'Read':
        return (
          <div className="tool-input__read">
            <div className="tool-input__label">File:</div>
            <div className="tool-input__path">{String(input.file_path)}</div>
          </div>
        )
      case 'Write':
        return (
          <div className="tool-input__write">
            <div className="tool-input__label">File:</div>
            <div className="tool-input__path">{String(input.file_path)}</div>
          </div>
        )
      default:
        return (
          <div className="tool-input__default">
            <pre className="tool-input__json">{JSON.stringify(input, null, 2)}</pre>
          </div>
        )
    }
  }

  return <div className="tool-input">{getInputSummary()}</div>
}

function ToolOutputDisplay({
  toolName,
  output,
  input,
}: {
  toolName: string
  output: Record<string, unknown>
  input: Record<string, unknown>
}) {
  const getOutputDisplay = () => {
    switch (toolName) {
      case 'Bash': {
        const stdout = output.stdout as string | undefined
        const stderr = output.stderr as string | undefined
        const exitCode = output.exit_code as number | undefined
        return (
          <div className="tool-output__bash">
            {stdout && (
              <div className="tool-output__section">
                <div className="tool-output__label">Output:</div>
                <pre className="tool-output__code">{stdout}</pre>
              </div>
            )}
            {stderr && (
              <div className="tool-output__section tool-output__section--error">
                <div className="tool-output__label">Error:</div>
                <pre className="tool-output__code">{stderr}</pre>
              </div>
            )}
            {exitCode !== undefined && (
              <div className="tool-output__exit-code">Exit code: {exitCode}</div>
            )}
          </div>
        )
      }
      case 'Grep': {
        const matches = output.matches as string | undefined
        const count = output.count as number | undefined
        return (
          <div className="tool-output__grep">
            {count !== undefined && (
              <div className="tool-output__count">Found {count} matches</div>
            )}
            {matches && (
              <div className="tool-output__section">
                <pre className="tool-output__code">{matches}</pre>
              </div>
            )}
          </div>
        )
      }
      case 'Edit': {
        const oldString = input.old_string as string | undefined
        const newString = input.new_string as string | undefined
        return (
          <div className="tool-output__edit">
            <div className="tool-output__label">Changes:</div>
            <DiffViewer oldText={oldString || ''} newText={newString || ''} />
          </div>
        )
      }
      case 'Read': {
        const content = output.content as string | undefined
        return (
          <div className="tool-output__read">
            <div className="tool-output__section">
              <pre className="tool-output__code">{content}</pre>
            </div>
          </div>
        )
      }
      default:
        return (
          <div className="tool-output__default">
            <pre className="tool-output__json">{JSON.stringify(output, null, 2)}</pre>
          </div>
        )
    }
  }

  return <div className="tool-output">{getOutputDisplay()}</div>
}

function DiffViewer({ oldText, newText }: { oldText: string; newText: string }) {
  // Simple line-by-line diff display
  const oldLines = oldText.split('\n')
  const newLines = newText.split('\n')

  return (
    <div className="diff-viewer">
      <div className="diff-viewer__container">
        <div className="diff-viewer__column diff-viewer__column--removed">
          <div className="diff-viewer__header">Removed</div>
          <pre className="diff-viewer__content">
            {oldLines.map((line, i) => (
              <div key={i} className="diff-viewer__line diff-viewer__line--removed">
                {line}
              </div>
            ))}
          </pre>
        </div>
        <div className="diff-viewer__column diff-viewer__column--added">
          <div className="diff-viewer__header">Added</div>
          <pre className="diff-viewer__content">
            {newLines.map((line, i) => (
              <div key={i} className="diff-viewer__line diff-viewer__line--added">
                {line}
              </div>
            ))}
          </pre>
        </div>
      </div>
    </div>
  )
}
