import { useCallback, useEffect, useRef, useState } from 'react'
import { speakRequest } from '../api'
import { applyOutputDevice } from '../lib/audioDevices'

// What is worth reading aloud: no code, links or markup.
export function speakableText(md: string): string {
  return md
    .replace(/```[\s\S]*?```/g, ' (code) ')
    .replace(/`([^`]*)`/g, '$1')
    .replace(/!\[[^\]]*\]\([^)]*\)/g, '')
    .replace(/\[([^\]]*)\]\([^)]*\)/g, '$1')
    .replace(/https?:\/\/\S+/g, '')
    .replace(/^\s{0,3}#{1,6}\s*/gm, '')
    .replace(/[*_~>|]+/g, '')
    .replace(/^\s*[-+]\s+/gm, '')
    .replace(/\n{2,}/g, '\n')
    .trim()
}

export type TTSState = 'idle' | 'loading' | 'speaking'

// Plays the audio of lucode's /api/tts as it arrives (PCM16 mono), so speech starts
// before the whole text is synthesized. One utterance at a time.
export function useWorkerTTS() {
  const [state, setState] = useState<TTSState>('idle')
  const [error, setError] = useState('')
  const run = useRef<{ abort: AbortController; ctx: AudioContext } | null>(null)

  const stop = useCallback(() => {
    const r = run.current
    run.current = null
    if (!r) return
    r.abort.abort()
    void r.ctx.close().catch(() => {})
    setState('idle')
  }, [])

  const speak = useCallback(async (raw: string) => {
    stop()
    const text = speakableText(raw)
    if (!text) return
    setError('')
    setState('loading')
    const abort = new AbortController()
    const ctx = new AudioContext()
    const me = { abort, ctx }
    run.current = me
    try {
      await applyOutputDevice(ctx)
      const res = await speakRequest(text, abort.signal)
      if (!res.ok) throw new Error((await res.text()).trim() || `HTTP ${res.status}`)
      const rate = Number(res.headers.get('X-Sample-Rate')) || 44100
      const reader = res.body!.getReader()
      let next = ctx.currentTime + 0.1
      let carry = new Uint8Array(0) // a chunk may end in the middle of a 16-bit sample
      let last: AudioBufferSourceNode | null = null
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        const bytes = new Uint8Array(carry.length + value.length)
        bytes.set(carry)
        bytes.set(value, carry.length)
        const whole = bytes.length - (bytes.length % 2)
        carry = bytes.slice(whole)
        if (whole === 0) continue
        const pcm = new Int16Array(bytes.buffer, bytes.byteOffset, whole / 2)
        const buf = ctx.createBuffer(1, pcm.length, rate)
        const ch = buf.getChannelData(0)
        for (let i = 0; i < pcm.length; i++) ch[i] = pcm[i] / 0x8000
        const src = ctx.createBufferSource()
        src.buffer = buf
        src.connect(ctx.destination)
        next = Math.max(next, ctx.currentTime + 0.02)
        src.start(next)
        next += buf.duration
        last = src
        if (run.current === me) setState('speaking')
      }
      if (last) {
        last.onended = () => {
          if (run.current === me) { run.current = null; void ctx.close().catch(() => {}); setState('idle') }
        }
      } else {
        stop()
      }
    } catch (e) {
      if (abort.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
      if (run.current === me) stop()
    }
  }, [stop])

  useEffect(() => stop, [stop])
  return { state, error, speak, stop }
}
