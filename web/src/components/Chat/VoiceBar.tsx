import { Switch } from '@headlessui/react'
import { IconX } from '@tabler/icons-react'
import { motion } from 'framer-motion'
import { useEffect, useState } from 'react'

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
  voiceURI: string | null
  onClose: () => void
  onLangChange: (lang: string) => void
  onAutoSendToggle: () => void
  onAutoReadToggle: () => void
  onNoEchoToggle: () => void
  onVoiceChange: (voiceURI: string | null) => void
}

export function VoiceSettingsModal({
  isOpen,
  lang,
  autoSend,
  autoRead,
  noEcho,
  voiceURI,
  onClose,
  onLangChange,
  onAutoSendToggle,
  onAutoReadToggle,
  onNoEchoToggle,
  onVoiceChange,
}: Props) {
  const [voices, setVoices] = useState<SpeechSynthesisVoice[]>([])

  useEffect(() => {
    const synth = window.speechSynthesis
    if (!synth) return

    const load = () => setVoices(synth.getVoices())
    load()
    synth.addEventListener('voiceschanged', load)
    return () => synth.removeEventListener('voiceschanged', load)
  }, [])

  const langVoices = voices.filter((v) => v.lang.startsWith(lang.split('-')[0]))

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

          {/* Voice */}
          {langVoices.length > 0 && (
            <div className="voice-modal__option">
              <label htmlFor="voice-voice" className="voice-modal__label">
                Voice
              </label>
              <select
                id="voice-voice"
                value={voiceURI ?? ''}
                onChange={(e) => onVoiceChange(e.target.value || null)}
                className="voice-modal__select"
              >
                <option value="">Default</option>
                {langVoices.map((v) => (
                  <option key={v.voiceURI} value={v.voiceURI}>
                    {v.name}{v.localService ? '' : ' ☁'}
                  </option>
                ))}
              </select>
            </div>
          )}

          {/* Auto-send toggle */}
          <div className="voice-modal__option">
            <div className="voice-modal__switch-row">
              <Switch
                checked={autoSend}
                onChange={onAutoSendToggle}
                className="voice-modal__switch"
                data-checked={autoSend ? '' : undefined}
              >
                <span className="voice-modal__switch-thumb" />
              </Switch>
              <span className="voice-modal__toggle-text">Auto-send after recording</span>
            </div>
            <p className="voice-modal__help-text">
              Automatically send the message when speech ends
            </p>
          </div>

          {/* Auto-read toggle */}
          <div className="voice-modal__option">
            <div className="voice-modal__switch-row">
              <Switch
                checked={autoRead}
                onChange={onAutoReadToggle}
                className="voice-modal__switch"
                data-checked={autoRead ? '' : undefined}
              >
                <span className="voice-modal__switch-thumb" />
              </Switch>
              <span className="voice-modal__toggle-text">Auto-read responses</span>
            </div>
            <p className="voice-modal__help-text">
              Automatically read Claude's responses aloud
            </p>
          </div>

          {/* No-echo toggle */}
          <div className="voice-modal__option">
            <div className="voice-modal__switch-row">
              <Switch
                checked={noEcho}
                onChange={onNoEchoToggle}
                className="voice-modal__switch"
                data-checked={noEcho ? '' : undefined}
              >
                <span className="voice-modal__switch-thumb" />
              </Switch>
              <span className="voice-modal__toggle-text">No-echo mode</span>
            </div>
            <p className="voice-modal__help-text">
              When active, coherent speech stops TTS playback. Use if you have speakers (not headphones). Clicking mic while TTS plays also stops it.
            </p>
          </div>
        </div>
      </motion.div>
    </>
  )
}
