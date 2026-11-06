import { useEffect, useState } from 'react'
import * as api from '../../api'

// Settings of the Orchard Run engine. Like the Fish Audio key, the Orchard key is saved on the server
// (~/.lucode/stt-orchard.json) and is never shown again: the server only says whether one is saved.
export function OrchardSettings({ onTest }: { onTest: () => void }) {
  const [configured, setConfigured] = useState(false)
  const [key, setKey] = useState('')
  const [language, setLanguage] = useState('')
  const [prompt, setPrompt] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const [saved, setSaved] = useState(false)

  useEffect(() => {
    api.getOrchardSettings().then(s => {
      setConfigured(s.configured)
      setLanguage(s.language)
      setPrompt(s.prompt)
    }).catch(e => setError(String(e?.message ?? e)))
  }, [])

  const save = async (clearKey = false) => {
    setBusy(true)
    setError('')
    setSaved(false)
    try {
      const s = await api.saveOrchardSettings({ api_key: key || undefined, clear_key: clearKey, language, prompt })
      setConfigured(s.configured)
      setKey('')
      setSaved(true)
    } catch (e) {
      setError(String((e as Error)?.message ?? e).replace(/^PUT \S+ → \d+: /, ''))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <p className="orch-dialog__note">
        Orchard Run transcribes in the cloud and bills by the audio. It is push to talk: click the microphone,
        speak, click it again, and the whole recording is sent and written into the box. The hotword and the
        phrase-end rules of the local worker do not apply. The key is saved on this computer
        (~/.lucode/stt-orchard.json) and is never shown again.
      </p>
      <label className="orch-dialog__field">
        <span>Orchard API key {configured && <em>· saved</em>}</span>
        <input type="password" autoComplete="off" value={key} onChange={e => { setKey(e.target.value); setSaved(false) }}
          placeholder={configured ? 'Leave empty to keep the saved key' : 'ork_…'} />
      </label>
      <label className="orch-dialog__field">
        <span>Language</span>
        <input value={language} onChange={e => { setLanguage(e.target.value); setSaved(false) }} placeholder="es (empty = detect it)" maxLength={8} />
      </label>
      <label className="orch-dialog__field">
        <span>Context ({prompt.length}/896)</span>
        <textarea rows={3} value={prompt} maxLength={896} onChange={e => { setPrompt(e.target.value); setSaved(false) }}
          placeholder="Names and jargon it should get right: lucode, runner, Kali…" />
      </label>
      {error && <div className="orch__error">{error}</div>}
      <div className="orch-dialog__actions">
        {configured && <button className="orch-dialog__btn orch-dialog__btn--danger" disabled={busy} onClick={() => void save(true)}>Remove key</button>}
        <span style={{ flex: 1 }} />
        {saved && <span className="orch-dialog__note" style={{ margin: 0 }}>Saved</span>}
        <button className="orch-dialog__btn" disabled={!configured} onClick={onTest}>Test…</button>
        <button className="orch-dialog__btn orch-dialog__btn--primary" disabled={busy || (!configured && !key.trim())} onClick={() => void save()}>Save</button>
      </div>
    </>
  )
}
