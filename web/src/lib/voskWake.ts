// Hotword detector that runs in the browser: Vosk compiled to wasm (vosk-browser) with a grammar of
// just the wake phrase (+ [unk]). The microphone audio stays here until the phrase is heard; only
// then does useWorkerMic start sending it to lucode.
//
// Same rule as the voice worker's VoskKws (stt_worker.py): only the FINAL result counts (with a
// restricted grammar Vosk shows the phrase as a partial for sounds that merely resemble it), its text
// must contain the phrase, and every word of it needs a confidence of at least MIN_WORD_CONFIDENCE.
// The final comes out when Vosk hears about a second of silence after the speech.
//
// vosk-browser (about 6 MB of js with the wasm inside) and the Spanish small model (about 40 MB,
// served by lucode at /api/stt/vosk-model.tar.gz) are loaded the first time the hotword is turned
// on, and the model stays in memory until the page closes.
import type { Model, KaldiRecognizer } from 'vosk-browser'
import { fetchVoskModel } from '../api'

const SAMPLE_RATE = 16000
const MIN_WORD_CONFIDENCE = 0.7
const CHUNK_SECONDS = 0.04 // the microphone worklet posts 40 ms of audio at a time
const KEEP_CHUNKS = 150    // 6 s of audio are kept to hand over what was said after the phrase

let modelPromise: Promise<Model> | null = null

// Loads the model once. A failed load is not cached, so the next try starts again.
function loadModel(): Promise<Model> {
  if (!modelPromise) {
    modelPromise = (async () => {
      const [{ createModel }, url] = await Promise.all([import('vosk-browser'), fetchVoskModel()])
      try {
        return await createModel(url, -1) // -1: no Kaldi logging in the console
      } finally {
        URL.revokeObjectURL(url) // the worker has read the archive by the time the model is ready
      }
    })()
    modelPromise.catch(() => { modelPromise = null })
  }
  return modelPromise
}

// lower case and no accents, to compare what was heard with the phrase asked for
const normalize = (s: string) => s.toLowerCase().normalize('NFD').replace(/[\u0300-\u036f]/g, '').split(/\s+/).filter(Boolean).join(' ')

export interface WakeDetector {
  /** 16 kHz mono PCM16, 40 ms per chunk. Ignored while the detector is not armed. */
  feed(pcm16: ArrayBuffer): void
  /** Starts listening for the phrase again (after the command was handled). */
  rearm(): void
  dispose(): void
}

interface VoskWord { word: string; conf: number; start: number; end: number }

// Resolves once the model is loaded. onWake gets the audio chunks that came after the last word of
// the phrase (what the user said while Vosk was still waiting for the silence); the detector then
// disarms itself until rearm().
export async function createWakeDetector(phrase: string, onWake: (after: ArrayBuffer[]) => void): Promise<WakeDetector> {
  const model = await loadModel()
  const words = phrase.toLowerCase().normalize('NFC').split(/\s+/).filter(Boolean)
  if (!words.length) throw new Error('the hotword phrase is empty')
  const target = normalize(phrase)
  const grammar = JSON.stringify([words.join(' '), '[unk]'])

  let rec: KaldiRecognizer | null = null
  let armed = false
  let disposed = false
  let fed = 0                                  // chunks given to the current recognizer
  let ring: { idx: number; pcm: ArrayBuffer }[] = []

  const make = () => {
    rec?.remove()
    const r = new model.KaldiRecognizer(SAMPLE_RATE, grammar)
    r.setWords(true)
    r.on('result', m => {
      if (!armed || r !== rec) return
      const res = (m as { result: { text?: string; result?: VoskWord[] } }).result
      if (!normalize(res.text ?? '').includes(target)) return
      const heard = (res.result ?? []).filter(w => w.word !== '[unk]')
      if (!heard.length || Math.min(...heard.map(w => w.conf)) < MIN_WORD_CONFIDENCE) return
      armed = false
      const from = Math.floor(Math.max(...heard.map(w => w.end)) / CHUNK_SECONDS) // first chunk after the phrase
      onWake(ring.filter(c => c.idx >= from).map(c => c.pcm))
      ring = []
    })
    rec = r
    fed = 0
    ring = []
  }
  make()
  armed = true

  return {
    feed(pcm16) {
      if (!armed || !rec || disposed) return
      ring.push({ idx: fed++, pcm: pcm16 })
      if (ring.length > KEEP_CHUNKS) ring.shift()
      const i16 = new Int16Array(pcm16)
      const f32 = new Float32Array(i16.length)
      for (let i = 0; i < i16.length; i++) f32[i] = i16[i] / 0x8000
      rec.acceptWaveformFloat(f32, SAMPLE_RATE)
    },
    rearm() {
      if (disposed) return
      make() // a new recognizer: nothing of the previous phrase is left in it
      armed = true
    },
    dispose() {
      disposed = true
      armed = false
      rec?.remove()
      rec = null
    },
  }
}
