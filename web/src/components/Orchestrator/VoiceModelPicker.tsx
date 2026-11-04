import { useCallback, useEffect, useState } from 'react'
import * as api from '../../api'

// A server that does not know the model endpoints yet (lucode not restarted after an update)
// answers the page itself instead of JSON, which shows up as a JSON parse error.
function explain(e: unknown): string {
  const msg = String((e as Error)?.message ?? e)
  return /JSON|<!doctype|Unexpected token/i.test(msg)
    ? 'The lucode server does not have the voice model settings yet. Restart lucode and open this again.'
    : msg
}

// Picks the speech recognition model of the voice worker. The worker is one per machine and is
// shared with other apps (SttApp), so switching it restarts it for all of them.
export function VoiceModelPicker({ onTest }: { onTest: () => void }) {
  const [info, setInfo] = useState<api.SttModels | null>(null)
  const [pick, setPick] = useState('')
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')

  const refresh = useCallback(
    () => api.getSttModels().then(setInfo).catch(e => setError(explain(e))),
    [],
  )
  useEffect(() => { void refresh() }, [refresh])

  // while the model loads (the worker answers before it is ready) keep asking
  const loading = !!info?.worker && !info.worker.ready
  useEffect(() => {
    if (!loading) return
    const t = setInterval(() => { void refresh() }, 2000)
    return () => clearInterval(t)
  }, [loading, refresh])

  if (!info) return <p className="orch-dialog__note">{error || 'Loading…'}</p>

  const chosen = pick || info.selected
  const running = info.worker?.model
  const label = (id: string) => info.models.find(m => m.id === id)?.label ?? id
  const switching = !!running && running !== chosen // a running worker with another model gets replaced
  const dirty = switching || chosen !== info.selected

  const apply = async () => {
    setBusy(true)
    setError('')
    try {
      await api.setSttModel(chosen)
      setPick('')
      await refresh()
    } catch (e) {
      setError(explain(e))
    } finally {
      setBusy(false)
    }
  }

  return (
    <>
      <p className="orch-dialog__note">
        Speech recognition runs in a local voice worker. It is shared with other apps such as SttApp,
        so everything on this computer uses the same model.
      </p>
      <div className="orch-model__list" role="radiogroup" aria-label="Recognition model">
        {info.models.map(m => {
          const here = running === m.id
          return (
            <label key={m.id} className={`orch-model__item${chosen === m.id ? ' orch-model__item--on' : ''}`}>
              <input type="radio" name="stt-model" value={m.id} checked={chosen === m.id} disabled={busy}
                onChange={() => setPick(m.id)} />
              <span className="orch-model__body">
                <span className="orch-model__name">{m.label}{m.recommended && <em>recommended</em>}</span>
                <span className="orch-model__meta">{m.device === 'gpu' ? 'GPU' : 'CPU'} · {m.memory}</span>
              </span>
              {here && (
                <span className={`orch-model__state${info.worker?.ready ? ' orch-model__state--ready' : ''}`}>
                  {info.worker?.ready ? 'running' : 'loading…'}
                </span>
              )}
            </label>
          )
        })}
      </div>
      <p className="orch-dialog__note">
        {info.worker
          ? <>Running now: <strong>{label(info.worker.model)}</strong>{info.worker.ready ? '.' : ', loading the model (about half a minute).'}</>
          : 'The worker is not running. It starts with the chosen model when you turn the microphone on.'}
        {switching && ' Switching restarts it: anything connected to it, SttApp included, loses its session.'}
      </p>
      {error && <div className="orch__error">{error}</div>}
      <div className="orch-dialog__actions">
        <button className="orch-dialog__btn orch-dialog__btn--primary" disabled={busy || !dirty} onClick={() => void apply()}>
          {busy ? 'Applying…' : switching ? 'Switch model' : 'Use this model'}
        </button>
        <button className="orch-dialog__btn" onClick={onTest}>Test speech recognition…</button>
      </div>
      <p className="orch-dialog__note">The test shows what the worker hears while you speak. Nothing is sent to the model.</p>
    </>
  )
}
