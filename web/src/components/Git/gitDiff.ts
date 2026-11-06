// Reads the unified diff git prints into lines with their numbers, and pairs them for the split
// view. Pure, for the tests.

export type DiffLineKind = 'add' | 'del' | 'ctx' | 'hunk' | 'note'

export interface DiffLine {
  kind: DiffLineKind
  text: string
  oldNo?: number
  newNo?: number
}

export interface ParsedDiff {
  lines: DiffLine[]
  binary: boolean
  notes: string[] // new file, rename, mode change: what the header says that the lines cannot
}

const HUNK = /^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@(.*)$/

export function parseDiff(text: string): ParsedDiff {
  const res: ParsedDiff = { lines: [], binary: false, notes: [] }
  let oldNo = 0
  let newNo = 0
  let inHunk = false
  for (const raw of text.split('\n')) {
    const line = raw.endsWith('\r') ? raw.slice(0, -1) : raw
    const hunk = HUNK.exec(line)
    if (hunk) {
      oldNo = Number(hunk[1])
      newNo = Number(hunk[2])
      inHunk = true
      res.lines.push({ kind: 'hunk', text: hunk[3].trim() })
      continue
    }
    if (line.startsWith('diff --git ')) {
      inHunk = false
      continue
    }
    if (!inHunk) {
      if (line.startsWith('Binary files ') || line === 'GIT binary patch') res.binary = true
      else if (/^(new file|deleted file|old mode|new mode|similarity index|rename from|rename to|copy from|copy to)/.test(line)) res.notes.push(line)
      continue
    }
    if (line.startsWith('+')) res.lines.push({ kind: 'add', text: line.slice(1), newNo: newNo++ })
    else if (line.startsWith('-')) res.lines.push({ kind: 'del', text: line.slice(1), oldNo: oldNo++ })
    else if (line.startsWith('\\')) res.lines.push({ kind: 'note', text: line.slice(2) })
    else if (line.startsWith(' ')) res.lines.push({ kind: 'ctx', text: line.slice(1), oldNo: oldNo++, newNo: newNo++ })
    // an empty string is what split leaves after the final newline
  }
  return res
}

export interface SplitRow {
  left?: DiffLine
  right?: DiffLine
  full?: DiffLine // a hunk header or a note: one line across both sides
}

// Removed lines sit beside the added lines of the same change.
export function toSplitRows(lines: DiffLine[]): SplitRow[] {
  const rows: SplitRow[] = []
  let dels: DiffLine[] = []
  let adds: DiffLine[] = []
  const flush = () => {
    for (let i = 0; i < Math.max(dels.length, adds.length); i++) rows.push({ left: dels[i], right: adds[i] })
    dels = []
    adds = []
  }
  for (const l of lines) {
    if (l.kind === 'del') {
      if (adds.length) flush()
      dels.push(l)
    } else if (l.kind === 'add') {
      adds.push(l)
    } else {
      flush()
      if (l.kind === 'ctx') rows.push({ left: l, right: l })
      else rows.push({ full: l })
    }
  }
  flush()
  return rows
}

const LANG_BY_EXT: Record<string, string> = {
  go: 'go', ts: 'typescript', tsx: 'typescript', js: 'javascript', jsx: 'javascript', mjs: 'javascript',
  py: 'python', rs: 'rust', java: 'java', kt: 'kotlin', swift: 'swift', rb: 'ruby', php: 'php',
  c: 'c', h: 'c', cpp: 'cpp', cc: 'cpp', hpp: 'cpp', cs: 'csharp', css: 'css', html: 'xml', xml: 'xml', svg: 'xml',
  json: 'json', yml: 'yaml', yaml: 'yaml', toml: 'ini', ini: 'ini', md: 'markdown', sql: 'sql', sh: 'bash', bash: 'bash', ps1: 'powershell',
}

export function languageOf(path: string): string {
  const name = path.split('/').pop() ?? ''
  if (name.toLowerCase() === 'dockerfile') return 'dockerfile'
  return LANG_BY_EXT[name.split('.').pop()?.toLowerCase() ?? ''] ?? ''
}
