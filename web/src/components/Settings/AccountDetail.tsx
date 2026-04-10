import { IconArrowLeft, IconBuilding, IconCalendar, IconRefresh, IconUser } from '@tabler/icons-react'
import { useEffect, useState } from 'react'
import * as api from '../../api'
import type { AccountProfile, AccountUsage } from '../../api'
import type { Account } from '../../types'

interface Props {
  account: Account
  onBack: () => void
}

function UsageBar({ label, utilization, resetsAt }: { label: string; utilization: number; resetsAt?: string }) {
  const pct = Math.min(100, Math.max(0, utilization))
  const color = pct >= 90 ? '#f38ba8' : pct >= 70 ? '#fab387' : '#a6e3a1'
  const resetsDate = resetsAt ? new Date(resetsAt) : null
  const resetsIn = resetsDate ? formatRelative(resetsDate) : null

  return (
    <div className="account-detail__usage-bar">
      <div className="account-detail__usage-bar-header">
        <span className="account-detail__usage-bar-label">{label}</span>
        <span className="account-detail__usage-bar-pct" style={{ color }}>{pct.toFixed(1)}%</span>
      </div>
      <div className="account-detail__usage-track">
        <div
          className="account-detail__usage-fill"
          style={{ width: `${pct}%`, background: color }}
        />
      </div>
      {resetsIn && (
        <div className="account-detail__usage-resets">resets {resetsIn}</div>
      )}
    </div>
  )
}

function formatRelative(date: Date): string {
  const diff = date.getTime() - Date.now()
  if (diff < 0) return 'soon'
  const h = Math.floor(diff / 3600000)
  const m = Math.floor((diff % 3600000) / 60000)
  if (h > 24) return `in ${Math.floor(h / 24)}d ${h % 24}h`
  if (h > 0) return `in ${h}h ${m}m`
  return `in ${m}m`
}

function formatDate(iso: string): string {
  return new Date(iso).toLocaleDateString(undefined, { year: 'numeric', month: 'short', day: 'numeric' })
}

