import { useCallback, useEffect, useRef, useState } from 'react'
import { getSttWSUrl } from './useWSConnection'
import { getDeviceId, getHotword, getSttTuning, setDeviceId, tuningMessage } from '../lib/audioDevices'
import type { WakeDetector } from '../lib/voskWake'

export type MicState = 'off' | 'loading' | 'listening' | 'armed' | 'awake'
// armed = waiting for the hotword; awake = hotword heard, the next phrase is the command

// Hotword with the "vosk" detector runs in this browser (lib/voskWake.ts): while armed the audio
// never leaves the page. Vosk closes its result after about a second of silence, so when the phrase
// is heard the detector hands over what was said after its last word, and then the live audio goes
// to lucode as a plain session; the final phrase puts the mic back to armed.
const WAKE_TIMEOUT_MS = 8000  // awake but nobody speaks: back to armed

// Posts 40 ms of mono PCM16 at a time (the voice worker wants small chunks at real time pace).
//
// Voice gate: while nobody speaks nothing is posted. The last 8 chunks (320 ms) are kept so the
// start of a phrase is not clipped, and after the last voice the audio keeps flowing for 3 s: the
// worker's silence detector needs that silence to close the phrase. The voice level is the RMS of
// the chunk against a noise floor learned from the quiet chunks.
const WORKLET = `
class Pcm extends AudioWorkletProcessor {
  constructor(o) {
    super()
    this.buf = []; this.n = 0; this.size = Math.round(sampleRate * 0.04)
    this.gate = !!(o.processorOptions && o.processorOptions.gate)
    this.open = !this.gate; this.hang = 0; this.floor = 0.005; this.ring = []
  }
  emit(buf, rms) {
    if (!this.gate) { this.port.postMessage(buf, [buf]); return }
    const voice = rms > Math.max(0.012, this.floor * 3)
    if (!voice) this.floor = Math.min(0.05, 0.95 * this.floor + 0.05 * rms)
    if (voice) {
      this.hang = 75
      if (!this.open) { for (const b of this.ring) this.port.postMessage(b, [b]); this.ring = []; this.open = true }
    } else if (this.open && --this.hang <= 0) {
      this.open = false
    }
    if (this.open) this.port.postMessage(buf, [buf])
    else { this.ring.push(buf); if (this.ring.length > 8) this.ring.shift() }
  }
  process(inputs) {
    const ch = inputs[0] && inputs[0][0]
    if (ch) {
      this.buf.push(new Float32Array(ch)); this.n += ch.length
      while (this.n >= this.size) {
        const out = new Int16Array(this.size); let o = 0, sq = 0
        while (o < this.size) {
          const head = this.buf[0], take = Math.min(head.length, this.size - o)
          for (let i = 0; i < take; i++) { const v = Math.max(-1, Math.min(1, head[i])); sq += v * v; out[o + i] = v < 0 ? v * 0x8000 : v * 0x7fff }
          o += take
          if (take === head.length) this.buf.shift(); else this.buf[0] = head.subarray(take)
        }
        this.n -= this.size
        this.emit(out.buffer, Math.sqrt(sq / this.size))
      }
    }
    return true
  }
}
registerProcessor('pcm', Pcm)
`

interface Handlers {
  onRealtime: (text: string) => void // partial text of the phrase being spoken
  onFinal: (text: string, viaHotword: boolean) => void // a finished phrase
  onLevel?: (level: number) => void // microphone level 0..1, about 15 times a second (for a meter)
}

