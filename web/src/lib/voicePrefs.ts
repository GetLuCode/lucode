/**
 * Voice preferences stored in localStorage.
 * Keys: voice_lang, voice_autosend, voice_autoread
 */

const KEYS = {
  LANG: 'voice_lang',
  AUTOSEND: 'voice_autosend',
  AUTOREAD: 'voice_autoread',
}

const DEFAULT_LANG = navigator.language || 'en-US'

export interface VoicePrefs {
  lang: string
  autoSend: boolean
  autoRead: boolean
}

export function getVoicePrefs(): VoicePrefs {
  if (typeof window === 'undefined') {
    return {
      lang: DEFAULT_LANG,
      autoSend: false,
      autoRead: false,
    }
  }

  const lang = localStorage.getItem(KEYS.LANG) || DEFAULT_LANG
  const autoSend = localStorage.getItem(KEYS.AUTOSEND) === 'true'
  const autoRead = localStorage.getItem(KEYS.AUTOREAD) === 'true'

  return { lang, autoSend, autoRead }
}

export function saveVoicePrefs(prefs: Partial<VoicePrefs>): void {
  if (typeof window === 'undefined') return

  if (prefs.lang !== undefined) {
    localStorage.setItem(KEYS.LANG, prefs.lang)
  }
  if (prefs.autoSend !== undefined) {
    localStorage.setItem(KEYS.AUTOSEND, String(prefs.autoSend))
  }
  if (prefs.autoRead !== undefined) {
    localStorage.setItem(KEYS.AUTOREAD, String(prefs.autoRead))
  }
}
