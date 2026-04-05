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

  // Keep callback refs stable so the effect doesn't re-run when handlers change
  const onTranscriptRef = useRef(onTranscript)
  const onInterimRef = useRef(onInterim)
  useEffect(() => { onTranscriptRef.current = onTranscript }, [onTranscript])
  useEffect(() => { onInterimRef.current = onInterim }, [onInterim])

  // Re-create recognition only when lang changes
  useEffect(() => {
    const SpeechRecognition =
      (window as any).SpeechRecognition || (window as any).webkitSpeechRecognition
    supportedRef.current = !!SpeechRecognition

    if (!SpeechRecognition) return

    // Stop any previous instance before replacing
    if (recognitionRef.current) {
      try { recognitionRef.current.stop() } catch {}
    }

    const recognition = new SpeechRecognition()
    recognitionRef.current = recognition

    recognition.continuous = true
    recognition.interimResults = true
    recognition.lang = lang  // correct property name

    recognition.onstart = () => setIsListening(true)

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

      if (interimTranscript) onInterimRef.current(interimTranscript)
      if (finalTranscript)   onTranscriptRef.current(finalTranscript.trim())
    }

    recognition.onerror = (event: any) => {
      if (event.error !== 'no-speech') {
        console.error('Speech recognition error:', event.error)
      }
      setIsListening(false)
      onInterimRef.current('')
    }

    recognition.onend = () => {
      setIsListening(false)
      onInterimRef.current('')
    }

    return () => {
      try { recognition.stop() } catch {}
    }
  }, [lang]) // only lang — callbacks are read via ref

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
