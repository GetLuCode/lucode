// Shared tool input renderers for hook approval modals and messages

export function ToolInputRenderer({ toolName, input }: { toolName: string; input: unknown }) {
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
    <div className="hook-input__fields">
      <pre className="hook-input__cmd"><span className="hook-input__cmd-prompt">$</span> {command}</pre>
      {description && <p className="hook-input__field-caption">{description}</p>}
    </div>
  )
}

function WriteInput({ data }: { data: Record<string, unknown> }) {
  const filePath = String(data.file_path ?? '')
  const content  = String(data.content   ?? '')
  return (
    <div className="hook-input__fields">
      <FilePath path={filePath} />
      <pre className="hook-input__code">{content}</pre>
    </div>
  )
}

function EditInput({ data }: { data: Record<string, unknown> }) {
  const filePath  = String(data.file_path  ?? '')
  const oldString = String(data.old_string ?? '')
  const newString = String(data.new_string ?? '')
  return (
    <div className="hook-input__fields">
      <FilePath path={filePath} />
      {oldString && (
        <div className="hook-input__diff">
          <span className="hook-input__diff-label hook-input__diff-label--remove">−</span>
          <pre className="hook-input__diff-block hook-input__diff-block--remove">{oldString}</pre>
        </div>
      )}
      {newString && (
        <div className="hook-input__diff">
          <span className="hook-input__diff-label hook-input__diff-label--add">+</span>
          <pre className="hook-input__diff-block hook-input__diff-block--add">{newString}</pre>
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
    <div className="hook-input__fields">
      {pattern && <pre className="hook-input__cmd"><span className="hook-input__cmd-prompt">/</span> {pattern}</pre>}
      <FilePath path={path || query} />
    </div>
  )
}

function JsonFallback({ input }: { input: unknown }) {
  return <pre className="hook-input__code">{JSON.stringify(input, null, 2)}</pre>
}

export function FilePath({ path }: { path: string }) {
  const parts = path.split('/')
  const file  = parts.pop() ?? ''
  const dir   = parts.join('/') || '/'
  return (
    <div className="hook-input__filepath">
      <span className="hook-input__filepath-dir">{dir}/</span>
      <span className="hook-input__filepath-file">{file}</span>
    </div>
  )
}
