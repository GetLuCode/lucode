// Dev proxy for Parcel (replaces .proxyrc.json). Same routes, but a backend that goes down or a
// websocket that resets no longer kills the dev server: the proxy answers 502 and keeps running.
const { createProxyMiddleware } = require('http-proxy-middleware')

// A dropped connection to the backend surfaces as an uncaught 'error' on a socket and would take
// the whole Parcel process down. Network errors are logged and ignored; anything else still crashes.
const NETWORK_ERRORS = new Set(['ECONNRESET', 'ECONNREFUSED', 'EPIPE', 'ETIMEDOUT', 'ECONNABORTED'])
if (!process.__lucodeProxyGuard) {
  process.__lucodeProxyGuard = true
  process.on('uncaughtException', err => {
    if (err && NETWORK_ERRORS.has(err.code)) {
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
  ['/ws', { target: 'http://localhost:8090', ws: true }],
]

module.exports = function (app) {
  for (const [path, opts] of routes) {
    app.use(createProxyMiddleware(path, { onError, ...opts }))
  }
}
