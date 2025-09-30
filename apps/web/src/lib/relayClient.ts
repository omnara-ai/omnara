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
  host: string
  port: number
  secure: boolean
  httpScheme: 'http' | 'https'
  wsScheme: 'ws' | 'wss'
  baseHttpUrl: string
  baseWsUrl: string
}

const RELAY_HOST = (import.meta.env.VITE_RELAY_HOST as string | undefined) ?? '127.0.0.1'
const RELAY_PORT_RAW = (import.meta.env.VITE_RELAY_PORT as string | undefined) ?? '8787'
const RELAY_SECURE = ((import.meta.env.VITE_RELAY_SECURE as string | undefined) ?? 'false').toLowerCase() === 'true'

export const API_KEY_SUBPROTOCOL_PREFIX = 'omnara-key.'
export const SUPABASE_SUBPROTOCOL_PREFIX = 'omnara-supabase.'

export function getRelayConfig(): RelayConfig {
  const port = Number.parseInt(RELAY_PORT_RAW, 10)
  const httpScheme: RelayConfig['httpScheme'] = RELAY_SECURE ? 'https' : 'http'
  const wsScheme: RelayConfig['wsScheme'] = RELAY_SECURE ? 'wss' : 'ws'

  const baseHost = Number.isFinite(port) && port > 0 ? `${RELAY_HOST}:${port}` : RELAY_HOST

  return {
    host: RELAY_HOST,
    port: Number.isFinite(port) ? port : 0,
    secure: RELAY_SECURE,
    httpScheme,
    wsScheme,
    baseHttpUrl: `${httpScheme}://${baseHost}`,
    baseWsUrl: `${wsScheme}://${baseHost}`,
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
  const response = await fetch(`${config.baseHttpUrl}/api/v1/sessions`, {
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
    `${runtimeConfig.baseWsUrl}/terminal`,
    `${SUPABASE_SUBPROTOCOL_PREFIX}${accessToken}`,
  )
  socket.binaryType = 'arraybuffer'
  return socket
}
