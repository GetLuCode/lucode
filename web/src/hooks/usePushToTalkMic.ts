import { useCallback, useEffect, useRef, useState } from 'react'
import * as api from '../api'
import { getDeviceId, setDeviceId } from '../lib/audioDevices'
import type { MicState } from './useWorkerMic'

// Push to talk, for engines that transcribe a whole recording (Orchard): start() begins recording,
// stop() ends it and sends everything at once; the text arrives through onFinal. Nothing is sent
// while recording, and leaving the page (unmount) throws the recording away.

const MAX_SECONDS = 600 // Orchard's synchronous endpoint is meant for short recordings

const WORKLET = `
class Rec extends AudioWorkletProcessor {
  process(inputs) {
    const ch = inputs[0] && inputs[0][0]
    if (ch) this.port.postMessage(new Float32Array(ch))
    return true
  }
}
registerProcessor('rec', Rec)
`

// Mono PCM16 WAV of float chunks.
export function encodeWav(chunks: Float32Array[], sampleRate: number): Blob {
  let n = 0
  for (const c of chunks) n += c.length
  const buf = new ArrayBuffer(44 + n * 2)
  const v = new DataView(buf)
  const str = (o: number, s: string) => { for (let i = 0; i < s.length; i++) v.setUint8(o + i, s.charCodeAt(i)) }
  str(0, 'RIFF'); v.setUint32(4, 36 + n * 2, true); str(8, 'WAVE'); str(12, 'fmt ')
  v.setUint32(16, 16, true); v.setUint16(20, 1, true); v.setUint16(22, 1, true)
  v.setUint32(24, sampleRate, true); v.setUint32(28, sampleRate * 2, true); v.setUint16(32, 2, true); v.setUint16(34, 16, true)
  str(36, 'data'); v.setUint32(40, n * 2, true)
  let o = 44
  for (const c of chunks) {
    for (let i = 0; i < c.length; i++, o += 2) {
      const s = Math.max(-1, Math.min(1, c[i]))
      v.setInt16(o, s < 0 ? s * 0x8000 : s * 0x7fff, true)
    }
  }
  return new Blob([buf], { type: 'audio/wav' })
}

interface Handlers {
  onRealtime: (text: string) => void // unused: the text only exists once the recording ends
  onFinal: (text: string, viaHotword: boolean, ms?: number) => void
  onLevel?: (level: number) => void
}

interface Recording {
  ctx?: AudioContext
  stream?: MediaStream
  node?: AudioWorkletNode
  chunks: Float32Array[]
  rate: number
  meter?: ReturnType<typeof setInterval>
  limit?: ReturnType<typeof setTimeout>
}

export function usePushToTalkMic(handlers: Handlers) {
  const [state, setState] = useState<MicState>('off')
  const [error, setError] = useState('')
  const h = useRef(handlers)
  h.current = handlers
  const res = useRef<Recording | null>(null)
  const abort = useRef<AbortController | null>(null)
  const mounted = useRef(true)

  const release = useCallback(() => {
    const r = res.current
    res.current = null
    if (!r) return
    clearInterval(r.meter)
    clearTimeout(r.limit)
    h.current.onLevel?.(0)
    try { r.node?.disconnect() } catch { /* already gone */ }
    r.stream?.getTracks().forEach(t => t.stop())
    void r.ctx?.close().catch(() => {})
  }, [])

  // Ends the recording and sends it.
  const stop = useCallback(async () => {
    const r = res.current
    if (!r) return // not recording (still transcribing, or off)
    release()
    const samples = r.chunks.reduce((a, c) => a + c.length, 0)
    if (samples < r.rate * 0.3) { setState('off'); return } // a click, not speech
    setState('processing')
    const ctl = new AbortController()
    abort.current = ctl
    try {
      const wav = encodeWav(r.chunks, r.rate)
      const t0 = performance.now() // from the call to the API until its answer
      const text = await api.orchardTranscribe(wav, ctl.signal)
      const ms = performance.now() - t0
      if (!mounted.current) return
      setState('off')
      h.current.onFinal(text.trim(), false, ms)
    } catch (e) {
      if (!mounted.current || ctl.signal.aborted) return
      setError(e instanceof Error ? e.message : String(e))
      setState('off')
    } finally {
      if (abort.current === ctl) abort.current = null
    }
  }, [release])

  const start = useCallback(async () => {
    if (res.current || abort.current) return
    setError('')
    setState('loading')
    try {
      const base = { channelCount: 1, echoCancellation: true, noiseSuppression: true }
      const wanted = getDeviceId('input')
      let stream: MediaStream
      try {
        stream = await navigator.mediaDevices.getUserMedia({ audio: wanted ? { ...base, deviceId: { exact: wanted } } : base })
      } catch (e) {
        if (wanted && e instanceof DOMException && e.name === 'OverconstrainedError') {
          setDeviceId('input', '')
          stream = await navigator.mediaDevices.getUserMedia({ audio: base })
        } else {
          throw e
        }
      }
      const ctx = new AudioContext({ sampleRate: 16000 })
      const url = URL.createObjectURL(new Blob([WORKLET], { type: 'application/javascript' }))
      await ctx.audioWorklet.addModule(url)
      URL.revokeObjectURL(url)
      const node = new AudioWorkletNode(ctx, 'rec')
      const source = ctx.createMediaStreamSource(stream)
      source.connect(node)
      const r: Recording = { ctx, stream, node, chunks: [], rate: ctx.sampleRate }
      node.port.onmessage = e => { r.chunks.push(e.data as Float32Array) }
      if (h.current.onLevel) {
        const an = ctx.createAnalyser()
        an.fftSize = 1024
        source.connect(an)
        const data = new Float32Array(an.fftSize)
        r.meter = setInterval(() => {
          an.getFloatTimeDomainData(data)
          let sq = 0
          for (const x of data) sq += x * x
          h.current.onLevel?.(Math.min(1, Math.sqrt(sq / data.length) * 4))
        }, 66)
      }
      r.limit = setTimeout(() => { void stop() }, MAX_SECONDS * 1000)
      res.current = r
      setState('listening')
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      release()
      setState('off')
    }
  }, [release, stop])

  useEffect(() => {
    mounted.current = true
    return () => { mounted.current = false; release(); abort.current?.abort() }
  }, [release])

  const setMuted = useCallback((_v: boolean) => { /* the recording is explicit: nothing to mute */ }, [])
  const finish = useCallback(() => { void stop() }, [stop])
  return { state, error, start, stop: finish, setMuted }
}
