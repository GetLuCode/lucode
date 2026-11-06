// Shared tool input renderers for hook approval modals and messages
import { IconEye, IconGitCompare } from '@tabler/icons-react'
import { useStore } from '../../store'
import { splitPath } from './hookSummary'

export function ToolInputRenderer({ toolName, input }: { toolName: string; input: unknown }) {
  const data = input as Record<string, unknown>

  switch (toolName) {
    case 'Bash':
      return <BashInput data={data} />
    case 'Write':
      return <WriteInput data={data} />
    case 'Edit':
      return <EditInput data={data} />
    case 'MultilineEdit':
      return <EditInput data={data} />
    case 'Read':
    case 'Glob':
    case 'Grep':
    case 'LS':
      return <FilePathInput toolName={toolName} data={data} />
    case 'Skill':
      return <SkillInput data={data} />
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

function WriteInput({ data }: { data: Record<string, unknown> }) {
  const filePath = String(data.file_path ?? '')
  const content  = String(data.content   ?? '')
  const openFilePreview = useStore(s => s.openFilePreview)

  return (
    <div className="hook-input__fields">
      <FilePath path={filePath} />
      <button
        className="hook-input__preview-btn"
        type="button"
        onClick={() => openFilePreview({ filePath, lang: detectLanguage(filePath), mode: 'content', content })}
      >
        <IconEye size={13} aria-hidden="true" />
        View content
      </button>
    </div>
  )
}

function EditInput({ data }: { data: Record<string, unknown> }) {
  const filePath  = String(data.file_path  ?? '')
  const oldString = String(data.old_string ?? '')
  const newString = String(data.new_string ?? '')
  const openFilePreview = useStore(s => s.openFilePreview)

  const handleOpen = () => {
    openFilePreview({
      filePath,
      lang: detectLanguage(filePath),
      mode: 'diff',
      original: oldString,
      modified: newString,
    })
  }

  return (
    <div className="hook-input__fields">
      <FilePath path={filePath} />
      <button className="hook-input__preview-btn" type="button" onClick={handleOpen}>
        <IconGitCompare size={13} aria-hidden="true" />
        View modifications
      </button>
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

function SkillInput({ data }: { data: Record<string, unknown> }) {
  const skill = String(data.skill ?? '')
  const args  = String(data.args  ?? '')
  return (
    <div className="hook-input__skill">
      <span className="hook-input__skill-name">{skill}</span>
      {args && <p className="hook-input__skill-args">{args}</p>}
    </div>
  )
}

function JsonFallback({ input }: { input: unknown }) {
  return <pre className="hook-input__code">{JSON.stringify(input, null, 2)}</pre>
}

export function FilePath({ path }: { path: string }) {
  const { dir, file } = splitPath(path)
  return (
    <div className="hook-input__filepath">
      <span className="hook-input__filepath-dir">{dir}</span>
      <span className="hook-input__filepath-file">{file}</span>
    </div>
  )
}
