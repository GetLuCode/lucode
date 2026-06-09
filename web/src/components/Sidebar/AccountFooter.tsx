import { useEffect, useRef, useState } from 'react'
import { IconEdit, IconPlus, IconTrash, IconX } from '@tabler/icons-react'
import * as api from '../../api'
import type { AccountProfile, AccountUsage } from '../../api'
import { useStore } from '../../store'
import type { Account } from '../../types'

// ─── helpers ─────────────────────────────────────────────────────────────────

function md5(str: string): string {
  // Simple MD5 for Gravatar — use a tiny pure-JS implementation
  // Based on https://github.com/blueimp/JavaScript-MD5 (MIT)
  function safeAdd(x: number, y: number) {
    const lsw = (x & 0xffff) + (y & 0xffff)
    const msw = (x >> 16) + (y >> 16) + (lsw >> 16)
    return (msw << 16) | (lsw & 0xffff)
  }
  function bitRotateLeft(num: number, cnt: number) {
    return (num << cnt) | (num >>> (32 - cnt))
  }
  function md5cmn(q: number, a: number, b: number, x: number, s: number, t: number) {
    return safeAdd(bitRotateLeft(safeAdd(safeAdd(a, q), safeAdd(x, t)), s), b)
  }
  function md5ff(a: number, b: number, c: number, d: number, x: number, s: number, t: number) {
    return md5cmn((b & c) | (~b & d), a, b, x, s, t)
  }
  function md5gg(a: number, b: number, c: number, d: number, x: number, s: number, t: number) {
    return md5cmn((b & d) | (c & ~d), a, b, x, s, t)
  }
  function md5hh(a: number, b: number, c: number, d: number, x: number, s: number, t: number) {
    return md5cmn(b ^ c ^ d, a, b, x, s, t)
  }
  function md5ii(a: number, b: number, c: number, d: number, x: number, s: number, t: number) {
    return md5cmn(c ^ (b | ~d), a, b, x, s, t)
  }
  function calculateMD5(n: number[]): number[] {
    const m = n.slice()
    let a = 1732584193, b = -271733879, c = -1732584194, d = 271733878
    for (let i = 0; i < m.length; i += 16) {
      const [aa, bb, cc, dd] = [a, b, c, d]
      a = md5ff(a, b, c, d, m[i], 7, -680876936); d = md5ff(d, a, b, c, m[i+1], 12, -389564586)
      c = md5ff(c, d, a, b, m[i+2], 17, 606105819); b = md5ff(b, c, d, a, m[i+3], 22, -1044525330)
      a = md5ff(a, b, c, d, m[i+4], 7, -176418897); d = md5ff(d, a, b, c, m[i+5], 12, 1200080426)
      c = md5ff(c, d, a, b, m[i+6], 17, -1473231341); b = md5ff(b, c, d, a, m[i+7], 22, -45705983)
      a = md5ff(a, b, c, d, m[i+8], 7, 1770035416); d = md5ff(d, a, b, c, m[i+9], 12, -1958414417)
      c = md5ff(c, d, a, b, m[i+10], 17, -42063); b = md5ff(b, c, d, a, m[i+11], 22, -1990404162)
      a = md5ff(a, b, c, d, m[i+12], 7, 1804603682); d = md5ff(d, a, b, c, m[i+13], 12, -40341101)
      c = md5ff(c, d, a, b, m[i+14], 17, -1502002290); b = md5ff(b, c, d, a, m[i+15], 22, 1236535329)
      a = md5gg(a, b, c, d, m[i+1], 5, -165796510); d = md5gg(d, a, b, c, m[i+6], 9, -1069501632)
      c = md5gg(c, d, a, b, m[i+11], 14, 643717713); b = md5gg(b, c, d, a, m[i+0], 20, -373897302)
      a = md5gg(a, b, c, d, m[i+5], 5, -701558691); d = md5gg(d, a, b, c, m[i+10], 9, 38016083)
      c = md5gg(c, d, a, b, m[i+15], 14, -660478335); b = md5gg(b, c, d, a, m[i+4], 20, -405537848)
      a = md5gg(a, b, c, d, m[i+9], 5, 568446438); d = md5gg(d, a, b, c, m[i+14], 9, -1019803690)
      c = md5gg(c, d, a, b, m[i+3], 14, -187363961); b = md5gg(b, c, d, a, m[i+8], 20, 1163531501)
      a = md5gg(a, b, c, d, m[i+13], 5, -1444681467); d = md5gg(d, a, b, c, m[i+2], 9, -51403784)
      c = md5gg(c, d, a, b, m[i+7], 14, 1735328473); b = md5gg(b, c, d, a, m[i+12], 20, -1926607734)
      a = md5hh(a, b, c, d, m[i+5], 4, -378558); d = md5hh(d, a, b, c, m[i+8], 11, -2022574463)
      c = md5hh(c, d, a, b, m[i+11], 16, 1839030562); b = md5hh(b, c, d, a, m[i+14], 23, -35309556)
      a = md5hh(a, b, c, d, m[i+1], 4, -1530992060); d = md5hh(d, a, b, c, m[i+4], 11, 1272893353)
      c = md5hh(c, d, a, b, m[i+7], 16, -155497632); b = md5hh(b, c, d, a, m[i+10], 23, -1094730640)
      a = md5hh(a, b, c, d, m[i+13], 4, 681279174); d = md5hh(d, a, b, c, m[i+0], 11, -358537222)
      c = md5hh(c, d, a, b, m[i+3], 16, -722521979); b = md5hh(b, c, d, a, m[i+6], 23, 76029189)
      a = md5hh(a, b, c, d, m[i+9], 4, -640364487); d = md5hh(d, a, b, c, m[i+12], 11, -421815835)
      c = md5hh(c, d, a, b, m[i+15], 16, 530742520); b = md5hh(b, c, d, a, m[i+2], 23, -995338651)
      a = md5ii(a, b, c, d, m[i+0], 6, -198630844); d = md5ii(d, a, b, c, m[i+7], 10, 1126891415)
      c = md5ii(c, d, a, b, m[i+14], 15, -1416354905); b = md5ii(b, c, d, a, m[i+5], 21, -57434055)
      a = md5ii(a, b, c, d, m[i+12], 6, 1700485571); d = md5ii(d, a, b, c, m[i+3], 10, -1894986606)
      c = md5ii(c, d, a, b, m[i+10], 15, -1051523); b = md5ii(b, c, d, a, m[i+1], 21, -2054922799)
      a = md5ii(a, b, c, d, m[i+8], 6, 1873313359); d = md5ii(d, a, b, c, m[i+15], 10, -30611744)
      c = md5ii(c, d, a, b, m[i+6], 15, -1560198380); b = md5ii(b, c, d, a, m[i+13], 21, 1309151649)
      a = md5ii(a, b, c, d, m[i+4], 6, -145523070); d = md5ii(d, a, b, c, m[i+11], 10, -1120210379)
      c = md5ii(c, d, a, b, m[i+2], 15, 718787259); b = md5ii(b, c, d, a, m[i+9], 21, -343485551)
      a = safeAdd(a, aa); b = safeAdd(b, bb); c = safeAdd(c, cc); d = safeAdd(d, dd)
    }
    return [a, b, c, d]
  }
  function utf8Encode(s: string): number[] {
    const bytes: number[] = []
    for (let i = 0; i < s.length; i++) {
      const c = s.charCodeAt(i)
      if (c < 128) { bytes.push(c) }
      else if (c < 2048) { bytes.push((c >> 6) | 192, (c & 63) | 128) }
      else { bytes.push((c >> 12) | 224, ((c >> 6) & 63) | 128, (c & 63) | 128) }
    }
    return bytes
  }
  function wordsToHex(n: number[]): string {
    let hex = ''
    for (let i = 0; i < n.length; i++) {
      for (let j = 0; j <= 3; j++) {
        const b = (n[i] >> (j * 8)) & 0xff
        hex += ('0' + b.toString(16)).slice(-2)
      }
    }
    return hex
  }
  function str2binl(s: number[]): number[] {
    const bin: number[] = Array(Math.ceil(s.length / 4)).fill(0)
    for (let i = 0; i < s.length; i++) bin[i >> 2] |= s[i] << ((i % 4) * 8)
    const len = s.length
    bin[len >> 2] |= 0x80 << ((len % 4) * 8)
    bin[(((len + 64) >> 9) << 4) + 14] = len * 8
    return bin
  }
  return wordsToHex(calculateMD5(str2binl(utf8Encode(str.trim().toLowerCase()))))
}

