import { useEffect, useRef, useState, type KeyboardEvent } from 'react'
import { IconX } from '@tabler/icons-react'
import * as api from '../../api'
import { SttTester } from './SttTester'
import { VoiceModelPicker } from './VoiceModelPicker'
import { canChooseOutput, DEFAULT_TUNING, getAutoSend, getDeviceId, getHotword, getSttTuning, listAudioDevices, setAutoSend, setDeviceId, setHotword, setSttTuning, type AudioDevices, type SttTuning } from '../../lib/audioDevices'

// Voice settings, in tabs. Everything applies on its own (it is kept in this browser) except the
// Fish Audio settings, which are saved on the server with the Save button of their tab. The Fish
// Audio key is write-only: the server only says whether one is saved.

type TabId = 'audio' | 'recognition' | 'phrase' | 'hotword' | 'speech'
const TABS: { id: TabId; label: string }[] = [
  { id: 'audio', label: 'Audio' },
  { id: 'recognition', label: 'Recognition' },
  { id: 'phrase', label: 'Phrase end' },
  { id: 'hotword', label: 'Hotword' },
  { id: 'speech', label: 'Voice output' },
]
const TAB_KEY = 'orch_voice_tab'
const firstTab = (): TabId => {
  try {
    const v = localStorage.getItem(TAB_KEY)
    if (TABS.some(t => t.id === v)) return v as TabId
  } catch { /* private mode */ }
  return 'audio'
}

// One phrase-end rule: an optional on/off switch, a slider and its value.
function TuneRule({ label, hint, unit, min, max, step, value, onChange, on, onToggle }: {
  label: string; hint: string; unit: string; min: number; max: number; step: number
  value: number; onChange: (v: number) => void
  on?: boolean; onToggle?: (on: boolean) => void
}) {
  const off = onToggle !== undefined && !on
  return (
    <div className={`orch-tune${off ? ' orch-tune--off' : ''}`}>
      <div className="orch-tune__head">
        {onToggle
          ? <label className="orch-dialog__check"><input type="checkbox" checked={!!on} onChange={e => onToggle(e.target.checked)} /><span>{label}</span></label>
          : <span className="orch-tune__label">{label}</span>}
        <span className="orch-tune__value">{off ? 'off' : `${step < 1 ? value.toFixed(1) : value}${unit}`}</span>
      </div>
      <input type="range" min={min} max={max} step={step} value={Math.min(max, value)} disabled={off}
        onChange={e => onChange(Number(e.target.value))} aria-label={label} />
      <span className="orch-tune__hint">{hint}</span>
    </div>
  )
}

