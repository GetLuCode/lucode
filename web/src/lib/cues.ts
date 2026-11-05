// Short audio cues for the voice mode, made with Web Audio (no files): one when the hotword is
// heard and the mic starts listening, another when the dictated text is sent.

let ctx: AudioContext | null = null

function audio(): AudioContext | null {
  try {
    ctx ??= new AudioContext()
    if (ctx.state === 'suspended') void ctx.resume() // phones start it suspended until a gesture
    return ctx
  } catch {
    return null
  }
}

// A soft tone: [frequency in Hz, start offset in s, duration in s]
type Tone = [number, number, number]

function play(tones: Tone[], volume = 0.12) {
  const a = audio()
  if (!a) return
  const t0 = a.currentTime
  for (const [freq, at, dur] of tones) {
    const osc = a.createOscillator()
    const gain = a.createGain()
    osc.type = 'sine'
    osc.frequency.value = freq
    // a quick fade in and out avoids clicks
    gain.gain.setValueAtTime(0, t0 + at)
    gain.gain.linearRampToValueAtTime(volume, t0 + at + 0.015)
    gain.gain.linearRampToValueAtTime(0, t0 + at + dur)
    osc.connect(gain).connect(a.destination)
    osc.start(t0 + at)
    osc.stop(t0 + at + dur + 0.02)
  }
}

/** The hotword was heard: now listening. Two rising notes. */
export const playListenCue = () => play([[660, 0, 0.09], [880, 0.09, 0.12]])

/** The dictated text is being sent. Two quick falling notes, a different shape from the listen cue. */
export const playSendCue = () => play([[880, 0, 0.07], [587, 0.07, 0.11]])