function gravatarUrl(email: string, size = 56): string {
  return `https://www.gravatar.com/avatar/${md5(email)}?s=${size}&d=404`
}

function formatResetsAt(iso: string): { short: string; exact: string } {
  const date = new Date(iso)
  const diff = date.getTime() - Date.now()
  const exact = date.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' })
  if (diff <= 0) return { short: 'resetting…', exact }
  const totalMin = Math.floor(diff / 60000)
  const h = Math.floor(totalMin / 60)
  const m = totalMin % 60
  const daysLeft = Math.floor(h / 24)
  let short: string
  if (h === 0) short = `${m}m`
  else if (daysLeft === 0) short = `${h}h ${m}m`
  else if (daysLeft === 1) short = `tomorrow ${date.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' })}`
  else short = `in ${daysLeft}d`
  return { short, exact }
}

type AccountStatus = 'available' | 'limited' | 'exhausted'

function getStatus(usage: AccountUsage | null): AccountStatus {
  if (!usage) return 'available'
  const fh = usage.five_hour?.utilization ?? 0
  const sd = usage.seven_day?.utilization ?? 0
  if (fh >= 100 || sd >= 100) return 'exhausted'
  if (fh >= 70 || sd >= 70) return 'limited'
  return 'available'
}

function statusEmoji(status: AccountStatus) {
  if (status === 'exhausted') return { emoji: '🔴', label: 'Exhausted', cls: 'exhausted' }
  if (status === 'limited')   return { emoji: '🟡', label: 'Limited',   cls: 'limited'   }
  return                             { emoji: '🟢', label: 'Available', cls: 'available'  }
}

