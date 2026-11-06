import { useState } from 'react'
import { IconPlayerPlay, IconPlayerStop } from '@tabler/icons-react'
import { useWorkerTTS } from '../../hooks/useWorkerTTS'

// Plays a typed text with the saved voice settings (the same path the chat uses), to try the model, voice and
// speed without a conversation. The last texts are kept in this browser.

const KEY = 'orch_tts_test_text'
const SAMPLE = 'Hola, soy tu supervisor. [laughs] Ya terminé la tarea y los tests pasan.'

function initialText(): string {
  try { return localStorage.getItem(KEY) || SAMPLE } catch { return SAMPLE }
}

export function TtsTester({ configured }: { configured: boolean }) {
  const [text, setText] = useState(initialText)
  const tts = useWorkerTTS()
  const busy = tts.state !== 'idle'

  const change = (v: string) => {
    setText(v)
    try { localStorage.setItem(KEY, v) } catch { /* private mode */ }
  }

  return (
    <div className="orch-tts-test">
      <label className="orch-dialog__field">
        <span>Test the voice</span>
        <textarea rows={3} value={text} onChange={e => change(e.target.value)} placeholder="Text to speak" />
      </label>
      <div className="orch-tts-test__row">
        <button
          className="orch-dialog__btn"
          disabled={!configured || (!busy && !text.trim())}
          onClick={() => (busy ? tts.stop() : void tts.speak(text))}
        >
          {busy ? <IconPlayerStop size={14} /> : <IconPlayerPlay size={14} />}
          <span>{busy ? 'Stop' : 'Play'}</span>
        </button>
        <span className="orch-dialog__note">
          {!configured ? 'Save a Fish Audio key first.'
            : tts.state === 'loading' ? 'Synthesizing…'
            : tts.state === 'speaking' ? 'Speaking…'
            : 'Uses the saved settings (Save first if you changed them). Repeated texts come from the local cache.'}
        </span>
      </div>
      {tts.error && <div className="orch__error">{tts.error}</div>}
    </div>
  )
}
