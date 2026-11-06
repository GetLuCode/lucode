// Service worker minimo: hace la app instalable y deja el shell disponible sin red.
// Nunca toca /api ni WebSockets: el estado siempre viene en vivo del hub.
const CACHE = 'lucode-shell-v1'

self.addEventListener('install', () => self.skipWaiting())

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches
      .keys()
      .then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim()),
  )
})

// Una notificacion de un nodo Push notification. Se muestra siempre: el navegador castiga a
// quien recibe un push sin ensenar nada. El tag hace que el mismo paso reenviado no se duplique.
self.addEventListener('push', (event) => {
  let data = {}
  try {
    data = event.data ? event.data.json() : {}
  } catch {
    // se muestra igualmente, con los textos por defecto
  }
  event.waitUntil(
    self.registration.showNotification(data.title || 'lucode', {
      body: data.body || '',
      icon: data.icon, // lo manda el hub: Parcel le pone un hash y aqui no se puede resolver
      tag: data.tag,
      data: { url: data.url || '/' },
    }),
  )
})

// Abre lo que dice la notificacion, reutilizando una ventana de la app si hay una.
self.addEventListener('notificationclick', (event) => {
  event.notification.close()
  const target = new URL(event.notification.data?.url || '/', self.location.origin)
  const url = target.origin === self.location.origin ? target.href : self.location.origin + '/'
  event.waitUntil(
    self.clients.matchAll({ type: 'window', includeUncontrolled: true }).then(async (windows) => {
      for (const w of windows) {
        if (new URL(w.url).origin !== self.location.origin) continue
        try {
          await w.focus()
          await w.navigate(url)
          return
        } catch {
          // esta ventana no se deja (aun no la controla este worker): se abre otra
        }
      }
      await self.clients.openWindow(url)
    }),
  )
})

self.addEventListener('fetch', (event) => {
  const req = event.request
  if (req.method !== 'GET') return
  const url = new URL(req.url)
  if (url.origin !== self.location.origin || url.pathname.startsWith('/api/')) return

  // Navegacion: red primero, y si no hay red el index.html cacheado.
  if (req.mode === 'navigate') {
    event.respondWith(
      fetch(req)
        .then((res) => {
          const copy = res.clone()
          caches.open(CACHE).then((c) => c.put('/', copy))
          return res
        })
        .catch(() => caches.match('/')),
    )
    return
  }

  // Assets (nombres con hash de Parcel): cache primero, se rellena sobre la marcha.
  event.respondWith(
    caches.match(req).then(
      (hit) =>
        hit ||
        fetch(req).then((res) => {
          if (res.ok) {
            const copy = res.clone()
            caches.open(CACHE).then((c) => c.put(req, copy))
          }
          return res
        }),
    ),
  )
})