function activeMode(usage: AccountUsage | null): string | null {
  if (!usage) return null
  const fh = usage.five_hour?.utilization ?? 0
  const sd = usage.seven_day?.utilization ?? 0
  const extra = usage.extra_usage
  if (fh >= sd && fh > 0) return '5h window'
  if (sd > 0) return '7d window'
  if (extra?.is_enabled && (extra.utilization ?? 0) > 0) return 'billing'
  return null
}

const barFill = (pct: number) => Math.min(100, Math.max(0, pct))
function barColor(pct: number) {
  if (pct >= 100) return 'var(--usage-exhausted, #f38ba8)'
  if (pct >= 70)  return 'var(--usage-limited,   #fab387)'
  return                  'var(--usage-ok,        #a6e3a1)'
}

// ─── Avatar with Gravatar + initial fallback ──────────────────────────────────

function Avatar({ email, initial, size = 28, className = '' }: {
  email?: string
  initial: string
  size?: number
  className?: string
}) {
  const [imgFailed, setImgFailed] = useState(false)
  const src = email && !imgFailed ? gravatarUrl(email, size * 2) : null

  return (
    <div
      className={`af-avatar ${className}`}
      style={{ width: size, height: size, fontSize: size * 0.36 }}
    >
      {src ? (
        <img
          src={src}
          alt=""
          width={size}
          height={size}
          onError={() => setImgFailed(true)}
          style={{ width: size, height: size, borderRadius: '50%', display: 'block', objectFit: 'cover' }}
        />
      ) : (
        <span>{initial}</span>
      )}
    </div>
  )
}

// ─── UsageRow ────────────────────────────────────────────────────────────────

