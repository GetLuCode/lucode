import { useEffect, useState } from 'react'

// Strips markdown and emojis from text before speaking
function stripMarkdown(text: string): string {
  return text
    // Remove fenced code blocks entirely (```lang\n...\n```)
    .replace(/```[\s\S]*?```/g, ' ')
    // Remove inline code
    .replace(/`[^`]*`/g, ' ')
    // Unwrap bold/italic
    .replace(/\*\*\*(.+?)\*\*\*/g, '$1')
    .replace(/\*\*(.+?)\*\*/g, '$1')
    .replace(/___(.+?)___/g, '$1')
    .replace(/__(.+?)__/g, '$1')
    .replace(/\*(.+?)\*/g, '$1')
    .replace(/_(.+?)_/g, '$1')
    // Strip headers (keep the text)
    .replace(/^#{1,6}\s+(.+)$/gm, '$1')
    // Strip links (keep label)
    .replace(/\[([^\]]+)\]\([^)]+\)/g, '$1')
    // Strip images
    .replace(/!\[([^\]]*)\]\([^)]+\)/g, '$1')
    // Strip blockquotes
    .replace(/^>\s*/gm, '')
    // Strip list markers
    .replace(/^[\s]*[-*+]\s+/gm, '')
    .replace(/^[\s]*\d+\.\s+/gm, '')
    // Strip horizontal rules
    .replace(/^[-*_]{3,}$/gm, '')
    // Strip emojis — these cause TTS to stop or mispronounce
    .replace(/[\u{1F000}-\u{1FFFF}]/gu, '')
    .replace(/[\u{2600}-\u{27BF}]/gu, '')
    .replace(/[\u{2B00}-\u{2BFF}]/gu, '')
    .replace(/[\uFE00-\uFEFF]/g, '')
    // Collapse multiple blank lines / spaces
    .replace(/\n{3,}/g, '\n\n')
    .replace(/  +/g, ' ')
    .trim()
}

interface TTSModule {
  speak(text: string, lang: string): void
  stop(): void
  isSpeaking(): boolean
}

/**
 * Singleton TTS module using Web Speech API.
 * Can be imported and used from anywhere without React context.
 */
const ttsModule: TTSModule = {
  isSpeaking(): boolean {
    return (window as any).speechSynthesis?.speaking ?? false
  },

  speak(text: string, lang: string): void {
    const synth = (window as any).speechSynthesis
    if (!synth) return

    // Cancel any ongoing speech
    synth.cancel()

    const cleanText = stripMarkdown(text)
    if (!cleanText.trim()) return

    const utterance = new SpeechSynthesisUtterance(cleanText)
    utterance.lang = lang
    utterance.rate = 1.0
    utterance.pitch = 1.0
    utterance.volume = 1.0

    synth.speak(utterance)
  },

  stop(): void {
    const synth = (window as any).speechSynthesis
    if (synth) {
      synth.cancel()
    }
  },
}

/**
 * Hook to use TTS with React state tracking.
 * Returns { supported, isSpeaking } for UI updates.
 * Call tts.speak() directly for actual TTS operations.
 */
export function useTTS() {
  const [isSpeaking, setIsSpeaking] = useState(false)
  const [supported] = useState(!!((window as any).speechSynthesis))

  // Track speechSynthesis state
  useEffect(() => {
    if (!supported) return

    const synth = (window as any).speechSynthesis
    const checkState = () => {
      setIsSpeaking(synth.speaking)
    }

    const interval = setInterval(checkState, 100)
    return () => clearInterval(interval)
  }, [supported])

  return {
    supported,
    isSpeaking,
    speak: ttsModule.speak.bind(ttsModule),
    stop: ttsModule.stop.bind(ttsModule),
  }
}

/**
 * Export singleton for direct usage without React hook.
 * Usage: tts.speak(text, lang) from anywhere.
 */
export const tts = ttsModule
