import { useEffect, useRef, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { motion } from 'framer-motion'
import { IconX } from '@tabler/icons-react'
import { backdropClose } from '../../lib/backdropClose'
import './Plugins.css'

// The dialog of everything plugin: above the settings window, closed by Esc or the backdrop.
export function PluginDialog({ title, subtitle, icon, onClose, footer, wide, busy, children }: {
  title: string
  subtitle?: ReactNode
  icon?: ReactNode
  onClose: () => void
  footer?: ReactNode
  wide?: boolean
  busy?: boolean // a call is on its way: closing now would hide its answer
  children: ReactNode
}) {
  const box = useRef<HTMLDivElement>(null)
  const close = () => { if (!busy) onClose() }
  const closeRef = useRef(close)
  closeRef.current = close
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') { e.stopPropagation(); closeRef.current() } }
    window.addEventListener('keydown', onKey, true)
    box.current?.focus()
    return () => window.removeEventListener('keydown', onKey, true)
  }, [])
  return createPortal(
    <motion.div className="plugin-dialog__backdrop" initial={{ opacity: 0 }} animate={{ opacity: 1 }} transition={{ duration: 0.14 }} {...backdropClose(close)}>
      <motion.div
        ref={box}
        className={`plugin-dialog${wide ? ' plugin-dialog--wide' : ''}`}
        role="dialog"
        aria-modal="true"
        aria-label={title}
        tabIndex={-1}
        initial={{ opacity: 0, y: 10, scale: 0.985 }}
        animate={{ opacity: 1, y: 0, scale: 1 }}
        transition={{ duration: 0.2, ease: [0.16, 1, 0.3, 1] }}
      >
        <header className="plugin-dialog__head">
          {icon && <span className="plugin-tile">{icon}</span>}
          <div className="plugin-dialog__heading">
            <h2>{title}</h2>
            {subtitle && <div className="plugin-dialog__subtitle">{subtitle}</div>}
          </div>
          <button className="plugin-icon-btn" onClick={close} aria-label="Close" disabled={busy}><IconX size={16} /></button>
        </header>
        <div className="plugin-dialog__body">{children}</div>
        {footer && <footer className="plugin-dialog__foot">{footer}</footer>}
      </motion.div>
    </motion.div>,
    document.body,
  )
}