function UsageRow({ label, pct, resetsAt, isBilling, credits }: {
  label: string
  pct: number
  resetsAt?: string
  isBilling?: boolean
  credits?: string
}) {
  const reset = resetsAt ? formatResetsAt(resetsAt) : null
  const color = barColor(pct)
  return (
    <div className="af-usage-row">
      <div className="af-usage-row__header">
        <span className="af-usage-row__label">
          {isBilling && <span className="af-usage-row__dollar">$</span>}
          {label}
        </span>
        <span className="af-usage-row__right">
          {credits
            ? <span className="af-usage-row__credits">{credits}</span>
            : reset
              ? <span className="af-usage-row__reset" title={reset.exact}>resets in {reset.short}</span>
              : null
          }
          <span className="af-usage-row__pct" style={{ color }}>{pct.toFixed(0)}%</span>
        </span>
      </div>
      <div className="af-usage-row__track">
        <div className="af-usage-row__fill" style={{ width: `${barFill(pct)}%`, background: color }} />
      </div>
    </div>
  )
}

// ─── AccountForm (add / edit) ─────────────────────────────────────────────────

function AccountForm({ initial, onSave, onCancel }: {
  initial?: { name: string; claude_config_dir: string }
  onSave: (name: string, dir: string) => Promise<void>
  onCancel: () => void
}) {
  const [name, setName] = useState(initial?.name ?? '')
  const [dir, setDir] = useState(initial?.claude_config_dir ?? '')
  const [saving, setSaving] = useState(false)
  const [error, setError] = useState('')

  async function handleSubmit(e: React.FormEvent) {
    e.preventDefault()
    if (!name.trim() || !dir.trim()) { setError('Both fields are required'); return }
    setSaving(true)
    setError('')
    try {
      // Validate that .credentials.json exists in the given dir (on the active space)
      await api.validateAccountDir(dir.trim())
    } catch (err) {
      const msg = (err as Error).message
      // validateAccountDir throws on non-2xx; extract the JSON error if present
      const match = msg.match(/"error":"([^"]+)"/)
      setError(match ? match[1] : msg)
      setSaving(false)
      return
    }
    try {
      await onSave(name.trim(), dir.trim())
    } catch (err) {
      setError((err as Error).message)
      setSaving(false)
    }
  }

  return (
    <form className="af-form" onSubmit={handleSubmit}>
      <div className="af-form__field">
        <label className="af-form__label">Account name</label>
        <input
          className="af-form__input"
          value={name}
          onChange={e => setName(e.target.value)}
          placeholder="e.g. personal"
          autoFocus
        />
      </div>
      <div className="af-form__field">
        <label className="af-form__label">Claude config dir</label>
        <input
          className="af-form__input"
          value={dir}
          onChange={e => setDir(e.target.value)}
          placeholder="e.g. /home/user/.claude"
        />
        <div className="af-form__hint">Must contain <code>.credentials.json</code></div>
      </div>
      {error && <div className="af-form__error">{error}</div>}
      <div className="af-form__actions">
        <button type="button" className="af-form__btn af-form__btn--cancel" onClick={onCancel}>
          Cancel
        </button>
        <button type="submit" className="af-form__btn af-form__btn--save" disabled={saving}>
          {saving ? 'Saving…' : (initial ? 'Save' : 'Add account')}
        </button>
      </div>
    </form>
  )
}

// ─── AccountCard (expanded panel) ────────────────────────────────────────────