export function AccountDetail({ account, onBack }: Props) {
  const [profile, setProfile] = useState<AccountProfile | null>(null)
  const [usage, setUsage] = useState<AccountUsage | null>(null)
  const [profileLoading, setProfileLoading] = useState(true)
  const [usageLoading, setUsageLoading] = useState(true)
  const [profileError, setProfileError] = useState('')
  const [usageError, setUsageError] = useState('')

  useEffect(() => {
    setProfileLoading(true)
    setUsageLoading(true)
    setProfileError('')
    setUsageError('')

    api.getAccountProfile(account.id)
      .then(setProfile)
      .catch(err => setProfileError((err as Error).message || 'Failed to load profile'))
      .finally(() => setProfileLoading(false))

    api.getAccountUsage(account.id)
      .then(setUsage)
      .catch(err => setUsageError((err as Error).message || 'Failed to load usage'))
      .finally(() => setUsageLoading(false))
  }, [account.id])

  async function refreshUsage() {
    setUsageLoading(true)
    setUsageError('')
    try {
      const u = await api.getAccountUsage(account.id, true)
      setUsage(u)
    } catch (err) {
      setUsageError((err as Error).message || 'Failed to refresh usage')
    } finally {
      setUsageLoading(false)
    }
  }

  function planBadge() {
    if (!profile) return null
    if (profile.account.has_claude_max) return <span className="accounts-tab__badge accounts-tab__badge--max">Max</span>
    if (profile.account.has_claude_pro) return <span className="accounts-tab__badge accounts-tab__badge--pro">Pro</span>
    return <span className="accounts-tab__badge accounts-tab__badge--unknown">Free</span>
  }

  return (
    <div className="account-detail">
      <button className="account-detail__back" onClick={onBack}>
        <IconArrowLeft size={14} />
        <span>All accounts</span>
      </button>

      {profileLoading && <div className="account-detail__loading">Loading profile…</div>}
      {profileError && <div className="account-detail__error">{profileError}</div>}

      {profile && (
        <>
          <div className="account-detail__header">
            <div className="account-detail__avatar">
              {profile.account.display_name.charAt(0).toUpperCase()}
            </div>
            <div className="account-detail__identity">
              <div className="account-detail__name">{profile.account.full_name}</div>
              <div className="account-detail__email">{profile.account.email}</div>
              <div style={{ marginTop: 4 }}>{planBadge()}</div>
            </div>
          </div>

          <div className="account-detail__section-title">
            <IconBuilding size={13} />
            Organization
          </div>
          <div className="account-detail__card">
            <div className="account-detail__row">
              <span className="account-detail__row-label">Name</span>
              <span>{profile.organization.name}</span>
            </div>
            <div className="account-detail__row">
              <span className="account-detail__row-label">Plan</span>
              <span>{profile.organization.organization_type.replace(/_/g, ' ')}</span>
            </div>
            <div className="account-detail__row">
              <span className="account-detail__row-label">Status</span>
              <span style={{ color: profile.organization.subscription_status === 'active' ? '#a6e3a1' : '#f38ba8' }}>
                {profile.organization.subscription_status}
              </span>
            </div>
            <div className="account-detail__row">
              <span className="account-detail__row-label">Rate tier</span>
              <span>{profile.organization.rate_limit_tier}</span>
            </div>
          </div>

          <div className="account-detail__section-title">
            <IconCalendar size={13} />
            Member since
          </div>
          <div className="account-detail__card">
            <div className="account-detail__row">
              <span className="account-detail__row-label">Account</span>
              <span>{formatDate(profile.account.created_at)}</span>
            </div>
            <div className="account-detail__row">
              <span className="account-detail__row-label">Subscription</span>
              <span>{formatDate(profile.organization.subscription_created_at)}</span>
            </div>
          </div>
        </>
      )}

      <div className="account-detail__section-title">
        <IconUser size={13} />
        Usage
        <button
          className="account-detail__refresh-btn"
          onClick={refreshUsage}
          disabled={usageLoading}
          title="Refresh usage"
        >
          <IconRefresh size={12} className={usageLoading ? 'account-detail__refresh-spin' : ''} />
        </button>
      </div>

      {usageLoading && !usage && <div className="account-detail__loading">Loading usage…</div>}
      {usageError && <div className="account-detail__error">{usageError}</div>}

      {usage && (
        <div className="account-detail__card" style={{ opacity: usageLoading ? 0.5 : 1, transition: 'opacity 0.2s' }}>
          {usage.five_hour && (
            <UsageBar
              label="5-hour window"
              utilization={usage.five_hour.utilization}
              resetsAt={usage.five_hour.resets_at}
            />
          )}
          {usage.seven_day && (
            <UsageBar
              label="7-day window"
              utilization={usage.seven_day.utilization}
              resetsAt={usage.seven_day.resets_at}
            />
          )}
          {usage.seven_day_opus && (
            <UsageBar
              label="7-day Opus"
              utilization={usage.seven_day_opus.utilization}
              resetsAt={usage.seven_day_opus.resets_at}
            />
          )}
          {usage.seven_day_sonnet && (
            <UsageBar
              label="7-day Sonnet"
              utilization={usage.seven_day_sonnet.utilization}
              resetsAt={usage.seven_day_sonnet.resets_at}
            />
          )}
          {usage.extra_usage?.is_enabled && (
            <>
              <div className="account-detail__usage-divider" />
              <UsageBar
                label={`Extra usage (${usage.extra_usage.used_credits.toFixed(0)} / ${usage.extra_usage.monthly_limit} credits)`}
                utilization={usage.extra_usage.utilization}
              />
            </>
          )}
          {!usage.five_hour && !usage.seven_day && !usage.extra_usage?.is_enabled && (
            <div style={{ color: 'var(--text-muted)', fontSize: '0.8rem' }}>No usage data available</div>
          )}
        </div>
      )}
    </div>
  )
}
