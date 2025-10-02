import { useEffect, useMemo, useRef, useState } from 'react'
import { Terminal } from 'xterm'
import { FitAddon } from 'xterm-addon-fit'
import clsx from 'clsx'

import 'xterm/css/xterm.css'

import {
  createRelayWebSocket,
  fetchRelaySessions,
  getRelayConfig,
  getSupabaseAccessToken,
} from '@/lib/relayClient'

const FRAME_HEADER_SIZE = 5
const FRAME_TYPE_OUTPUT = 0

type ConnectionStatus =
  | 'idle'
  | 'checking-session'
  | 'session-missing'
  | 'connecting'
  | 'connected'
  | 'ended'
  | 'disconnected'
  | 'error'

interface TerminalLiveTerminalProps {
  instanceId: string
  className?: string
}

export function TerminalLiveTerminal({ instanceId, className }: TerminalLiveTerminalProps) {
  const containerRef = useRef<HTMLDivElement | null>(null)
  const terminalRef = useRef<Terminal | null>(null)
  const fitAddonRef = useRef<FitAddon | null>(null)
  const socketRef = useRef<WebSocket | null>(null)
  const disposablesRef = useRef<Array<{ dispose: () => void }>>([])
  const resizeObserverRef = useRef<ResizeObserver | null>(null)
  const textDecoderRef = useRef<TextDecoder | null>(null)
  const bufferRef = useRef<Uint8Array>(new Uint8Array())
  const pendingResizeRef = useRef<{ cols: number; rows: number } | null>(null)
  const suppressResizeRef = useRef(0)
  const accessTokenRef = useRef<string | null>(null)
  const reconnectTimeoutRef = useRef<NodeJS.Timeout | null>(null)
  const historyLoadedRef = useRef(false)
  const historyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null)
  const suppressFullClearsRef = useRef(false)

  const [status, setStatus] = useState<ConnectionStatus>('idle')
  const statusRef = useRef<ConnectionStatus>('idle')
  const [error, setError] = useState<string | null>(null)

  const relayConfig = useMemo(() => getRelayConfig(), [])

  useEffect(() => {
    return () => {
      disposeSocket()
      disposeTerminal()
      resetDecoder()
    }
  }, [])

  useEffect(() => {
    disposeSocket()
    disposeTerminal()
    resetDecoder()

    if (!containerRef.current) {
      return
    }

    const abort = new AbortController()
    let cancelled = false

    createTerminal()

    async function start(): Promise<void> {
      try {
        updateStatus('checking-session')
        setError(null)

        const accessToken = await getSupabaseAccessToken()
        if (!accessToken) {
          updateStatus('error')
          setError('Sign in to view the live terminal stream.')
          return
        }
        if (cancelled) return

        accessTokenRef.current = accessToken

        const sessions = await fetchRelaySessions(accessToken, abort.signal)
        if (cancelled) return

        if (!sessions.some(session => session.id === instanceId)) {
          updateStatus('session-missing')
          setError('Session is not currently registered with the relay.')
          return
        }

        connectSocket(accessToken)
      } catch (err) {
        if (abort.signal.aborted || cancelled) {
          return
        }
        const message = err instanceof Error ? err.message : 'Failed to reach relay'
        updateStatus('error')
        setError(message)
      }
    }

    start()

    return () => {
      cancelled = true
      abort.abort()
      disposeSocket()
      disposeTerminal()
      resetDecoder()
    }
  }, [instanceId])

  function updateStatus(next: ConnectionStatus): void {
    statusRef.current = next
    setStatus(next)
  }

  function disposeTerminal(): void {
    if (resizeObserverRef.current) {
      try {
        resizeObserverRef.current.disconnect()
      } catch (_) {}
      resizeObserverRef.current = null
    }

    for (const disposable of disposablesRef.current) {
      try {
        disposable.dispose()
      } catch (_) {}
    }
    disposablesRef.current = []

    if (fitAddonRef.current && typeof fitAddonRef.current.dispose === 'function') {
      try {
        fitAddonRef.current.dispose()
      } catch (_) {}
      fitAddonRef.current = null
    }

    if (terminalRef.current) {
      try {
        terminalRef.current.dispose()
      } catch (_) {}
      terminalRef.current = null
    }
  }

  function disposeSocket(): void {
    if (reconnectTimeoutRef.current) {
      clearTimeout(reconnectTimeoutRef.current)
      reconnectTimeoutRef.current = null
    }
    if (socketRef.current) {
      try {
        socketRef.current.close()
      } catch (_) {}
      socketRef.current = null
    }
    historyLoadedRef.current = false
    if (historyTimerRef.current) {
      clearTimeout(historyTimerRef.current)
      historyTimerRef.current = null
    }
    suppressFullClearsRef.current = false
  }

  function resetDecoder(): void {
    textDecoderRef.current = new TextDecoder('utf-8')
    bufferRef.current = new Uint8Array()
  }

  function createTerminal(): void {
    disposeTerminal()

    if (!containerRef.current) {
      return
    }

    const term = new Terminal({
      fontFamily: '"Berkeley Mono", "Fira Code", monospace',
      convertEol: false,
      allowProposedApi: true,
      theme: {
        background: '#000000',
        foreground: '#dddddd',
      },
    })

    const fitAddon = new FitAddon()
    term.loadAddon(fitAddon)

    const clearHandler = term.parser.registerCsiHandler({ final: 'J' }, params => {
      const code = params.length === 0 ? 0 : params[0]
      if (code === 2 && suppressFullClearsRef.current && !historyLoadedRef.current) {
        return true
      }
      return false
    })

    const dataListener = term.onData(data => sendInput(data))
    const resizeListener = term.onResize(({ cols, rows }) => {
      pendingResizeRef.current = { cols, rows }
      if (suppressResizeRef.current > 0) {
        return
      }
      sendResizeRequest(cols, rows)
    })

    term.open(containerRef.current)
    fitAddonRef.current = fitAddon
    terminalRef.current = term

    runFit()
    const size = getCurrentSize()
    if (size) {
      pendingResizeRef.current = size
      sendResizeRequest(size.cols, size.rows)
    }

    term.focus()

    const resizeObserver = new ResizeObserver(() => {
      runFit()
      const measured = getCurrentSize()
      if (measured) {
        pendingResizeRef.current = measured
        sendResizeRequest(measured.cols, measured.rows)
      }
    })

    resizeObserver.observe(containerRef.current)

    disposablesRef.current = [clearHandler, dataListener, resizeListener]
    resizeObserverRef.current = resizeObserver
  }

  function runFit(): void {
    const fitAddon = fitAddonRef.current
    const term = terminalRef.current
    if (!fitAddon || !term || !term.element) {
      return
    }

    const element = term.element as HTMLElement | null
    if (!element || element.offsetWidth === 0 || element.offsetHeight === 0) {
      return
    }

    suppressResizeRef.current += 1
    try {
      fitAddon.fit()
    } catch (_) {
      // Ignore sporadic fit failures while layout settles.
    } finally {
      suppressResizeRef.current = Math.max(0, suppressResizeRef.current - 1)
    }
  }

  function getCurrentSize(): { cols: number; rows: number } | null {
    const term = terminalRef.current
    if (!term) return null

    const cols = Number(term.cols)
    const rows = Number(term.rows)
    if (!Number.isFinite(cols) || !Number.isFinite(rows)) {
      return null
    }
    return { cols, rows }
  }

  function sendInput(data: string): void {
    const socket = socketRef.current
    if (!socket || socket.readyState !== WebSocket.OPEN) return

    const payload: Record<string, unknown> = {
      type: 'input',
      data,
    }

    const size = getCurrentSize()
    if (size) {
      payload.cols = size.cols
      payload.rows = size.rows
    }

    socket.send(JSON.stringify(payload))
  }

  function sendResizeRequest(cols?: number | null, rows?: number | null): void {
    const socket = socketRef.current
    if (!socket || socket.readyState !== WebSocket.OPEN) return

    if (!Number.isFinite(cols ?? NaN) || !Number.isFinite(rows ?? NaN)) {
      return
    }

    const safeCols = Math.max(1, Math.trunc(cols!))
    const safeRows = Math.max(1, Math.trunc(rows!))
    socket.send(
      JSON.stringify({
        type: 'resize_request',
        cols: safeCols,
        rows: safeRows,
      }),
    )
  }

  function connectSocket(accessToken: string): void {
    disposeSocket()
    resetDecoder()
    updateStatus('connecting')

    historyLoadedRef.current = false
    if (historyTimerRef.current) {
      clearTimeout(historyTimerRef.current)
    }
    historyTimerRef.current = setTimeout(() => {
      historyLoadedRef.current = true
      historyTimerRef.current = null
    }, 2000)
    suppressFullClearsRef.current = false

    const socket = createRelayWebSocket(instanceId, accessToken, relayConfig)
    socketRef.current = socket

    socket.onopen = () => {
      updateStatus('connected')
      socket.send(
        JSON.stringify({
          type: 'join_session',
          session_id: instanceId,
        }),
      )

      const size = pendingResizeRef.current ?? getCurrentSize()
      if (size) {
        sendResizeRequest(size.cols, size.rows)
      }
    }

    socket.onclose = event => {
      if (statusRef.current === 'connected') {
        updateStatus('disconnected')
      } else if (statusRef.current !== 'ended') {
        updateStatus('disconnected')
      }

      const decoder = textDecoderRef.current
      const term = terminalRef.current
      if (decoder && term) {
        const flush = decoder.decode()
        if (flush) {
          term.write(flush)
        }
      }

      if (event.code === 1008) {
        setError('Relay rejected authentication credentials.')
      } else if (statusRef.current === 'connected' || statusRef.current === 'disconnected') {
        // Unexpected disconnect, attempt to reconnect after 5 seconds
        setStatus('disconnected')
        setError('Connection lost. Reconnecting...')
        reconnectTimeoutRef.current = setTimeout(() => {
          if (statusRef.current === 'disconnected') {
            const token = accessTokenRef.current
            if (token) {
              // This will trigger onclose again if it fails, creating a retry loop
              connectSocket(token)
            }
          }
        }, 5000)
      } else {
        setError(null)
      }

      socketRef.current = null
      historyLoadedRef.current = false
      if (historyTimerRef.current) {
        clearTimeout(historyTimerRef.current)
        historyTimerRef.current = null
      }
      suppressFullClearsRef.current = false
    }

    socket.onerror = () => {
      updateStatus('error')
      setError('WebSocket error while streaming session')
    }

    socket.onmessage = event => {
      const { data } = event

      if (typeof data === 'string') {
        handleJsonMessage(data)
        return
      }

      if (data instanceof ArrayBuffer) {
        appendBuffer(new Uint8Array(data))
        processFrames()
        return
      }

      if (data instanceof Blob) {
        data
          .arrayBuffer()
          .then(buffer => {
            appendBuffer(new Uint8Array(buffer))
            processFrames()
          })
          .catch(() => {
            updateStatus('error')
            setError('Failed to read binary relay frame')
          })
        return
      }

      try {
        const text = String(data)
        appendBuffer(new TextEncoder().encode(text))
        processFrames()
      } catch (_) {
        // ignore unknown payload formats
      }
    }
  }

  function handleJsonMessage(message: string): void {
    let payload: any
    try {
      payload = JSON.parse(message)
    } catch (_) {
      return
    }

    switch (payload?.type) {
      case 'resize':
        applyResize(payload?.cols, payload?.rows)
        break
      case 'agent_metadata': {
        const metadata = payload?.metadata ?? {}
        const historyPolicy = metadata.history_policy
        const agentName = metadata.agent
        const appName = metadata.app
        suppressFullClearsRef.current =
          historyPolicy === 'strip_esc_j' || agentName === 'codex' || appName === 'codex'

        if (suppressFullClearsRef.current) {
          historyLoadedRef.current = false
          if (!historyTimerRef.current) {
            historyTimerRef.current = setTimeout(() => {
              historyLoadedRef.current = true
              historyTimerRef.current = null
            }, 2000)
          }
        } else {
          historyLoadedRef.current = true
          if (historyTimerRef.current) {
            clearTimeout(historyTimerRef.current)
            historyTimerRef.current = null
          }
        }
        break
      }
      case 'history_complete':
        historyLoadedRef.current = true
        if (historyTimerRef.current) {
          clearTimeout(historyTimerRef.current)
          historyTimerRef.current = null
        }
        break
      case 'error':
        setError(payload?.message ?? 'Relay reported an error')
        updateStatus('error')
        break
      case 'session_ended':
        setError(null)
        updateStatus('ended')
        break
      default:
        break
    }
  }

  function appendBuffer(chunk: Uint8Array): void {
    if (!chunk.length) return
    const current = bufferRef.current
    const combined = new Uint8Array(current.length + chunk.length)
    combined.set(current, 0)
    combined.set(chunk, current.length)
    bufferRef.current = combined
  }

  function shiftBuffer(length: number): void {
    bufferRef.current = bufferRef.current.slice(length)
  }

  function processFrames(): void {
    const term = terminalRef.current
    if (!term) {
      bufferRef.current = new Uint8Array()
      return
    }

    while (bufferRef.current.length >= FRAME_HEADER_SIZE) {
      const view = new DataView(
        bufferRef.current.buffer,
        bufferRef.current.byteOffset,
        bufferRef.current.byteLength,
      )

      const frameType = view.getUint8(0)
      const frameLength = view.getUint32(1)
      const total = FRAME_HEADER_SIZE + frameLength

      if (bufferRef.current.length < total) {
        return
      }

      const payload = bufferRef.current.slice(FRAME_HEADER_SIZE, total)
      shiftBuffer(total)

      if (frameType === FRAME_TYPE_OUTPUT) {
        const decoder = textDecoderRef.current
        if (!decoder) continue
        const text = decoder.decode(payload, { stream: true })
        if (text) {
          term.write(text)
        }
      }
    }
  }

  function applyResize(cols?: number | null, rows?: number | null): void {
    const term = terminalRef.current
    if (!term) return

    const normalizedCols = Number(cols)
    const normalizedRows = Number(rows)

    if (!Number.isFinite(normalizedCols) || !Number.isFinite(normalizedRows)) {
      return
    }

    const safeCols = Math.max(1, Math.trunc(normalizedCols))
    const safeRows = Math.max(1, Math.trunc(normalizedRows))

    pendingResizeRef.current = { cols: safeCols, rows: safeRows }

    if (term.cols === safeCols && term.rows === safeRows) {
      return
    }

    suppressResizeRef.current += 1
    try {
      term.resize(safeCols, safeRows)
    } finally {
      suppressResizeRef.current = Math.max(0, suppressResizeRef.current - 1)
    }
  }

  return (
    <div className={clsx('relative h-full pb-2', className)}>
      <div
        ref={containerRef}
        className="h-full bg-black rounded-xl border border-border-divider overflow-hidden"
      />
      {error && (
        <div className="absolute top-3 right-3 text-xs text-red-100 bg-red-500/90 px-3 py-2 rounded-md shadow-lg">
          {error}
        </div>
      )}
    </div>
  )
}
