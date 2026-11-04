import { useEffect, useRef, useState } from 'react'
import { IconX } from '@tabler/icons-react'
import { useWorkerMic, type MicState } from '../../hooks/useWorkerMic'
import { getHotword } from '../../lib/audioDevices'

const LABEL: Record<MicState, string> = {
  off: 'Microphone off',
  loading: 'Loading the model…',
  listening: 'Listening',
  armed: 'Waiting for the hotword',
  awake: 'Hotword heard: say the phrase',
}

// Tries the speech recognition on its own: the same microphone, worker and hotword settings as
// the orchestrator chat, but what it hears is only listed here. Nothing reaches the model.
export function SttTester({ onClose }: { onClose: () => void }) {
  const [live, setLive] = useState('')
  const [lines, setLines] = useState<{ id: number; text: string; hotword: boolean; at: string }[]>([])
  const [level, setLevel] = useState(0)
  const next = useRef(1)
  const list = useRef<HTMLDivElement>(null)
  const mic = useWorkerMic({
    onRealtime: setLive,
    onFinal: (text, hotword) => {
      setLive('')
      if (!text) return
      setLines(l => [...l, { id: next.current++, text, hotword, at: new Date().toLocaleTimeString() }])
    },
    onLevel: setLevel,
  })

  // Escape closes this window only, not the settings under it
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      e.stopPropagation()
      onClose()
    }
    document.addEventListener('keydown', onKey, true)
    return () => document.removeEventListener('keydown', onKey, true)
  }, [onClose])

  useEffect(() => { list.current?.scrollTo({ top: list.current.scrollHeight }) }, [lines, live])

  const hw = getHotword()
  const on = mic.state !== 'off'
  return (
    <div className="orch-dialog__backdrop" onMouseDown={e => e.stopPropagation()} onMouseUp={e => e.stopPropagation()}>
      <div className="orch-dialog stt-test" role="dialog" aria-label="Speech recognition test">
        <div className="orch-dialog__header">
          <h3>Speech recognition test</h3>
          <button className="orch__icon-btn" onClick={onClose} aria-label="Close"><IconX size={16} /></button>
        </div>
        <p className="orch-dialog__note">
          Nothing you say here is sent to the model. Uses the microphone and hotword chosen in Voice
          {hw.enabled ? ` (hotword on: ${hw.backend === 'vosk' ? `“${hw.phrase}”` : '“hey jarvis”'})` : ' (hotword off)'}.
        </p>
        <div className="stt-test__bar">
          <button
            className={`orch-dialog__btn ${on ? '' : 'orch-dialog__btn--primary'}`}
            onClick={() => (on ? mic.stop() : void mic.start())}
          >
            {on ? 'Turn microphone off' : 'Turn microphone on'}
          </button>
          <div className="stt-test__meter" role="meter" aria-label="Microphone level" aria-valuemin={0} aria-valuemax={1} aria-valuenow={level}>
            <div className="stt-test__meter-fill" style={{ transform: `scaleX(${level.toFixed(2)})` }} />
          </div>
          <span className={`stt-test__state stt-test__state--${mic.state}`}>{LABEL[mic.state]}</span>
        </div>
        <div className="stt-test__list" ref={list} aria-live="polite">
          {lines.length === 0 && !live.trim() && <div className="stt-test__empty">What the worker hears shows up here.</div>}
          {lines.map(l => (
            <div key={l.id} className="stt-test__line">
              <span className="stt-test__time">{l.at}</span>
              {l.hotword && <span className="stt-test__tag">hotword</span>}
              <span>{l.text}</span>
            </div>
          ))}
          {live.trim() && (
            // Same row layout as a finished line (time column + text), like the grey "hablando…" bubble
            // of SttApp: when the final arrives only the colour and the label change, nothing jumps.
            <div className="stt-test__line stt-test__line--live">
              <span className="stt-test__time">speaking…</span>
              <span>{live.trim()}</span>
            </div>
          )}
        </div>
        {mic.error && <div className="orch__error">{mic.error}</div>}
        <div className="orch-dialog__actions">
          <button className="orch-dialog__btn" onClick={() => { setLines([]); setLive('') }} disabled={lines.length === 0 && !live}>Clear</button>
          <span style={{ flex: 1 }} />
          <button className="orch-dialog__btn" onClick={onClose}>Close</button>
        </div>
      </div>
    </div>
  )
}
