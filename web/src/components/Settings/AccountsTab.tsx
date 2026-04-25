import { IconKey, IconPlus, IconTrash, IconX } from '@tabler/icons-react'
import { useEffect, useState } from 'react'
import { useStore } from '../../store'
import type { Account } from '../../types'
import { AccountDetail } from './AccountDetail'

export function AccountsTab() {
  const { accounts, loadAccounts, createAccount, deleteAccount } = useStore()
  const [selectedAccount, setSelectedAccount] = useState<Account | null>(null)

  const [showAddForm, setShowAddForm] = useState(false)
  const [name, setName] = useState('')
  const [claudeConfigDir, setClaudeConfigDir] = useState('')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  useEffect(() => { loadAccounts() }, [])

  async function handleCreate() {
    if (!name.trim()) { setError('Name is required'); return }
    if (!claudeConfigDir.trim()) { setError('Claude config dir is required'); return }
    setSaving(true)
    setError('')
    try {
      await createAccount({
        name: name.trim(),
        claude_config_dir: claudeConfigDir.trim(),
      })
      resetForm()
    } catch (err) {
      setError((err as Error).message || 'Failed to create account')
    } finally {
      setSaving(false)
    }
  }

  function resetForm() {
    setShowAddForm(false)
    setName('')
    setClaudeConfigDir('')
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

  function tokenStatus(a: Account) {
    if (!a.has_token) return { label: 'no token', cls: 'accounts-tab__badge accounts-tab__badge--unknown' }
    if (a.expires_at) {
      const ttl = a.expires_at - Date.now()
      if (ttl < 0) return { label: 'expired', cls: 'accounts-tab__badge accounts-tab__badge--expired' }
      if (ttl < 5 * 60 * 1000) return { label: 'expiring soon', cls: 'accounts-tab__badge accounts-tab__badge--expiring' }
    }
    return { label: 'active', cls: 'accounts-tab__badge accounts-tab__badge--pro' }
  }

  return (
    <div>
      <div className="accounts-tab__list">
        {accounts.length === 0 ? (
          <div className="accounts-tab__empty">No accounts added yet</div>
        ) : (
          accounts.map((a: Account) => {
            const ts = tokenStatus(a)
            return (
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
                  {a.expires_at ? (
                    <div className="accounts-tab__item-dir">
                      expires {new Date(a.expires_at).toLocaleString()}
                    </div>
                  ) : (
                    <div className="accounts-tab__item-dir">no expiry set</div>
                  )}
                </div>
                <span className={ts.cls}>{ts.label}</span>
                <button
                  className="accounts-tab__delete"
                  onClick={e => handleDelete(e, a.id)}
                  aria-label="Delete account"
                >
                  <IconTrash size={15} />
                </button>
              </div>
            )
          })
        )}
      </div>

      {!showAddForm ? (
        <button className="accounts-tab__add-btn" onClick={() => setShowAddForm(true)}>
          <IconPlus size={14} />
          Add Account
        </button>
      ) : (
        <div className="accounts-tab__add-panel">
          <div className="accounts-tab__add-panel-header">
            <span className="accounts-tab__add-panel-title">Add Account</span>
            <button className="accounts-tab__add-panel-close" onClick={resetForm} aria-label="Cancel">
              <IconX size={14} />
            </button>
          </div>

          <div className="accounts-tab__form">
            <div className="accounts-tab__field">
              <label>Name</label>
              <input
                type="text"
                className="input"
                placeholder="e.g. personal, work"
                value={name}
                onChange={e => setName(e.target.value)}
                autoFocus
              />
            </div>

            <div className="accounts-tab__field">
              <label>
                <IconKey size={12} style={{ marginRight: 4, verticalAlign: 'middle' }} />
                Claude Config Dir
              </label>
              <input
                type="text"
                className="input"
                placeholder="/path/to/.claude (optional)"
                value={claudeConfigDir}
                onChange={e => setClaudeConfigDir(e.target.value)}
              />
              <div className="accounts-tab__field-hint">
                Path to Claude config directory (e.g., <code>~/.claude</code>)
              </div>
            </div>

            {error && <div style={{ color: '#f38ba8', fontSize: '0.8rem' }}>{error}</div>}

            <div className="accounts-tab__form-footer">
              <button className="btn btn--secondary" onClick={resetForm}>Cancel</button>
              <button
                className="btn btn--primary"
                onClick={handleCreate}
                disabled={saving || !name.trim() || !claudeConfigDir.trim()}
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