function AccountCard({ account, isActive, onSelect, onEdit, onDelete }: {
  account: Account
  isActive: boolean
  onSelect: () => void
  onEdit: () => void
  onDelete: () => void
}) {
  const [profile, setProfile] = useState<AccountProfile | null>(null)
  const [usage, setUsage] = useState<AccountUsage | null>(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    setLoading(true)
    Promise.all([
      api.getAccountProfile(account.id).catch(() => null),
      api.getAccountUsage(account.id).catch(() => null),
    ]).then(([p, u]) => { setProfile(p); setUsage(u); setLoading(false) })
  }, [account.id])

  const status = getStatus(usage)
  const sd = statusEmoji(status)
  const mode = activeMode(usage)
  const email = profile?.account.email ?? ''
  const initial = email.charAt(0).toUpperCase() || (account.name ?? '').charAt(0).toUpperCase() || '?'
  const hasExtra = usage?.extra_usage?.is_enabled

  return (
    <div className={`af-card${isActive ? ' af-card--active' : ''}`}>
      <div className="af-card__header" onClick={onSelect} role="button" tabIndex={0}
        onKeyDown={e => { if (e.key === 'Enter') onSelect() }}
        style={{ cursor: 'pointer' }}
      >
        <Avatar email={email} initial={initial} size={28} />
        <div className="af-card__identity">
          <div className="af-card__name-row">
            <span className="af-card__name">{account.name}</span>
            {isActive && <span className="af-card__active-badge">ACTIVE</span>}
          </div>
          {email && <span className="af-card__email">{email}</span>}
        </div>
        <div className="af-card__status">
          <span className="af-card__status-dot" title={sd.label}>{sd.emoji}</span>
          {mode && (
            <span className={`af-card__mode af-card__mode--${status}`}>{mode}</span>
          )}
        </div>
        <div className="af-card__actions" onClick={e => e.stopPropagation()}>
          <button className="af-card__action-btn" title="Edit" onClick={onEdit}>
            <IconEdit size={13} />
          </button>
          <button className="af-card__action-btn af-card__action-btn--danger" title="Delete" onClick={onDelete}>
            <IconTrash size={13} />
          </button>
        </div>
      </div>

      {loading ? (
        <div className="af-card__loading">Loading usage…</div>
      ) : (
        <div className="af-card__usage">
          {usage?.five_hour && (
            <UsageRow label="5h window" pct={usage.five_hour.utilization} resetsAt={usage.five_hour.resets_at} />
          )}
          {usage?.seven_day && (
            <UsageRow label="7d window" pct={usage.seven_day.utilization} resetsAt={usage.seven_day.resets_at} />
          )}
          {hasExtra && usage?.extra_usage && (
            <UsageRow
              label="Extra usage" pct={usage.extra_usage.utilization} isBilling
              credits={`$${usage.extra_usage.used_credits.toFixed(2)} / $${usage.extra_usage.monthly_limit}`}
            />
          )}
          {!usage?.five_hour && !usage?.seven_day && !hasExtra && (
            <div className="af-card__no-usage">No usage data</div>
          )}
        </div>
      )}
    </div>
  )
}

// ─── Main component ───────────────────────────────────────────────────────────

type PanelView = 'list' | 'add' | { edit: Account }

