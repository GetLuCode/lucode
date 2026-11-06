import { test } from 'node:test'
import assert from 'node:assert/strict'
import { readFileSync } from 'node:fs'
import vm from 'node:vm'
import { base64UrlToBytes, pushBlocker, sameBytes } from '../src/lib/pushSupport.ts'

const able = { secureContext: true, serviceWorker: true, pushManager: true, notification: true, production: true, permission: 'default' }

test('a page that can receive notifications has no blocker', () => {
  assert.equal(pushBlocker(able), '')
  assert.equal(pushBlocker({ ...able, permission: 'granted' }), '')
})

test('each blocker says what to do about it', () => {
  assert.match(pushBlocker({ ...able, secureContext: false, serviceWorker: false, pushManager: false }), /HTTPS/)
  assert.match(pushBlocker({ ...able, pushManager: false }), /home screen/)
  assert.match(pushBlocker({ ...able, production: false }), /dev server/)
  assert.match(pushBlocker({ ...able, permission: 'denied' }), /settings/)
})

test('a VAPID key decodes to its bytes, with or without padding', () => {
  assert.deepEqual([...base64UrlToBytes('-_8')], [0xfb, 0xff])
  assert.deepEqual([...base64UrlToBytes('AQID')], [1, 2, 3])
  assert.equal(base64UrlToBytes('A'.repeat(87)).length, 65) // a real key's length
})

test('sameBytes compares a stored key with a decoded one', () => {
  const key = Uint8Array.from([1, 2, 3])
  assert.equal(sameBytes(key.buffer, Uint8Array.from([1, 2, 3])), true)
  assert.equal(sameBytes(key.buffer, Uint8Array.from([1, 2, 4])), false)
  assert.equal(sameBytes(key.buffer, Uint8Array.from([1, 2])), false)
  assert.equal(sameBytes(null, key), false)
})

// The real sw.js, run with a fake browser around it.
function loadWorker(windows: { url: string; focus(): Promise<unknown>; navigate(u: string): Promise<unknown> }[] = []) {
  const listeners: Record<string, (e: unknown) => void> = {}
  const shown: { title: string; options: { body: string; tag: string; icon: string; data: { url: string } } }[] = []
  const opened: string[] = []
  const self = {
    location: new URL('https://hub.test/sw.js'),
    addEventListener: (type: string, fn: (e: unknown) => void) => { listeners[type] = fn },
    registration: { showNotification: async (title: string, options: (typeof shown)[number]['options']) => { shown.push({ title, options }) } },
    clients: {
      matchAll: async () => windows,
      openWindow: async (u: string) => { opened.push(u) },
      claim: async () => {},
    },
    skipWaiting: () => {},
  }
  // as a classic script, the way it is registered: import and import.meta do not work in one
  const source = readFileSync(new URL('../src/sw.js', import.meta.url), 'utf8')
  vm.runInNewContext(source, { self, URL, caches: {}, fetch: () => {} })
  const fire = async (type: string, event: Record<string, unknown>) => {
    let done: unknown = null
    listeners[type]({ ...event, waitUntil: (p: unknown) => { done = p } })
    await done
  }
  return { fire, shown, opened }
}

test('a push shows its title, text, tag and link', async () => {
  const sw = loadWorker()
  const payload = { title: 'Ready', body: 'Fix the bug', tag: 'flow-step-7', url: '/projects/p/flows?flow=f&run=r', icon: '/icon-192.abc.png' }
  await sw.fire('push', { data: { json: () => payload } })
  assert.equal(sw.shown.length, 1)
  assert.equal(sw.shown[0].title, 'Ready')
  assert.equal(sw.shown[0].options.body, 'Fix the bug')
  assert.equal(sw.shown[0].options.tag, 'flow-step-7')
  assert.equal(sw.shown[0].options.data.url, payload.url)
  assert.equal(sw.shown[0].options.icon, payload.icon)
})

test('a push that cannot be read still shows something', async () => {
  for (const data of [null, { json: () => { throw new Error('not json') } }]) {
    const sw = loadWorker()
    await sw.fire('push', { data })
    assert.equal(sw.shown[0].title, 'lucode')
  }
})

const notification = (url?: string) => {
  const n = { closed: false, data: { url }, close() { n.closed = true } }
  return n
}

test('clicking opens the link in a window of the app if there is one', async () => {
  const went: string[] = []
  let focused = 0
  const sw = loadWorker([
    { url: 'https://other.test/', focus: async () => { throw new Error('not ours') }, navigate: async () => {} },
    { url: 'https://hub.test/x', focus: async () => { focused++ }, navigate: async (u) => { went.push(u) } },
  ])
  const n = notification('/projects/p/flows?run=r')
  await sw.fire('notificationclick', { notification: n })
  assert.equal(n.closed, true)
  assert.equal(focused, 1)
  assert.deepEqual(went, ['https://hub.test/projects/p/flows?run=r'])
  assert.deepEqual(sw.opened, [])
})

test('clicking opens a new window when none can take it', async () => {
  const sw = loadWorker([{ url: 'https://hub.test/x', focus: async () => {}, navigate: async () => { throw new Error('not controlled') } }])
  await sw.fire('notificationclick', { notification: notification('/a') })
  assert.deepEqual(sw.opened, ['https://hub.test/a'])
})

test('clicking never leaves the app', async () => {
  const sw = loadWorker()
  await sw.fire('notificationclick', { notification: notification('https://evil.test/x') })
  assert.deepEqual(sw.opened, ['https://hub.test/'])
})
