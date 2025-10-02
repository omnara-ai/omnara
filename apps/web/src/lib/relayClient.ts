import { supabase } from '@/lib/supabase'
import { reportError } from '@/integrations/sentry'

export interface RelaySessionSummary {
  id: string
  active: boolean
  started_at?: string
  ended_at?: string | null
  cols?: number | null
  rows?: number | null
}

export interface RelayConfig {
  httpUrl: string
  wsUrl: string
}

const RELAY_URL = import.meta.env.VITE_RELAY_URL as string | undefined

export const SUPABASE_SUBPROTOCOL_PREFIX = 'omnara-supabase.'

export function getRelayConfig(): RelayConfig {
  if (!RELAY_URL) {
    throw new Error(
      'Missing VITE_RELAY_URL. Set it to the relay websocket endpoint, e.g. ws://localhost:8787/terminal',
    )
  }

  let relay: URL
  try {
    relay = new URL(RELAY_URL)
  } catch (error) {
    throw new Error(
      `Invalid VITE_RELAY_URL "${RELAY_URL}". Expected a websocket URL such as ws://localhost:8787/terminal`,
    )
  }

  if (relay.protocol !== 'ws:' && relay.protocol !== 'wss:') {
    throw new Error(
      `Unsupported VITE_RELAY_URL protocol "${relay.protocol}". Use ws:// or wss://`,
    )
  }

  return {
    httpUrl: relay.protocol === 'wss:' ? `https://${relay.host}` : `http://${relay.host}`,
    wsUrl: relay.toString().replace(/\/$/, ''),
  }
}

export async function getSupabaseAccessToken(): Promise<string | null> {
  const { data } = await supabase.auth.getSession()
  return data.session?.access_token ?? null
}

export async function fetchRelaySessions(
  accessToken: string,
  signal?: AbortSignal,
): Promise<RelaySessionSummary[]> {
  const config = getRelayConfig()
  const response = await fetch(`${config.httpUrl}/api/v1/sessions`, {
    method: 'GET',
    headers: {
      Authorization: `Bearer ${accessToken}`,
    },
    signal,
  })

  if (!response.ok) {
    const error = new Error(`Relay request failed with status ${response.status}`)
    reportError(error, {
      context: 'relay-session-fetch',
      extras: { status: response.status },
      tags: { feature: 'terminal' },
    })
    throw error
  }

  const payload = await response.json()
  const sessions = Array.isArray(payload?.sessions) ? payload.sessions : []
  return sessions as RelaySessionSummary[]
}

export function createRelayWebSocket(
  sessionId: string,
  accessToken: string,
  config?: RelayConfig,
): WebSocket {
  const runtimeConfig = config ?? getRelayConfig()
  const socket = new WebSocket(
    runtimeConfig.wsUrl,
    `${SUPABASE_SUBPROTOCOL_PREFIX}${accessToken}`,
  )
  socket.binaryType = 'arraybuffer'
  return socket
}