export function AccountFooter({ visible }: { visible: boolean }) {
  const accounts = useStore(s => s.accounts)
  const selectedAccountId = useStore(s => s.selectedAccountId)
  const setSelectedAccount = useStore(s => s.setSelectedAccount)
  const loadAccounts = useStore(s => s.loadAccounts)
  const createAccount = useStore(s => s.createAccount)
  const updateAccount = useStore(s => s.updateAccount)
  const deleteAccount = useStore(s => s.deleteAccount)

  const [open, setOpen] = useState(false)
  const [view, setView] = useState<PanelView>('list')
  const [selectedProfile, setSelectedProfile] = useState<AccountProfile | null>(null)
  const [selectedUsage, setSelectedUsage] = useState<AccountUsage | null>(null)
  const containerRef = useRef<HTMLDivElement>(null)

  const selectedAccount = accounts.find(a => a.id === selectedAccountId) ?? accounts[0]

  useEffect(() => { loadAccounts() }, [])

  useEffect(() => {
    if (!selectedAccountId && accounts.length > 0) setSelectedAccount(accounts[0].id)
  }, [accounts, selectedAccountId])

  useEffect(() => {
    if (!selectedAccount) return
    setSelectedProfile(null)
    setSelectedUsage(null)
    api.getAccountProfile(selectedAccount.id).then(setSelectedProfile).catch(() => {})
    api.getAccountUsage(selectedAccount.id).then(setSelectedUsage).catch(() => {})
  }, [selectedAccount?.id])

  useEffect(() => {
    if (!open) { setView('list'); return }
    const handler = (e: MouseEvent) => {
      if (containerRef.current && !containerRef.current.contains(e.target as Node)) {
        setOpen(false)
        setView('list')
      }
    }
    document.addEventListener('mousedown', handler)
    return () => document.removeEventListener('mousedown', handler)
  }, [open])

  if (!visible) return null

  const status = getStatus(selectedUsage)
  const sd = accounts.length > 0 ? statusEmoji(status) : statusEmoji('available')
  const email = selectedProfile?.account.email ?? ''
  const initial = email.charAt(0).toUpperCase() || (selectedAccount?.name.charAt(0).toUpperCase() ?? '+')

  const fiveHourReset = selectedUsage?.five_hour?.resets_at
    ? formatResetsAt(selectedUsage.five_hour.resets_at)
    : null

  async function handleAdd(name: string, dir: string) {
    await createAccount({ name, claude_config_dir: dir })
    setView('list')
  }

  async function handleEdit(account: Account, name: string, dir: string) {
    await updateAccount(account.id, { name, claude_config_dir: dir })
    setView('list')
  }

  async function handleDelete(id: string) {
    if (!confirm('Delete this account?')) return
    await deleteAccount(id)
    setView('list')
  }

  return (
    <div className="account-footer" ref={containerRef}>
      {open && (
        <div className="af-panel">
          <div className="af-panel__header">
            <span>Accounts</span>
            <div className="af-panel__header-actions">
              {view === 'list' && (
                <button className="af-panel__add-btn" title="Add account" onClick={() => setView('add')}>
                  <IconPlus size={14} />
                </button>
              )}
              {view !== 'list' && (
                <button className="af-panel__add-btn" title="Back" onClick={() => setView('list')}>
                  <IconX size={14} />
                </button>
              )}
            </div>
          </div>

          {view === 'add' && (
            <AccountForm
              onSave={handleAdd}
              onCancel={() => setView('list')}
            />
          )}

          {typeof view === 'object' && 'edit' in view && (
            <AccountForm
              initial={{ name: view.edit.name, claude_config_dir: view.edit.claude_config_dir ?? '' }}
              onSave={(name, dir) => handleEdit(view.edit, name, dir)}
              onCancel={() => setView('list')}
            />
          )}

          {view === 'list' && (
            <div className="af-panel__list">
              {accounts.length === 0 && (
                <div className="af-card__no-usage" style={{ padding: '12px 14px' }}>
                  No accounts. Click + to add one.
                </div>
              )}
              {accounts.map(a => (
                <AccountCard
                  key={a.id}
                  account={a}
                  isActive={a.id === (selectedAccountId ?? accounts[0]?.id)}
                  onSelect={() => { setSelectedAccount(a.id); setOpen(false) }}
                  onEdit={() => setView({ edit: a })}
                  onDelete={() => handleDelete(a.id)}
                />
              ))}
            </div>
          )}
        </div>
      )}

      <button
        className={`account-footer__bar account-footer__bar--${sd.cls}`}
        onClick={() => setOpen(v => !v)}
        title={selectedAccount ? `${selectedAccount.name} · ${sd.label}` : 'No account configured'}
      >
        <Avatar email={email} initial={initial} size={28} />
        <div className="account-footer__identity">
          <span className="account-footer__name">{selectedAccount?.name ?? 'No account'}</span>
          {email && <span className="account-footer__email">{email}</span>}
        </div>
        {fiveHourReset && (
          <span className="account-footer__reset" title={fiveHourReset.exact}>
            {fiveHourReset.short}
          </span>
        )}
        <span
          className={`account-footer__status-dot account-footer__status-dot--${sd.cls}`}
          title={sd.label}
        >
          {sd.emoji}
        </span>
      </button>
    </div>
  )
}
