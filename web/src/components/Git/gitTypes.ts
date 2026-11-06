// What the runner answers at /api/git/* (internal/api/gitclient.go), relayed by the hub.

export interface GitTarget {
  runner_id: string
  hostname: string
  os: string
  online: boolean
  repo: string
}

export interface GitTargets {
  targets: GitTarget[]
  reason?: string
}

export interface GitChange {
  path: string
  orig_path?: string
  index: string    // status against HEAD; "." = none
  worktree: string // status against the index; "." = none, "?" = untracked
  conflict?: boolean
  untracked?: boolean
  staged_add?: number
  staged_del?: number
  unstaged_add?: number
  unstaged_del?: number
}

export interface GitBranch {
  name: string
  remote?: boolean
  sha: string
  upstream?: string
  upstream_gone?: boolean
  ahead?: number
  behind?: number
  main_ahead: number
  main_behind: number
  at: number
  author: string
  subject: string
  current?: boolean
  worktree_path?: string
}

export interface GitTag { name: string; sha: string; at: number; subject: string }
export interface GitStash { ref: string; sha: string; message: string; at: number; author: string }
export interface GitWorktreeBrief { path: string; branch?: string; main?: boolean }

export interface GitOverview {
  not_repo?: boolean
  repo: string
  dir: string
  branch: string
  detached: boolean
  unborn?: boolean
  head: string
  upstream?: string
  ahead: number
  behind: number
  state?: string
  main: string
  changes: GitChange[]
  truncated?: boolean
  branches: GitBranch[]
  tags: GitTag[]
  stashes: GitStash[]
  worktrees: GitWorktreeBrief[]
  remotes: string[]
  user_name?: string
  user_email?: string
}

export interface GitRef { name: string; kind: 'branch' | 'remote' | 'tag' | 'head'; head?: boolean }

export interface GitFileStat {
  path: string
  orig_path?: string
  status: string
  added: number
  removed: number
  binary?: boolean
}

export interface GitCommit {
  sha: string
  parents: string[]
  author: string
  email: string
  at: number
  committer: string
  commit_at: number
  refs: GitRef[]
  subject: string
  body?: string
  files: GitFileStat[]
  files_total: number
  added: number
  removed: number
}

export interface GitLog { commits: GitCommit[]; more: boolean }
export interface GitDiffAnswer { text: string; truncated?: boolean }

export interface GitCompare {
  base: string
  head: string
  merge_base: string
  ahead: number
  behind: number
  commits: GitCommit[]
  base_commits: GitCommit[]
  files: GitFileStat[]
  files_total: number
  added: number
  removed: number
  conflicts: boolean
  conflict_files: string[]
}

export interface GitStashFiles { files: GitFileStat[]; untracked: GitFileStat[] }

export interface GitMergeRef {
  into: string
  sha?: string
  subject?: string
  author?: string
  at?: number
  kind: 'merge' | 'contained' | 'squash'
}

export interface GitTaskLink {
  card_id: string
  title: string
  list: string
  list_kind: string
  agent?: string
  session_id?: string
  running?: boolean
  branch: string
  worktree?: string
  merged_into?: string
  merged_at?: string
  merge?: GitMergeRef
}

export interface GitWorktree {
  path: string
  branch?: string
  head: string
  detached?: boolean
  main?: boolean
  locked?: boolean
  lock_reason?: string
  prunable?: boolean
  missing?: boolean
  dirty: number
  ahead: number
  behind: number
  subject?: string
  author?: string
  at?: number
  merge?: GitMergeRef
  task?: GitTaskLink
}

export interface GitWorktrees {
  main: string
  worktrees: GitWorktree[]
  merged_tasks: GitTaskLink[]
}

// What a diff pane is asked to show.
export type DiffRequest =
  | { mode: 'worktree' | 'staged' | 'untracked'; path: string; orig_path?: string }
  | { mode: 'commit'; sha: string; path: string; orig_path?: string }
  | { mode: 'range'; base: string; head: string; path: string; orig_path?: string }
  | { mode: 'stash'; sha: string; path: string; untracked?: boolean }
