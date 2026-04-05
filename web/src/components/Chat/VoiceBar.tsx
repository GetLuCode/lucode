import { IconX } from '@tabler/icons-react'
import { motion } from 'framer-motion'

const LANGUAGES = [
  { label: 'Español (España)', value: 'es-ES' },
  { label: 'Español (México)', value: 'es-MX' },
  { label: 'English (US)', value: 'en-US' },
  { label: 'English (UK)', value: 'en-GB' },
  { label: 'Français', value: 'fr-FR' },
  { label: 'Deutsch', value: 'de-DE' },
  { label: 'Português (BR)', value: 'pt-BR' },
  { label: 'Italiano', value: 'it-IT' },
  { label: '日本語', value: 'ja-JP' },
  { label: '中文', value: 'zh-CN' },
]

interface Props {
  isOpen: boolean
  lang: string
  autoSend: boolean
  autoRead: boolean
  noEcho: boolean
  onClose: () => void
  onLangChange: (lang: string) => void
  onAutoSendToggle: () => void
  onAutoReadToggle: () => void
  onNoEchoToggle: () => void
}

export function VoiceSettingsModal({
  isOpen,
  lang,
  autoSend,
  autoRead,
  noEcho,
  onClose,
  onLangChange,
  onAutoSendToggle,
  onAutoReadToggle,
  onNoEchoToggle,
}: Props) {
  if (!isOpen) return null

  return (
    <>
      {/* Backdrop */}
      <motion.div
        className="voice-modal-backdrop"
        onClick={onClose}
        initial={{ opacity: 0 }}
        animate={{ opacity: 1 }}
        exit={{ opacity: 0 }}
        transition={{ duration: 0.15 }}
      />

      {/* Modal */}
      <motion.div
        className="voice-modal"
        initial={{ opacity: 0, scale: 0.95, y: -8 }}
        animate={{ opacity: 1, scale: 1, y: 0 }}
        exit={{ opacity: 0, scale: 0.95, y: -8 }}
        transition={{ duration: 0.15, ease: 'easeOut' }}
      >
        {/* Header */}
        <div className="voice-modal__header">
          <h3 className="voice-modal__title">Voice Settings</h3>
          <button
            className="voice-modal__close"
            onClick={onClose}
            title="Close"
            aria-label="Close voice settings"
          >
            <IconX size={18} />
          </button>
        </div>

        {/* Body */}
        <div className="voice-modal__body">
          {/* Language */}
          <div className="voice-modal__option">
            <label htmlFor="voice-lang" className="voice-modal__label">
              Language
            </label>
            <select
              id="voice-lang"
              value={lang}
              onChange={(e) => onLangChange(e.target.value)}
              className="voice-modal__select"
            >
              {LANGUAGES.map((l) => (
                <option key={l.value} value={l.value}>
                  {l.label}
                </option>
              ))}
            </select>
          </div>

          {/* Auto-send toggle */}
          <div className="voice-modal__option">
            <label className="voice-modal__toggle-label">
              <input
                type="checkbox"
                checked={autoSend}
                onChange={onAutoSendToggle}
                className="voice-modal__checkbox"
              />
              <span className="voice-modal__toggle-text">Auto-send after recording</span>
            </label>
            <p className="voice-modal__help-text">
              Automatically send the message when speech ends
            </p>
          </div>

          {/* Auto-read toggle */}
          <div className="voice-modal__option">
            <label className="voice-modal__toggle-label">
              <input
                type="checkbox"
                checked={autoRead}
                onChange={onAutoReadToggle}
                className="voice-modal__checkbox"
              />
              <span className="voice-modal__toggle-text">Auto-read responses</span>
            </label>
            <p className="voice-modal__help-text">
              Automatically read Claude's responses aloud
            </p>
          </div>

          {/* No-echo toggle */}
          <div className="voice-modal__option">
            <label className="voice-modal__toggle-label">
              <input
                type="checkbox"
                checked={noEcho}
                onChange={onNoEchoToggle}
                className="voice-modal__checkbox"
              />
              <span className="voice-modal__toggle-text">No-echo mode</span>
            </label>
            <p className="voice-modal__help-text">
              When active, coherent speech stops TTS playback. Use if you have speakers (not headphones). Clicking mic while TTS plays also stops it.
            </p>
          </div>
        </div>
      </motion.div>
    </>
  )
}
