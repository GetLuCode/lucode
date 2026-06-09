import { useEffect, useState } from 'react'
import { motion } from 'framer-motion'
import { IconX, IconPlayerStop } from '@tabler/icons-react'
import * as api from '../../api'
import type { ProcessInfo } from '../../types'
import './TaskManagerPanel.css'

interface Props {
  isOpen: boolean
  onClose: () => void
}

function formatUptime(startedAt: string): string {
  const ms = Date.now() - new Date(startedAt).getTime()
  if (ms < 0) return '0s'
  const s = Math.floor(ms / 1000)
  const m = Math.floor(s / 60)
  const h = Math.floor(m / 60)
  if (h > 0) return `${h}h ${m % 60}m`
  if (m > 0) return `${m}m ${s % 60}s`
  return `${s}s`
}

function formatRAM(bytes: number): string {
  if (!bytes) return '—'
  const mib = bytes / (1024 * 1024)
  return `${mib.toFixed(1)} MiB`
}

function shortID(id: string): string {
  return id.length > 8 ? id.slice(0, 8) : id
}

export function TaskManagerPanel({ isOpen, onClose }: Props) {
  const [processes, setProcesses] = useState<ProcessInfo[]>([])
  const [loading, setLoading] = useState(false)
  const [killing, setKilling] = useState<string | null>(null)

  useEffect(() => {
    if (!isOpen) return
    let alive = true

    const load = () => {
      api.listProcesses()
        .then(list => { if (alive) setProcesses(list ?? []) })
        .catch(() => {})
        .finally(() => { if (alive) setLoading(false) })
    }

    setLoading(true)
    load()
    const t = setInterval(load, 5000)
    return () => {
      alive = false
      clearInterval(t)
    }
  }, [isOpen])

  const handleKill = async (id: string) => {
    setKilling(id)
    try {
      await api.killProcess(id)
      setProcesses(prev => prev.filter(p => p.id !== id))
    } catch {
      // leave row; next poll will update state
    } finally {
      setKilling(null)
    }
  }

  if (!isOpen) return null

  return (
    <motion.div
      className="tm-overlay"
      initial={{ opacity: 0 }}
      animate={{ opacity: 1 }}
      exit={{ opacity: 0 }}
      onClick={onClose}
    >
      <motion.div
        className="tm-panel"
        initial={{ scale: 0.95, opacity: 0 }}
        animate={{ scale: 1, opacity: 1 }}
        exit={{ scale: 0.95, opacity: 0 }}
        onClick={e => e.stopPropagation()}
      >
        <div className="tm-header">
          <span className="tm-title">Task Manager</span>
          <button className="tm-close" onClick={onClose} aria-label="Close">
            <IconX size={18} />
          </button>
        </div>

        <div className="tm-body">
          {loading && processes.length === 0 ? (
            <div className="tm-empty">Loading…</div>
          ) : processes.length === 0 ? (
            <div className="tm-empty">No active Claude processes.</div>
          ) : (
            <table className="tm-table">
              <thead>
                <tr>
                  <th>Session ID</th>
                  <th>Model</th>
                  <th>PID</th>
                  <th>Uptime</th>
                  <th>RAM</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {processes.map(p => (
                  <tr key={p.id}>
                    <td className="tm-cell-id" title={p.id}>{shortID(p.id)}</td>
                    <td>{p.model || '—'}</td>
                    <td className="tm-cell-mono">{p.pid || '—'}</td>
                    <td className="tm-cell-mono">{p.started_at ? formatUptime(p.started_at) : '—'}</td>
                    <td className="tm-cell-mono">{formatRAM(p.ram_bytes)}</td>
                    <td>
                      <button
                        className="tm-kill-btn"
                        title="Kill process"
                        disabled={killing === p.id}
                        onClick={() => handleKill(p.id)}
                      >
                        <IconPlayerStop size={13} />
                        {killing === p.id ? 'Killing…' : 'Kill'}
                      </button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>

        <div className="tm-footer">
          <span className="tm-footer-hint">Auto-refreshes every 5s. Kill sends session_end then force-kills after 3s.</span>
        </div>
      </motion.div>
    </motion.div>
  )
}
