// The GitHub picture of a commit author. Git only knows a name and an address, and GitHub serves
// the picture of the account an address belongs to. An address with no account gets one generic
// image, so that image is compared against a lookup that cannot match: the author then keeps
// their initials. This endpoint is not documented, and any failure also means initials.
const SIZE = 64 // enough for every place an avatar is drawn, so one lookup per address
const NOBODY = 'lucode-no-such-account@example.invalid'

const lookup = (email: string) =>
  `https://avatars.githubusercontent.com/u/e?email=${encodeURIComponent(email)}&s=${SIZE}`

async function fetchImage(email: string): Promise<Blob> {
  const res = await fetch(lookup(email), { credentials: 'omit', referrerPolicy: 'no-referrer' })
  if (!res.ok) throw new Error(`avatar ${res.status}`)
  return res.blob()
}

async function sameBytes(a: Blob, b: Blob): Promise<boolean> {
  if (a.size !== b.size) return false
  const [x, y] = await Promise.all([a.arrayBuffer(), b.arrayBuffer()])
  const bx = new Uint8Array(x)
  const by = new Uint8Array(y)
  return bx.every((v, i) => v === by[i])
}

let generic: Promise<Blob> | null = null
const pending = new Map<string, Promise<string | null>>()
const resolved = new Map<string, string | null>()

const keyOf = (email: string) => email.trim().toLowerCase()

// What is already known, to draw the picture at once when a row comes back into view.
export function knownAvatar(email: string | undefined): string | null | undefined {
  return email ? resolved.get(keyOf(email)) : null
}

// The address of the picture, or null to keep the initials. Each address is asked once per page load.
export function githubAvatar(email: string | undefined): Promise<string | null> {
  const key = keyOf(email ?? '')
  if (!key) return Promise.resolve(null)
  let p = pending.get(key)
  if (!p) {
    p = (async () => {
      try {
        generic ??= fetchImage(NOBODY).catch(e => { generic = null; throw e })
        const [image, nobody] = await Promise.all([fetchImage(key), generic])
        return (await sameBytes(image, nobody)) ? null : URL.createObjectURL(image)
      } catch {
        return null // offline or blocked: initials
      }
    })().then(url => { resolved.set(key, url); return url })
    pending.set(key, p)
  }
  return p
}
