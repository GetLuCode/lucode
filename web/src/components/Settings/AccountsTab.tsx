import { IconCheck, IconChevronRight, IconFolder, IconFolderCheck, IconPlus, IconTrash, IconX } from '@tabler/icons-react'
import { useEffect, useState } from 'react'
import * as api from '../../api'
import { useStore } from '../../store'
import type { Account } from '../../types'
import { AccountDetail } from './AccountDetail'

export function AccountsTab() {
  const { accounts, loadAccounts, createAccount, deleteAccount } = useStore()
  const [selectedAccount, setSelectedAccount] = useState<Account | null>(null)

  const [showAddForm, setShowAddForm] = useState(false)
  const [name, setName] = useState('')
  const [currentPath, setCurrentPath] = useState('')
  const [root, setRoot] = useState('')
  const [dirs, setDirs] = useState<string[]>([])
  const [loadingDirs, setLoadingDirs] = useState(false)
  const [hasCredentials, setHasCredentials] = useState(false)
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => {
    loadAccounts()
  }, [])

  useEffect(() => {
    if (showAddForm && !currentPath) {
      loadHomeDirs('')
    }
  }, [showAddForm])

  // Auto-fill name from last path segment
  useEffect(() => {
    if (!currentPath || !root) return
    const rel = currentPath.startsWith(root) ? currentPath.slice(root.length).replace(/^\//, '') : ''
    const segments = rel ? rel.split('/').filter(Boolean) : []
    const last = segments[segments.length - 1] || root.split('/').filter(Boolean).pop() || ''
    setName(last)
  }, [currentPath, root])

  async function loadHomeDirs(path: string) {
    setLoadingDirs(true)
    setError('')
    try {
      const result = await api.listHomeDirs(path || '')
      if (!root) setRoot(result.root)
      setCurrentPath(result.path)
      setDirs(result.dirs || [])
      setHasCredentials(result.has_credentials)
    } catch (err) {
      setError((err as Error).message || 'Failed to load directories')
    } finally {
      setLoadingDirs(false)
    }
  }

  async function handleNavigate(dirName: string) {
    const newPath = currentPath.endsWith('/') ? `${currentPath}${dirName}` : `${currentPath}/${dirName}`
    await loadHomeDirs(newPath)
  }

  async function handleCreate() {
    if (!name.trim() || !currentPath) {
      setError('Name and directory are required')
      return
    }
    if (!hasCredentials) {
      setError('Selected directory has no .credentials.json file')
      return
    }
    setSaving(true)
    setError('')
    try {
      await createAccount({ name: name.trim(), claude_config_dir: currentPath })
      setName('')
      setShowAddForm(false)
      setCurrentPath('')
      setRoot('')
      setDirs([])
      setHasCredentials(false)
    } catch (err) {
      setError((err as Error).message || 'Failed to create account')
    } finally {
      setSaving(false)
    }
  }

  function handleCancelAdd() {
    setShowAddForm(false)
    setName('')
    setCurrentPath('')
    setRoot('')
    setDirs([])
    setHasCredentials(false)
    setError('')
  }

  async function handleDelete(e: React.MouseEvent, id: string) {
    e.stopPropagation()
    try {
      await deleteAccount(id)
      if (selectedAccount?.id === id) setSelectedAccount(null)
    } catch (err) {
      setError((err as Error).message || 'Failed to delete account')
    }
  }

  if (selectedAccount) {
    return (
      <AccountDetail
        account={selectedAccount}
        onBack={() => setSelectedAccount(null)}
      />
    )
  }

  const relativePath = root && currentPath.startsWith(root)
    ? currentPath.slice(root.length).replace(/^\//, '')
    : ''
  const segments = relativePath ? relativePath.split('/').filter(Boolean) : []

  function badgeClass(sub: string) {
    if (sub === 'pro') return 'accounts-tab__badge accounts-tab__badge--pro'
    if (sub === 'max' || sub === 'max_5x' || sub === 'max_20x') return 'accounts-tab__badge accounts-tab__badge--max'
    return 'accounts-tab__badge accounts-tab__badge--unknown'
  }

  function badgeLabel(sub: string) {
    if (!sub) return 'unknown'
    return sub.replace(/_/g, ' ')
  }

  return (
    <div>
      <div className="accounts-tab__list">
        {accounts.length === 0 ? (
          <div className="accounts-tab__empty">No accounts added yet</div>
        ) : (
          accounts.map((a: Account) => (
            <div
              key={a.id}
              className="accounts-tab__item"
              onClick={() => setSelectedAccount(a)}
              role="button"
              tabIndex={0}
              onKeyDown={e => { if (e.key === 'Enter') setSelectedAccount(a) }}
            >
              <div className="accounts-tab__item-info">
                <div className="accounts-tab__item-name">{a.name}</div>
                <div className="accounts-tab__item-dir">{a.claude_config_dir}</div>
              </div>
              <span className={badgeClass(a.subscription_type)}>{badgeLabel(a.subscription_type)}</span>
              <button
                className="accounts-tab__delete"
                onClick={e => handleDelete(e, a.id)}
                aria-label="Delete account"
              >
                <IconTrash size={15} />
              </button>
            </div>
          ))
        )}
      </div>

      {!showAddForm ? (
        <button
          className="accounts-tab__add-btn"
          onClick={() => setShowAddForm(true)}
        >
          <IconPlus size={14} />
          Add Account
        </button>
      ) : (
        <div className="accounts-tab__add-panel">
          <div className="accounts-tab__add-panel-header">
            <span className="accounts-tab__add-panel-title">Add Account</span>
            <button className="accounts-tab__add-panel-close" onClick={handleCancelAdd} aria-label="Cancel">
              <IconX size={14} />
            </button>
          </div>

          <div className="accounts-tab__form">
            <div className="accounts-tab__field">
              <label>Directory</label>
              <div className="accounts-tab__dir-browser">
                <div className="accounts-tab__breadcrumb">
                  <button
                    className="accounts-tab__breadcrumb-btn"
                    onClick={() => loadHomeDirs(root)}
                    disabled={!relativePath}
                  >
                    ~
                  </button>
                  {segments.map((seg, i) => {
                    const pathUpToHere = root + '/' + segments.slice(0, i + 1).join('/')
                    const isLast = i === segments.length - 1
                    return (
                      <span key={i} style={{ display: 'flex', alignItems: 'center', gap: 2 }}>
                        <IconChevronRight size={12} style={{ color: 'var(--text-muted)' }} />
                        <button
                          className="accounts-tab__breadcrumb-btn"
                          onClick={() => !isLast && loadHomeDirs(pathUpToHere)}
                          disabled={isLast}
                        >
                          {seg}
                        </button>
                      </span>
                    )
                  })}
                  {hasCredentials && (
                    <span className="accounts-tab__cred-badge" title=".credentials.json found">
                      <IconCheck size={11} />
                      .credentials.json
                    </span>
                  )}
                </div>
                <div className="accounts-tab__dir-list">
                  {loadingDirs ? (
                    <div className="accounts-tab__dir-empty">Loading…</div>
                  ) : dirs.length === 0 ? (
                    <div className="accounts-tab__dir-empty">No subdirectories</div>
                  ) : (
                    dirs.map(dir => (
                      <button
                        key={dir}
                        className="accounts-tab__dir-item"
                        onClick={() => handleNavigate(dir)}
                      >
                        <IconFolder size={14} />
                        <span>{dir}</span>
                      </button>
                    ))
                  )}
                </div>
              </div>
              {!hasCredentials && currentPath && (
                <div className="accounts-tab__no-cred">
                  <IconFolderCheck size={13} />
                  No <code>.credentials.json</code> in this directory
                </div>
              )}
            </div>

            <div className="accounts-tab__field">
              <label>Name</label>
              <input
                type="text"
                className="input"
                placeholder="Account name"
                value={name}
                onChange={e => setName(e.target.value)}
                onKeyDown={e => { if (e.key === 'Enter') handleCreate() }}
              />
            </div>

            {error && <div style={{ color: '#f38ba8', fontSize: '0.8rem' }}>{error}</div>}

            <div className="accounts-tab__form-footer">
              <button className="btn btn--secondary" onClick={handleCancelAdd}>
                Cancel
              </button>
              <button
                className="btn btn--primary"
                onClick={handleCreate}
                disabled={saving || !name.trim() || !currentPath || !hasCredentials}
              >
                {saving ? 'Adding…' : 'Add Account'}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  )
}
