import { useEffect, useRef } from 'react'
import { Terminal } from '@xterm/xterm'
import { FitAddon } from '@xterm/addon-fit'
import { WebLinksAddon } from '@xterm/addon-web-links'
import '@xterm/xterm/css/xterm.css'
import * as api from '../../api'
import { getTermWSUrl } from '../../hooks/useWSConnection'

const THEME = {
  background: '#0a0a0d', foreground: '#e8e4dd', cursor: '#00d4aa', cursorAccent: '#0a0a0d',
  selectionBackground: 'rgba(0, 212, 170, 0.28)',
  black: '#1c1c21', red: '#ff5f5f', green: '#58c98c', yellow: '#e0b050', blue: '#6ea8fe', magenta: '#c792ea', cyan: '#63c9e0', white: '#e8e4dd',
  brightBlack: '#6b6771', brightRed: '#ff7a7a', brightGreen: '#7be0a8', brightYellow: '#f0c870', brightBlue: '#8bbcff', brightMagenta: '#d8a8f5', brightCyan: '#85dcf0', brightWhite: '#ffffff',
}

// The terminal of one command. The hub relays the bytes of the runner's pseudo-terminal; what was
// printed before this page attached is sent first, so closing the tab loses nothing.
export function TerminalPane({ projectId, commandId }: { projectId: string; commandId: string }) {
  const host = useRef<HTMLDivElement>(null)

  useEffect(() => {
    const el = host.current
    if (!el) return
    const term = new Terminal({
      fontFamily: "'JetBrains Mono', 'Fira Code', Consolas, monospace",
      fontSize: 13, lineHeight: 1.15, cursorBlink: true, scrollback: 5000, theme: THEME, allowProposedApi: true,
    })
    const fit = new FitAddon()
    term.loadAddon(fit)
    // Links in the output open in a new tab; only http(s), and the new page gets no opener.
    term.loadAddon(new WebLinksAddon((_e, uri) => {
      if (/^https?:\/\//i.test(uri)) window.open(uri, '_blank', 'noopener,noreferrer')
    }))
    term.open(el)

    let ws: WebSocket | null = null
    let closed = false
    let retry: ReturnType<typeof setTimeout> | undefined
    const enc = new TextEncoder()
    const sendSize = () => {
      if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }))
    }
    const refit = () => {
      if (!el.offsetWidth || !el.offsetHeight) return
      try { fit.fit() } catch { /* not laid out yet */ }
    }

    async function connect() {
      if (closed) return
      try {
        const { ticket } = await api.commandTerminalTicket(projectId, commandId)
        if (closed) return
        const socket = new WebSocket(await getTermWSUrl(ticket))
        ws = socket
        socket.binaryType = 'arraybuffer'
        socket.onopen = () => { term.reset(); refit(); sendSize() } // the replay that follows repaints it
        socket.onmessage = (e: MessageEvent) => {
          if (e.data instanceof ArrayBuffer) { term.write(new Uint8Array(e.data)); return }
          try {
            const m = JSON.parse(e.data as string) as { type: string; message?: string }
            if (m.type === 'reset') term.reset()
            else if (m.type === 'error') term.write(`\r\n\x1b[31m${m.message ?? 'terminal unavailable'}\x1b[0m\r\n`)
          } catch { /* not ours */ }
        }
        socket.onclose = () => {
          if (ws === socket) ws = null
          if (!closed) retry = setTimeout(connect, 2000)
        }
      } catch {
        if (!closed) retry = setTimeout(connect, 3000)
      }
    }

    const data = term.onData(s => { if (ws?.readyState === WebSocket.OPEN) ws.send(enc.encode(s)) })
    const bin = term.onBinary(s => { if (ws?.readyState === WebSocket.OPEN) ws.send(Uint8Array.from(s, c => c.charCodeAt(0))) })
    const size = term.onResize(sendSize)
    const observer = new ResizeObserver(refit)
    observer.observe(el)
    refit()
    void connect()

    return () => {
      closed = true
      clearTimeout(retry)
      observer.disconnect()
      data.dispose(); bin.dispose(); size.dispose()
      ws?.close()
      term.dispose()
    }
  }, [projectId, commandId])

  return <div className="cmds-term" ref={host} />
}
