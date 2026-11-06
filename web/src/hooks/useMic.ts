import { useCallback, useRef } from 'react'
import { getSttEngine, type SttEngine } from '../lib/audioDevices'
import { useWorkerMic } from './useWorkerMic'
import { usePushToTalkMic } from './usePushToTalkMic'

type Handlers = Parameters<typeof useWorkerMic>[0]

// The dictation microphone with the engine chosen in the voice settings (read each time it is turned on):
// the local worker (streaming) or Orchard (push to talk). Same shape as useWorkerMic.
export function useMic(handlers: Handlers) {
  const worker = useWorkerMic(handlers)
  const push = usePushToTalkMic(handlers)
  const active = useRef<SttEngine>('worker')

  const start = useCallback(async () => {
    active.current = getSttEngine()
    await (active.current === 'orchard' ? push.start() : worker.start())
  }, [push.start, worker.start]) // eslint-disable-line react-hooks/exhaustive-deps
  const stop = useCallback(() => { push.stop(); worker.stop() }, [push.stop, worker.stop]) // eslint-disable-line react-hooks/exhaustive-deps
  const setMuted = worker.setMuted

  const usingPush = active.current === 'orchard'
  const cur = usingPush ? push : worker
  return { state: cur.state, error: cur.error, start, stop, setMuted, engine: active.current }
}
