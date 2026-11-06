import { useEffect, useState } from 'react'
import { IconBell, IconBellOff } from '@tabler/icons-react'
import { currentPushBlocker, syncPush, testPush, turnOffPush, turnOnPush } from '../../lib/push'

const message = (e: unknown) => (e instanceof Error ? e.message : String(e))

// The switch of this device, for the Push notification node. It stays out of the node's form
// because that form is read-only on a phone, which is the device that most wants it.
export function PushDevice() {
  const [on, setOn] = useState<boolean | null>(null)
  const [busy, setBusy] = useState(false)
  const [note, setNote] = useState('')
  const [blocker, setBlocker] = useState(currentPushBlocker)

  useEffect(() => {
    if (blocker) return
    let stale = false
    syncPush()
      .then(v => { if (!stale) setOn(v) })
      .catch(e => { if (!stale) { setOn(false); setNote(message(e)) } })
    return () => { stale = true }
  }, [blocker])

  async function run(action: () => Promise<void>, after: string) {
    setBusy(true)
    setNote('')
    try {
      await action()
      setNote(after)
      return true
    } catch (e) {
      setNote(message(e))
      setBlocker(currentPushBlocker()) // the person may have just blocked them
      return false
    } finally {
      setBusy(false)
    }
  }

  async function toggle() {
    const next = !on
    if (await run(next ? turnOnPush : turnOffPush, '')) setOn(next)
  }

  return (
    <div className="flows-push">
      {blocker ? (
        <span className="flows-push__note"><IconBellOff size={14} /> {blocker}</span>
      ) : (
        <>
          <button
            className={`flows-pause${on ? ' flows-pause--active' : ''}`}
            onClick={toggle}
            disabled={busy || on === null}
            aria-pressed={on === true}
            title="Push notification nodes reach this device, even with lucode closed"
          >
            {on ? <IconBell size={14} /> : <IconBellOff size={14} />}
            Notifications on this device: {on ? 'on' : 'off'}
          </button>
          {on && (
            <button className="flows-push__test" onClick={() => run(testPush, 'Sent. It should appear in a few seconds.')} disabled={busy}>
              Send a test
            </button>
          )}
          {note && <span className="flows-push__note" role="status">{note}</span>}
        </>
      )}
    </div>
  )
}
