// Strips markdown and emojis from text before speaking
function stripMarkdown(text: string): string {
  return text
    .replace(/```[\s\S]*?```/g, ' ')
    .replace(/`[^`]*`/g, ' ')
    .replace(/\*\*\*(.+?)\*\*\*/g, '$1')
    .replace(/\*\*(.+?)\*\*/g, '$1')
    .replace(/___(.+?)___/g, '$1')
    .replace(/__(.+?)__/g, '$1')
    .replace(/\*(.+?)\*/g, '$1')
    .replace(/_(.+?)_/g, '$1')
    .replace(/^#{1,6}\s+(.+)$/gm, '$1')
    .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
    .replace(/!\[([^\]]*)\]\([^)]+\)/g, '$1')
    .replace(/^>\s*/gm, '')
    .replace(/^[\s]*[-*+]\s+/gm, '')
    .replace(/^[\s]*\d+\.\s+/gm, '')
    .replace(/^[-*_]{3,}$/gm, '')
    // Strip emojis — cause TTS to stop or mispronounce
    .replace(/[\u{1F000}-\u{1FFFF}]/gu, '')
    .replace(/[\u{2600}-\u{27BF}]/gu, '')
    .replace(/[\u{2B00}-\u{2BFF}]/gu, '')
    .replace(/[\uFE00-\uFEFF]/g, '')
    .replace(/\n{3,}/g, '\n\n')
    .replace(/  +/g, ' ')
    .trim()
}

export interface TTSCallbacks {
  onStart?: () => void
  onEnd?: () => void
}

interface TTSModule {
  speak(text: string, lang: string, callbacks?: TTSCallbacks): void
  stop(): void
  isSpeakingNow(): boolean
}

const ttsModule: TTSModule = {
  speak(text: string, lang: string, callbacks?: TTSCallbacks): void {
    const synth = (window as any).speechSynthesis
    if (!synth) return

    synth.cancel()

    const cleanText = stripMarkdown(text)
    if (!cleanText.trim()) return

    const utterance = new SpeechSynthesisUtterance(cleanText)
    utterance.lang = lang
    utterance.rate = 1.0
    utterance.pitch = 1.0
    utterance.volume = 1.0

    if (callbacks?.onStart) utterance.onstart = callbacks.onStart
    if (callbacks?.onEnd)   utterance.onend   = callbacks.onEnd

    synth.speak(utterance)
  },

  stop(): void {
    const synth = (window as any).speechSynthesis
    if (synth) synth.cancel()
  },

  isSpeakingNow(): boolean {
    return (window as any).speechSynthesis?.speaking ?? false
  },
}

export const tts = ttsModule
