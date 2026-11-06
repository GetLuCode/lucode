// The parts of push notifications that need no browser, so they can be tested without one.

export interface PushEnv {
  secureContext: boolean
  serviceWorker: boolean
  pushManager: boolean
  notification: boolean
  production: boolean
  permission: string
}

// Why this page cannot receive notifications, in words a person can act on. Empty: it can.
export function pushBlocker(env: PushEnv): string {
  if (!env.secureContext) return 'Needs HTTPS: browsers only allow notifications on an https page or on localhost.'
  if (!env.serviceWorker || !env.pushManager || !env.notification) {
    return 'This browser has no push notifications here. On an iPhone, add lucode to the home screen and open it from there.'
  }
  if (!env.production) return 'Not available in the dev server: open the hub\'s own port, where the service worker runs.'
  if (env.permission === 'denied') return 'Notifications are blocked for this site: allow them in the browser\'s settings.'
  return ''
}

// A VAPID key travels as base64url, and the browser wants its bytes.
export function base64UrlToBytes(text: string): Uint8Array<ArrayBuffer> {
  const binary = atob(text.replace(/-/g, '+').replace(/_/g, '/').padEnd(Math.ceil(text.length / 4) * 4, '='))
  const out = new Uint8Array(new ArrayBuffer(binary.length))
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i)
  return out
}

export function sameBytes(a: ArrayBuffer | null | undefined, b: Uint8Array): boolean {
  if (!a || a.byteLength !== b.length) return false
  const bytes = new Uint8Array(a)
  return bytes.every((v, i) => v === b[i])
}
