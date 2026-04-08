// natsDebug: En desarrollo, intercepta los mensajes NATS y los loguea como JSON
// para que sean visibles en DevTools. En producción, no hace nada.

const isDev = import.meta.env.MODE === 'development'

// Log NATS messages to console as JSON (visible in DevTools Network tab via console)
export function logNatsMessage(direction: 'send' | 'recv', subject: string, data: Uint8Array) {
  if (!isDev) return

  const sc = new TextDecoder()
  const text = sc.decode(data)
  let json: any = text
  try {
    json = JSON.parse(text)
  } catch {}

  const style = direction === 'send'
    ? 'color: #22c55e; font-weight: bold'  // green
    : 'color: #3b82f6; font-weight: bold'  // blue

  console.log(
    `%c[nats ${direction}]%c ${subject}`,
    style,
    'color: inherit',
    json
  )
}

// Wrapper para conexiones NATS que loguea los mensajes
export function wrapNatsConnectionForDebug(nc: any) {
  if (!isDev) return nc

  const originalPublish = nc.publish.bind(nc)
  nc.publish = (subject: string, data: Uint8Array) => {
    logNatsMessage('send', subject, data)
    return originalPublish(subject, data)
  }

  return nc
}