// Streams the microphone to lucode (/ws/stt), which relays it to the Parakeet worker.
export function useWorkerMic(handlers: Handlers) {
  const [state, setState] = useState<MicState>('off')
  const [error, setError] = useState('')
  const h = useRef(handlers)
  h.current = handlers
  const muted = useRef(false)
  const wakeMode = useRef(false)
  // A partial is the whole phrase so far and replaces the previous one; a final closes the
  // phrase. A late partial of a phrase that was just closed must not reopen it.
  const ignoreRealtimeUntil = useRef(0)
  const lastFinal = useRef({ text: '', at: 0 })
  const res = useRef<{ ws?: WebSocket; ctx?: AudioContext; stream?: MediaStream; node?: AudioWorkletNode; wake?: WakeDetector; wakeTimer?: ReturnType<typeof setTimeout> }>({})

  const stop = useCallback(() => {
    const r = res.current
    res.current = {}
    clearTimeout(r.wakeTimer)
    r.wake?.dispose()
    try { if (r.ws?.readyState === WebSocket.OPEN) r.ws.send(JSON.stringify({ type: 'flush' })) } catch { /* closing */ }
    try { r.node?.disconnect() } catch { /* already gone */ }
    r.stream?.getTracks().forEach(t => t.stop())
    void r.ctx?.close().catch(() => {})
    // let the flush reach the worker before closing
    setTimeout(() => { try { r.ws?.close() } catch { /* closed */ } }, 300)
    setState('off')
  }, [])

  const start = useCallback(async () => {
    if (res.current.ws) return
    setError('')
    setState('loading')
    try {
      const base = { channelCount: 1, echoCancellation: true, noiseSuppression: true }
      const wanted = getDeviceId('input')
      let stream: MediaStream
      try {
        stream = await navigator.mediaDevices.getUserMedia({ audio: wanted ? { ...base, deviceId: { exact: wanted } } : base })
      } catch (e) {
        // the saved microphone is gone: forget it and use the default
        if (wanted && e instanceof DOMException && e.name === 'OverconstrainedError') {
          setDeviceId('input', '')
          stream = await navigator.mediaDevices.getUserMedia({ audio: base })
        } else {
          throw e
        }
      }
      // Hotword "vosk": detected here, in the browser. If the wasm or its model cannot load, the
      // worker detects it as before (the old path below) instead of leaving the mic dead.
      const hw = getHotword()
      let local = hw.enabled && hw.backend === 'vosk'
      let wake: WakeDetector | undefined
      let onWake: (after: ArrayBuffer[]) => void = () => {} // set once the socket exists
      if (local) {
        try {
          const { createWakeDetector } = await import('../lib/voskWake')
          wake = await createWakeDetector(hw.phrase, after => onWake(after))
        } catch (e) {
          console.warn('[stt] browser hotword unavailable, using the worker:', e)
          local = false
        }
      }
      const ctx = new AudioContext({ sampleRate: 16000 })
      const url = URL.createObjectURL(new Blob([WORKLET], { type: 'application/javascript' }))
      await ctx.audioWorklet.addModule(url)
      URL.revokeObjectURL(url)
      let gate = true
      try { gate = localStorage.getItem('orch_mic_gate') !== '0' } catch { /* default on */ }
      if (local) gate = false // the detector hears everything; the network only gets audio after the hotword
      const node = new AudioWorkletNode(ctx, 'pcm', { processorOptions: { gate } })
      const source = ctx.createMediaStreamSource(stream)
      source.connect(node)
      let meter: ReturnType<typeof setInterval> | undefined
      if (h.current.onLevel) {
        const an = ctx.createAnalyser()
        an.fftSize = 1024
        source.connect(an)
        const data = new Float32Array(an.fftSize)
        meter = setInterval(() => {
          an.getFloatTimeDomainData(data)
          let sq = 0
          for (const v of data) sq += v * v
          h.current.onLevel?.(Math.min(1, Math.sqrt(sq / data.length) * 4))
        }, 66)
      }

      const ws = new WebSocket(await getSttWSUrl())
      ws.binaryType = 'arraybuffer'
      res.current = { ws, ctx, stream, node, wake }
      let ready = false
      // local hotword: awake = the phrase was heard and audio is going to lucode
      let awake = false, speechSeen = false
      const backToArmed = (flush: boolean) => {
        awake = false
        clearTimeout(res.current.wakeTimer)
        if (flush && ws.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'flush' }))
        wake?.rearm()
        setState('armed')
      }
      onWake = after => {
        if (awake || !ready) return
        awake = true; speechSeen = false
        setState('awake')
        if (ws.readyState === WebSocket.OPEN) for (const b of after) ws.send(b) // said while Vosk waited for the silence
        // nobody spoke after the hotword: go back to waiting (and close what the worker may hold)
        res.current.wakeTimer = setTimeout(() => { if (!speechSeen) backToArmed(true) }, WAKE_TIMEOUT_MS)
      }
      node.port.onmessage = e => {
        const pcm = e.data as ArrayBuffer
        if (wake && !awake) { // armed: the audio stays in the browser
          if (ready && !muted.current) wake.feed(pcm)
          return
        }
        if (ready && !muted.current && ws.readyState === WebSocket.OPEN) ws.send(pcm)
      }
      wakeMode.current = hw.enabled
      ws.onopen = () => ws.send(JSON.stringify({
        type: 'start', sample_rate: ctx.sampleRate, ...tuningMessage(getSttTuning()),
        ...(hw.enabled && !wake ? { wake: true, wake_backend: hw.backend, wake_phrase: hw.backend === 'vosk' ? hw.phrase : undefined } : {}),
      }))
      ws.onmessage = e => {
        const m = JSON.parse(e.data as string) as { type: string; text?: string }
        if (m.type === 'ready') { ready = true; setState(wakeMode.current ? 'armed' : 'listening') }
        else if (m.type === 'wake') setState('awake')
        else if (m.type === 'wake_timeout') setState('armed')
        else if (m.type === 'noise') { h.current.onRealtime(''); if (wake) backToArmed(false); else if (wakeMode.current) setState('armed') }
        else if (m.type === 'wake_phrase') { if (!(m as { ok?: boolean }).ok) { setError(m.text ?? 'wake phrase rejected'); stop() } }
        else if (m.type === 'speech') { ignoreRealtimeUntil.current = 0; speechSeen = true; clearTimeout(res.current.wakeTimer) }
        else if (m.type === 'realtime') { if (Date.now() >= ignoreRealtimeUntil.current) h.current.onRealtime(m.text ?? '') }
        else if (m.type === 'final') {
          const text = (m.text ?? '').trim()
          ignoreRealtimeUntil.current = Date.now() + 1500
          // the same phrase twice in a row within a few seconds is a repeat, not new speech
          const dup = text !== '' && text === lastFinal.current.text && Date.now() - lastFinal.current.at < 4000
          if (text) lastFinal.current = { text, at: Date.now() }
          if (!dup) h.current.onFinal(text, wakeMode.current)
          else h.current.onRealtime('') // drop any partial still showing
          if (wake) backToArmed(false); else if (wakeMode.current) setState('armed')
        }
        else if (m.type === 'error') { setError(m.text ?? 'voice error'); stop() }
      }
      ws.addEventListener('close', () => { if (meter) clearInterval(meter); h.current.onLevel?.(0) })
      ws.onclose = () => { if (res.current.ws === ws) stop() }
      ws.onerror = () => { setError('could not reach lucode'); stop() }
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e))
      stop()
    }
  }, [stop])

  useEffect(() => stop, [stop])

  const setMuted = useCallback((v: boolean) => { muted.current = v }, [])
  return { state, error, start, stop, setMuted }
}
