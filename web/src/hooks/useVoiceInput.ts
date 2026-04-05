import { useEffect, useRef, useState } from 'react'

interface UseVoiceInputOptions {
  lang: string
  onTranscript: (text: string) => void
  onInterim: (text: string) => void
}

interface UseVoiceInputReturn {
  isListening: boolean
  start: () => void
  stop: () => void
  supported: boolean
}

export function useVoiceInput({
  lang,
  onTranscript,
  onInterim,
}: UseVoiceInputOptions): UseVoiceInputReturn {
  const [isListening, setIsListening] = useState(false)
  const recognitionRef = useRef<any>(null)
  const supportedRef = useRef(false)

  useEffect(() => {
    const SpeechRecognition =
      (window as any).SpeechRecognition || (window as any).webkitSpeechRecognition
    supportedRef.current = !!SpeechRecognition

    if (!SpeechRecognition) return

    const recognition = new SpeechRecognition()
    recognitionRef.current = recognition

    recognition.continuous = true
    recognition.interimResults = true
    recognition.language = lang

    recognition.onstart = () => {
      setIsListening(true)
    }

    recognition.onresult = (event: any) => {
      let interimTranscript = ''
      let finalTranscript = ''

      for (let i = event.resultIndex; i < event.results.length; i++) {
        const transcript = event.results[i][0].transcript

        if (event.results[i].isFinal) {
          finalTranscript += transcript + ' '
        } else {
          interimTranscript += transcript
        }
      }

      if (interimTranscript) {
        onInterim(interimTranscript)
      }

      if (finalTranscript) {
        onTranscript(finalTranscript.trim())
      }
    }

    recognition.onerror = (event: any) => {
      console.error('Speech recognition error:', event.error)
      if (event.error === 'no-speech') {
        // Timeout after no speech detected
        recognition.stop()
      }
    }

    recognition.onend = () => {
      setIsListening(false)
      onInterim('')
    }

    return () => {
      if (recognitionRef.current) {
        recognitionRef.current.stop()
      }
    }
  }, [lang, onTranscript, onInterim])

  const start = () => {
    if (recognitionRef.current && !isListening) {
      recognitionRef.current.start()
    }
  }

  const stop = () => {
    if (recognitionRef.current && isListening) {
      recognitionRef.current.stop()
    }
  }

  return {
    isListening,
    start,
    stop,
    supported: supportedRef.current,
  }
}
