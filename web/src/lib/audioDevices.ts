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
