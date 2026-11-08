import * as api from '../api'
import { base64UrlToBytes, pushBlocker, sameBytes } from './pushSupport'

// This device's push subscription. The hub keeps a copy, and it is what a Push notification node
// sends to.

export function currentPushBlocker(): string {
  return pushBlocker({
    secureContext: window.isSecureContext,
    serviceWorker: 'serviceWorker' in navigator,
    pushManager: 'PushManager' in window,
    notification: 'Notification' in window,
    production: process.env.NODE_ENV === 'production',
    permission: 'Notification' in window ? Notification.permission : 'default',
  })
}

// `ready` never settles if the service worker could not register, so it gets a limit.
function registration(): Promise<ServiceWorkerRegistration> {
  return Promise.race([
    navigator.serviceWorker.ready,
    new Promise<never>((_, reject) => setTimeout(() => reject(new Error('The service worker did not start: reload the page and try again.')), 10_000)),
  ])
}

function toHub(sub: PushSubscription) {
  const json = sub.toJSON()
  return { endpoint: sub.endpoint, keys: { p256dh: json.keys?.p256dh ?? '', auth: json.keys?.auth ?? '' } }
}

// Whether this device is on, telling the hub again in case it forgot (or the browser renewed the
// subscription). A subscription made with other keys could never be delivered, so it counts as off.
export async function syncPush(): Promise<boolean> {
  const sub = await (await registration()).pushManager.getSubscription()
  if (!sub || Notification.permission !== 'granted') return false
  const { public_key } = await api.getFlowPush()
  if (!sameBytes(sub.options.applicationServerKey, base64UrlToBytes(public_key))) return false
  await api.savePushSubscription(toHub(sub))
  return true
}

// Call it from a click: browsers refuse to ask for permission otherwise.
export async function turnOnPush(): Promise<void> {
  const permission = await Notification.requestPermission()
  if (permission !== 'granted') throw new Error('Notifications were not allowed for this site.')
  const { public_key } = await api.getFlowPush()
  const key = base64UrlToBytes(public_key)
  const { pushManager } = await registration()
  let sub = await pushManager.getSubscription()
  if (sub && !sameBytes(sub.options.applicationServerKey, key)) {
    await sub.unsubscribe()
    sub = null
  }
  sub ??= await pushManager.subscribe({ userVisibleOnly: true, applicationServerKey: key })
  await api.savePushSubscription(toHub(sub))
}

export async function turnOffPush(): Promise<void> {
  const sub = await (await registration()).pushManager.getSubscription()
  if (!sub) return
  await api.removePushSubscription(sub.endpoint)
  await sub.unsubscribe()
}

export async function testPush(): Promise<void> {
  const sub = await (await registration()).pushManager.getSubscription()
  if (!sub) throw new Error('This device has notifications off.')
  await api.testPushSubscription(sub.endpoint)
}

// Signing out must stop the notifications of this device: whoever signs in next is someone else.
// It never gets in the way of signing out.
export async function dropPushOnSignOut(): Promise<void> {
  if (currentPushBlocker()) return
  try {
    await turnOffPush()
  } catch (e) {
    console.warn('push', e)
  }
}
