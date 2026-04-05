/**
 * Voice preferences stored in localStorage.
 * Keys: voice_lang, voice_autosend, voice_autoread, voice_noecho
 */

const KEYS = {
  LANG:      'voice_lang',
  AUTOSEND:  'voice_autosend',
  AUTOREAD:  'voice_autoread',
  NOECHO:    'voice_noecho',
  VOICEURI:  'voice_uri',
}

const DEFAULT_LANG = navigator.language || 'en-US'

export interface VoicePrefs {
  lang: string
  autoSend: boolean
  autoRead: boolean
  noEcho: boolean
  voiceURI: string | null
}

export function getVoicePrefs(): VoicePrefs {
  if (typeof window === 'undefined') {
    return { lang: DEFAULT_LANG, autoSend: false, autoRead: false, noEcho: false, voiceURI: null }
  }
  return {
    lang:     localStorage.getItem(KEYS.LANG) || DEFAULT_LANG,
    autoSend: localStorage.getItem(KEYS.AUTOSEND) === 'true',
    autoRead: localStorage.getItem(KEYS.AUTOREAD) === 'true',
    noEcho:   localStorage.getItem(KEYS.NOECHO)   === 'true',
    voiceURI: localStorage.getItem(KEYS.VOICEURI) || null,
  }
}

export function saveVoicePrefs(prefs: Partial<VoicePrefs>): void {
  if (typeof window === 'undefined') return
  if (prefs.lang     !== undefined) localStorage.setItem(KEYS.LANG,     prefs.lang)
  if (prefs.autoSend !== undefined) localStorage.setItem(KEYS.AUTOSEND, String(prefs.autoSend))
  if (prefs.autoRead !== undefined) localStorage.setItem(KEYS.AUTOREAD, String(prefs.autoRead))
  if (prefs.noEcho   !== undefined) localStorage.setItem(KEYS.NOECHO,   String(prefs.noEcho))
  if (prefs.voiceURI !== undefined) {
    if (prefs.voiceURI) localStorage.setItem(KEYS.VOICEURI, prefs.voiceURI)
    else localStorage.removeItem(KEYS.VOICEURI)
  }
}
