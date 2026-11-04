import { useEffect, useRef, useState } from 'react'
import { IconX } from '@tabler/icons-react'
import * as api from '../../api'
import { SttTester } from './SttTester'
import { canChooseOutput, getDeviceId, getHotword, listAudioDevices, setDeviceId, setHotword, type AudioDevices } from '../../lib/audioDevices'

// Voice output settings. The Fish Audio key is write-only: the server only says
// whether one is saved.
export function VoiceSettingsDialog({ onClose, onSaved }: { onClose: () => void; onSaved: (configured: boolean) => void }) {
  const [configured, setConfigured] = useState(false)
  const [key, setKey] = useState('')
  const [model, setModel] = useState('')
  const [voice, setVoice] = useState('')
  const [speed, setSpeed] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const pressedBackdrop = useRef(false)
  const [devices, setDevices] = useState<AudioDevices>({ inputs: [], outputs: [], canChooseOutput: canChooseOutput() })
  const [micId, setMicId] = useState(() => getDeviceId('input'))
  const [spkId, setSpkId] = useState(() => getDeviceId('output'))
  const [hw, setHw] = useState(getHotword)
  const [testing, setTesting] = useState(false)
  const changeHw = (patch: Partial<typeof hw>) => { const n = { ...hw, ...patch }; setHw(n); setHotword(n) }

  // Devices apply on their own (they are per browser); they follow plugging and unplugging.
  useEffect(() => {
    let alive = true
    const refresh = () => { listAudioDevices().then(d => { if (alive) setDevices(d) }).catch(() => {}) }
    refresh()
    navigator.mediaDevices?.addEventListener('devicechange', refresh)
    return () => { alive = false; navigator.mediaDevices?.removeEventListener('devicechange', refresh) }
  }, [])

  useEffect(() => {
    api.getTTSSettings().then(s => {
      setConfigured(s.configured)
      setModel(s.model)
      setVoice(s.voice_id)
      setSpeed(s.speed ? String(s.speed) : '')
      onSaved(s.configured)
    }).catch(e => setError(String(e?.message ?? e)))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  useEffect(() => {
    const onKey = (e: KeyboardEvent) => { if (e.key === 'Escape') onClose() }
    window.addEventListener('keydown', onKey)
    return () => window.removeEventListener('keydown', onKey)
  }, [onClose])

  const save = async (clearKey = false) => {
    const sp = speed.trim() === '' ? 0 : Number(speed)
    if (Number.isNaN(sp) || (sp !== 0 && (sp < 0.5 || sp > 2))) { setError('Speed must be between 0.5 and 2'); return }
    setBusy(true)
    setError('')
    try {
      const s = await api.saveTTSSettings({ api_key: key || undefined, clear_key: clearKey, model, voice_id: voice, speed: sp })
      setConfigured(s.configured)
      setKey('')
      onSaved(s.configured)
      if (!clearKey) onClose()
    } catch (e) {
      setError(String((e as Error)?.message ?? e))
    } finally {
      setBusy(false)
    }
  }

  // a click closes only when the press and the release are both on the backdrop
  return (
    <div
      className="orch-dialog__backdrop"
      onMouseDown={e => { pressedBackdrop.current = e.target === e.currentTarget }}
      onMouseUp={e => { if (pressedBackdrop.current && e.target === e.currentTarget) onClose(); pressedBackdrop.current = false }}
    >
      <div className="orch-dialog" role="dialog" aria-label="Voice settings">
        <div className="orch-dialog__header">
          <h3>Voice</h3>
          <button className="orch__icon-btn" onClick={onClose} aria-label="Close"><IconX size={16} /></button>
        </div>
        <label className="orch-dialog__field">
          <span>Microphone</span>
          <select value={micId} onChange={e => { setMicId(e.target.value); setDeviceId('input', e.target.value) }}>
            <option value="">System default</option>
            {micId && !devices.inputs.some(d => d.deviceId === micId) && <option value={micId}>(unplugged device)</option>}
            {devices.inputs.map(d => <option key={d.deviceId} value={d.deviceId}>{d.label}</option>)}
          </select>
        </label>
        <label className="orch-dialog__field">
          <span>Speaker {!devices.canChooseOutput && <em>· not supported by this browser</em>}</span>
          <select disabled={!devices.canChooseOutput} value={spkId} onChange={e => { setSpkId(e.target.value); setDeviceId('output', e.target.value) }}>
            <option value="">System default</option>
            {spkId && !devices.outputs.some(d => d.deviceId === spkId) && <option value={spkId}>(unplugged device)</option>}
            {devices.outputs.map(d => <option key={d.deviceId} value={d.deviceId}>{d.label}</option>)}
          </select>
        </label>
        <div className="orch-dialog__rule" />
        <button className="orch-dialog__btn" onClick={() => setTesting(true)}>Test speech recognition…</button>
        <p className="orch-dialog__note">Speak and see what the voice worker hears. Nothing is sent to the model.</p>
        <div className="orch-dialog__rule" />
        <label className="orch-dialog__check">
          <input type="checkbox" checked={hw.enabled} onChange={e => changeHw({ enabled: e.target.checked })} />
          <span>Hotword: keep the microphone open and send what I say after it</span>
        </label>
        {hw.enabled && (
          <>
            <label className="orch-dialog__field">
              <span>Detector</span>
              <select value={hw.backend} onChange={e => changeHw({ backend: e.target.value === 'oww' ? 'oww' : 'vosk' })}>
                <option value="vosk">My own phrase (Vosk, Spanish)</option>
                <option value="oww">“hey jarvis” (openWakeWord)</option>
              </select>
            </label>
            {hw.backend === 'vosk' && (
              <label className="orch-dialog__field">
                <span>Phrase</span>
                <input value={hw.phrase} onChange={e => changeHw({ phrase: e.target.value })} placeholder="hey veronica" />
              </label>
            )}
            <p className="orch-dialog__note">Takes effect the next time you turn the microphone on. Everything is detected on this computer.</p>
          </>
        )}
        <div className="orch-dialog__rule" />
        <p className="orch-dialog__note">
          Text to speech with Fish Audio, through the local voice worker. Fish bills by the text it speaks.
          The key is saved on this computer (~/.lucode/tts.json) and is never shown again.
        </p>
        <label className="orch-dialog__field">
          <span>Fish Audio API key {configured && <em>· saved</em>}</span>
          <input type="password" autoComplete="off" value={key} onChange={e => setKey(e.target.value)}
            placeholder={configured ? 'Leave empty to keep the saved key' : 'Paste your key'} />
        </label>
        <label className="orch-dialog__field">
          <span>Model</span>
          <input value={model} onChange={e => setModel(e.target.value)} placeholder="s2.1-pro-free (default)" />
        </label>
        <label className="orch-dialog__field">
          <span>Voice id</span>
          <input value={voice} onChange={e => setVoice(e.target.value)} placeholder="reference_id (empty = the model default)" />
        </label>
        <label className="orch-dialog__field">
          <span>Speed (0.5 – 2)</span>
          <input value={speed} onChange={e => setSpeed(e.target.value)} placeholder="1" inputMode="decimal" />
        </label>
        {error && <div className="orch__error">{error}</div>}
        <div className="orch-dialog__actions">
          {configured && <button className="orch-dialog__btn orch-dialog__btn--danger" disabled={busy} onClick={() => save(true)}>Remove key</button>}
          <span style={{ flex: 1 }} />
          <button className="orch-dialog__btn" onClick={onClose}>Cancel</button>
          <button className="orch-dialog__btn orch-dialog__btn--primary" disabled={busy || (!configured && !key.trim())} onClick={() => save()}>Save</button>
        </div>
      </div>
      {testing && <SttTester onClose={() => setTesting(false)} />}
    </div>
  )
}
