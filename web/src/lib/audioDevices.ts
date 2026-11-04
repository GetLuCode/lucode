// Chosen microphone and speaker. Device ids belong to this browser, so they are kept in
// localStorage ("" = the system default).

const KEYS = { input: 'orch_mic_device', output: 'orch_speaker_device' } as const

export function getDeviceId(kind: keyof typeof KEYS): string {
  try { return localStorage.getItem(KEYS[kind]) ?? '' } catch { return '' }
}

export function setDeviceId(kind: keyof typeof KEYS, id: string) {
  try { localStorage.setItem(KEYS[kind], id) } catch { /* private mode */ }
}

// Hotword: keep the microphone open and only transcribe after the wake word.
export interface Hotword { enabled: boolean; backend: 'vosk' | 'oww'; phrase: string }

export function getHotword(): Hotword {
  try {
    const v = JSON.parse(localStorage.getItem('orch_hotword') ?? '{}') as Partial<Hotword>
    return { enabled: !!v.enabled, backend: v.backend === 'oww' ? 'oww' : 'vosk', phrase: v.phrase || 'hey veronica' }
  } catch {
    return { enabled: false, backend: 'vosk', phrase: 'hey veronica' }
  }
}

export function setHotword(h: Hotword) {
  try { localStorage.setItem('orch_hotword', JSON.stringify(h)) } catch { /* private mode */ }
}

// Auto-send: with plain dictation (no hotword), the first finished phrase turns the microphone
// off and is sent. Off by default.
export function getAutoSend(): boolean {
  try { return localStorage.getItem('orch_dictation_autosend') === '1' } catch { return false }
}

export function setAutoSend(on: boolean) {
  try { localStorage.setItem('orch_dictation_autosend', on ? '1' : '0') } catch { /* private mode */ }
}

// How the voice worker decides a phrase is over. Three rules, the first one that holds closes it:
// audio silence, the partial text staying unchanged for some seconds, and the partial repeating
// identically N times in a row (the worker emits one about every 0.2 s). Read when the mic starts.
export interface SttTuning {
  silence: number                 // seconds of audio silence (0.2-5)
  stableSecondsOn: boolean
  stableSeconds: number           // seconds without a change in the partial text (0.3-5)
  stableRepeatsOn: boolean
  stableRepeats: number           // identical partials in a row (1-1000)
}

export const DEFAULT_TUNING: SttTuning = { silence: 0.6, stableSecondsOn: false, stableSeconds: 2, stableRepeatsOn: true, stableRepeats: 10 }

const num = (v: unknown, def: number, min: number, max: number) =>
  typeof v === 'number' && isFinite(v) ? Math.min(max, Math.max(min, v)) : def

export function getSttTuning(): SttTuning {
  try {
    const v = JSON.parse(localStorage.getItem('orch_stt_tuning') ?? '{}') as Partial<SttTuning>
    const d = DEFAULT_TUNING
    return {
      silence: num(v.silence, d.silence, 0.2, 5),
      stableSecondsOn: typeof v.stableSecondsOn === 'boolean' ? v.stableSecondsOn : d.stableSecondsOn,
      stableSeconds: num(v.stableSeconds, d.stableSeconds, 0.3, 5),
      stableRepeatsOn: typeof v.stableRepeatsOn === 'boolean' ? v.stableRepeatsOn : d.stableRepeatsOn,
      stableRepeats: num(v.stableRepeats, d.stableRepeats, 1, 1000),
    }
  } catch {
    return DEFAULT_TUNING
  }
}

export function setSttTuning(t: SttTuning) {
  try { localStorage.setItem('orch_stt_tuning', JSON.stringify(t)) } catch { /* private mode */ }
}

// The fields of the /ws/stt start message for these settings (an off rule is sent as 0).
export function tuningMessage(t: SttTuning) {
  return {
    silence_seconds: t.silence,
    stable_seconds: t.stableSecondsOn ? t.stableSeconds : 0,
    stable_repeats: t.stableRepeatsOn ? Math.round(t.stableRepeats) : 0,
  }
}

export interface AudioDevices {
  inputs: MediaDeviceInfo[]
  outputs: MediaDeviceInfo[]
  canChooseOutput: boolean // AudioContext.setSinkId (Chromium, WebView2); not in Firefox/Safari
}

export const canChooseOutput = () => typeof AudioContext !== 'undefined' && 'setSinkId' in AudioContext.prototype

// Lists the devices. Browsers hide their names until the page has had microphone
// permission once, so when the names are blank a short capture is opened and closed.
export async function listAudioDevices(): Promise<AudioDevices> {
  let devices = await navigator.mediaDevices.enumerateDevices()
  if (devices.some(d => d.kind === 'audioinput' && !d.label)) {
    try {
      const s = await navigator.mediaDevices.getUserMedia({ audio: true })
      s.getTracks().forEach(t => t.stop())
      devices = await navigator.mediaDevices.enumerateDevices()
    } catch { /* permission denied: the list keeps generic names */ }
  }
  const named = (d: MediaDeviceInfo, i: number) => (d.label ? d : ({ ...d.toJSON(), label: `Device ${i + 1}` } as MediaDeviceInfo))
  return {
    inputs: devices.filter(d => d.kind === 'audioinput' && d.deviceId !== 'default' && d.deviceId !== 'communications').map(named),
    outputs: devices.filter(d => d.kind === 'audiooutput' && d.deviceId !== 'default' && d.deviceId !== 'communications').map(named),
    canChooseOutput: canChooseOutput(),
  }
}

// Sends an AudioContext to the chosen speaker; a device that is gone falls back to the default.
export async function applyOutputDevice(ctx: AudioContext) {
  const id = getDeviceId('output')
  if (!id || !canChooseOutput()) return
  try {
    await (ctx as AudioContext & { setSinkId(id: string): Promise<void> }).setSinkId(id)
  } catch { /* unplugged or not allowed: keep the default output */ }
}