export function VoiceSettingsDialog({ onClose, onSaved }: { onClose: () => void; onSaved: (configured: boolean) => void }) {
  const [tab, setTabState] = useState<TabId>(firstTab)
  const tabRefs = useRef<Partial<Record<TabId, HTMLButtonElement | null>>>({})
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
  const [autoSend, setAutoSendState] = useState(getAutoSend)
  const [testing, setTesting] = useState(false)
  const [tune, setTune] = useState(getSttTuning)
  const changeTune = (patch: Partial<SttTuning>) => { const n = { ...tune, ...patch }; setTune(n); setSttTuning(n) }
  const changeHw = (patch: Partial<typeof hw>) => { const n = { ...hw, ...patch }; setHw(n); setHotword(n) }

  const setTab = (id: TabId) => {
    setTabState(id)
    setError('')
    try { localStorage.setItem(TAB_KEY, id) } catch { /* private mode */ }
  }
  // arrows, Home and End move between the tabs (the tab list is one tab stop)
  const onTabKey = (e: KeyboardEvent) => {
    const i = TABS.findIndex(t => t.id === tab)
    let next = -1
    if (e.key === 'ArrowDown' || e.key === 'ArrowRight') next = (i + 1) % TABS.length
    else if (e.key === 'ArrowUp' || e.key === 'ArrowLeft') next = (i - 1 + TABS.length) % TABS.length
    else if (e.key === 'Home') next = 0
    else if (e.key === 'End') next = TABS.length - 1
    if (next < 0) return
    e.preventDefault()
    setTab(TABS[next].id)
    tabRefs.current[TABS[next].id]?.focus()
  }

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
    const onKey = (e: globalThis.KeyboardEvent) => { if (e.key === 'Escape') onClose() }
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
      <div className="orch-dialog orch-dialog--wide" role="dialog" aria-label="Voice settings">
        <div className="orch-dialog__header">
          <h3>Voice</h3>
          <button className="orch__icon-btn" onClick={onClose} aria-label="Close"><IconX size={16} /></button>
        </div>
        <div className="orch-voice">
          <div className="orch-voice__tabs" role="tablist" aria-label="Voice settings sections" onKeyDown={onTabKey}>
            {TABS.map(t => (
              <button
                key={t.id}
                ref={el => { tabRefs.current[t.id] = el }}
                id={`voice-tab-${t.id}`}
                role="tab"
                aria-selected={tab === t.id}
                aria-controls={`voice-panel-${t.id}`}
                tabIndex={tab === t.id ? 0 : -1}
                className="orch-voice__tab"
                onClick={() => setTab(t.id)}
              >{t.label}</button>
            ))}
          </div>
          <div className="orch-voice__panel" role="tabpanel" id={`voice-panel-${tab}`} aria-labelledby={`voice-tab-${tab}`}>
            {tab === 'audio' && (
              <>
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
                <p className="orch-dialog__note">The microphone and the speaker are remembered by this browser.</p>
              </>
            )}
            {tab === 'recognition' && <VoiceModelPicker onTest={() => setTesting(true)} />}
            {tab === 'phrase' && (
              <>
                <p className="orch-dialog__note">
                  End of a phrase. The first rule that holds closes it; a rule that is off never does.
                  Takes effect the next time you turn the microphone on.
                </p>
                <TuneRule
                  label="Silence" unit="s" min={0.2} max={5} step={0.1}
                  hint="Seconds of audio silence."
                  value={tune.silence} onChange={v => changeTune({ silence: v })}
                />
                <TuneRule
                  label="Text unchanged" unit="s" min={0.3} max={5} step={0.1}
                  hint="Seconds the partial text stays the same."
                  on={tune.stableSecondsOn} onToggle={on => changeTune({ stableSecondsOn: on })}
                  value={tune.stableSeconds} onChange={v => changeTune({ stableSeconds: v })}
                />
                <TuneRule
                  label="Repeated text" unit="×" min={1} max={100} step={1}
                  hint="Identical partials in a row (about 0.2 s each; 10 ≈ 2 s)."
                  on={tune.stableRepeatsOn} onToggle={on => changeTune({ stableRepeatsOn: on })}
                  value={tune.stableRepeats} onChange={v => changeTune({ stableRepeats: v })}
                />
                <label className="orch-dialog__check">
                  <input type="checkbox" checked={autoSend} onChange={e => { setAutoSendState(e.target.checked); setAutoSend(e.target.checked) }} />
                  <span>Auto: when a phrase ends, turn the microphone off and send it</span>
                </label>
                <p className="orch-dialog__note">Only for plain dictation; it does nothing while the hotword is on.</p>
                <button className="orch-dialog__link" onClick={() => { setTune(DEFAULT_TUNING); setSttTuning(DEFAULT_TUNING) }}>Reset to defaults</button>
              </>
            )}
            {tab === 'hotword' && (
              <>
                <label className="orch-dialog__check">
                  <input type="checkbox" checked={hw.enabled} onChange={e => changeHw({ enabled: e.target.checked })} />
                  <span>Hotword: keep the microphone open and send what I say after it</span>
                </label>
                {hw.enabled ? (
                  <>
                    <label className="orch-dialog__field">
                      <span>Detector</span>
                      <select value={hw.backend} onChange={e => changeHw({ backend: e.target.value === 'oww' ? 'oww' : 'vosk' })}>
                        <option value="vosk">My own phrase (Vosk in the browser, Spanish)</option>
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
                    {hw.backend === 'vosk' && (
                      <p className="orch-dialog__note">
                        The phrase is detected inside this page: no audio is sent to lucode until you say it (say it, pause a moment, then
                        the command). The first time it downloads a 40 MB model, which the browser keeps.
                      </p>
                    )}
                  </>
                ) : (
                  <p className="orch-dialog__note">Off: the microphone sends everything it hears. Turn it on to only react after a wake phrase.</p>
                )}
              </>
            )}
            {tab === 'speech' && (
              <>
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
              </>
            )}
          </div>
        </div>
        {error && tab !== 'recognition' && <div className="orch__error">{error}</div>}
        <div className="orch-dialog__actions">
          {tab === 'speech' && configured && <button className="orch-dialog__btn orch-dialog__btn--danger" disabled={busy} onClick={() => save(true)}>Remove key</button>}
          <span style={{ flex: 1 }} />
          <button className="orch-dialog__btn" onClick={onClose}>{tab === 'speech' ? 'Cancel' : 'Close'}</button>
          {tab === 'speech' && (
            <button className="orch-dialog__btn orch-dialog__btn--primary" disabled={busy || (!configured && !key.trim())} onClick={() => save()}>Save</button>
          )}
        </div>
      </div>
      {testing && <SttTester onClose={() => setTesting(false)} />}
    </div>
  )
}
