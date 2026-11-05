// Dev proxy for Parcel (replaces .proxyrc.json). Same routes, but a backend that goes down or a
// websocket that resets no longer kills the dev server: the proxy answers 502 and keeps running.
const { createProxyMiddleware } = require('http-proxy-middleware')

// A dropped connection to the backend surfaces as an uncaught 'error' on a socket and would take
// the whole Parcel process down. Network errors are logged and ignored; anything else still crashes.
const NETWORK_ERRORS = new Set(['ECONNRESET', 'ECONNREFUSED', 'EPIPE', 'ETIMEDOUT', 'ECONNABORTED'])
if (!process.__lucodeProxyGuard) {
  process.__lucodeProxyGuard = true
  process.on('uncaughtException', err => {
    // WS_ERR_*: a websocket client (e.g. a phone behind an https proxy) sent a malformed frame or close code.
    if (err && (NETWORK_ERRORS.has(err.code) || String(err.code).startsWith('WS_ERR_'))) {
      console.warn(`[proxy] ignored ${err.code}: ${err.message}`)
      return
    }
    console.error(err)
    process.exit(1)
  })
}

// http-proxy-middleware v2: onError gets a ServerResponse for http requests and a raw socket for ws.
function onError(err, req, res) {
  console.warn(`[proxy] ${req.method} ${req.url} -> ${err.code || err.message}`)
  if (res && typeof res.writeHead === 'function') {
    if (!res.headersSent) res.writeHead(502, { 'Content-Type': 'text/plain' })
    res.end('Backend unavailable, the proxy is still alive. Retry in a moment.')
  } else if (res && typeof res.destroy === 'function') {
    res.destroy()
  }
}

const rewriteApi = { '^/api/': '/' }

// Order matters: the most specific /api prefixes first, the catch-all /api last.
const routes = [
  ['/api/auth', { target: 'http://localhost:8092', pathRewrite: rewriteApi }],
  ['/api/apikeys', { target: 'http://localhost:8092', pathRewrite: rewriteApi }],
  ['/api/versioning', { target: 'http://localhost:8091' }],
  ['/api/turns', { target: 'http://localhost:8091', pathRewrite: rewriteApi }],
  ['/api/models', { target: 'http://localhost:8091', pathRewrite: rewriteApi }],
  ['/api', { target: 'http://localhost:8090' }],
]

// The hub's websockets (/ws: events, /ws/stt: voice). Parcel's own HMR websocket accepts EVERY
// upgrade on this port and answers first, so a plain `ws: true` proxy never gets the connection
// (the browser sees an open socket that the hub never hears). So the upgrades are dispatched here:
// /ws goes to the hub, everything else (HMR) to Parcel's original listeners.
const wsProxy = createProxyMiddleware('/ws', { target: 'http://localhost:8090', onError })
let upgradeHooked = false
function hookUpgrade(server) {
  if (upgradeHooked || !server) return
  upgradeHooked = true
  const parcelListeners = server.listeners('upgrade')
  server.removeAllListeners('upgrade')
  server.on('upgrade', (req, socket, head) => {
    if (req.url === '/ws' || req.url.startsWith('/ws/') || req.url.startsWith('/ws?')) {
      socket.on('error', () => {}) // a reset socket must not crash the dev server
      wsProxy.upgrade(req, socket, head)
    } else {
      for (const l of parcelListeners) l.call(server, req, socket, head)
    }
  })
}

module.exports = function (app) {
  // Needs the http server, which is only reachable from a request: the page load comes first.
  app.use((req, _res, next) => { hookUpgrade(req.socket && req.socket.server); next() })
  for (const [path, opts] of routes) {
    app.use(createProxyMiddleware(path, { onError, ...opts }))
  }
}
