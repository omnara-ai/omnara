import type { AuthStrategy } from './auth'
import { ApiError } from './errors'

const SAFE_METHODS = new Set(['GET', 'HEAD', 'OPTIONS'])

export interface DeviceAuthPending {
  clientName: string
  tokenName: string
  createdAt: string
  expiresAt: string
}

export interface AuthConnector {
  slug: string
  kind: string
  displayName: string
  loginURL: string
}

export interface WebConfig {
  billingURL?: string
  apiURL?: string
}

function cookieValue(name: string): string | undefined {
  const prefix = `${name}=`
  const raw = document.cookie
    .split(';')
    .map((cookie) => cookie.trim())
    .find((cookie) => cookie.startsWith(prefix))
    ?.slice(prefix.length)
  if (!raw) return undefined
  try {
    return decodeURIComponent(raw)
  } catch {
    return undefined
  }
}

function csrfToken(): string | undefined {
  return cookieValue('__Host-omnara_csrf') ?? cookieValue('omnara_csrf')
}

export function getLastUsedAuthMethod(): string | undefined {
  return cookieValue('omnara_last_login_method')
}

export function cookieCsrf(): AuthStrategy {
  return {
    authenticate(request) {
      if (!SAFE_METHODS.has(request.method.toUpperCase())) {
        const token = csrfToken()
        if (token) request.headers.set('X-Omnara-Csrf', token)
      }
    },
  }
}

async function authJSON(path: string, body?: unknown): Promise<void> {
  const headers: Record<string, string> = {}
  if (body !== undefined) {
    headers['Content-Type'] = 'application/json'
  }
  const token = csrfToken()
  if (token) headers['X-Omnara-Csrf'] = token
  const response = await fetch(path, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  })
  if (!response.ok) throw await ApiError.fromResponse(response)
}

export async function listAuthConnectors(): Promise<AuthConnector[]> {
  const response = await fetch('/api/auth/connectors', { credentials: 'include' })
  if (!response.ok) throw await ApiError.fromResponse(response)
  const data = (await response.json()) as {
    connectors: {
      slug: string
      kind: string
      display_name: string
      login_url: string
    }[]
  }
  return data.connectors.map((connector) => ({
    slug: connector.slug,
    kind: connector.kind,
    displayName: connector.display_name,
    loginURL: connector.login_url,
  }))
}

export async function fetchWebConfig(): Promise<WebConfig> {
  const response = await fetch('/api/web-config', { credentials: 'include' })
  if (!response.ok) throw await ApiError.fromResponse(response)
  const data = (await response.json()) as { billing_url?: string; api_url?: string }
  return { billingURL: data.billing_url, apiURL: data.api_url }
}

export async function pendingDeviceAuth(userCode: string): Promise<DeviceAuthPending> {
  const params = new URLSearchParams({ user_code: userCode })
  const response = await fetch(`/api/auth/device/pending?${params}`, { credentials: 'include' })
  if (!response.ok) throw await ApiError.fromResponse(response)
  const data = (await response.json()) as {
    client_name: string
    token_name: string
    created_at: string
    expires_at: string
  }
  return {
    clientName: data.client_name,
    tokenName: data.token_name,
    createdAt: data.created_at,
    expiresAt: data.expires_at,
  }
}

export async function approveDeviceAuth(userCode: string): Promise<void> {
  await authJSON('/api/auth/device/approve', { user_code: userCode })
}

export async function denyDeviceAuth(userCode: string): Promise<void> {
  await authJSON('/api/auth/device/deny', { user_code: userCode })
}

export async function sessionLogin(email: string, password: string): Promise<void> {
  await authJSON('/api/auth/login', { email, password })
}

export async function sessionLogout(): Promise<void> {
  await authJSON('/api/auth/logout')
}

export async function requestSignup(email: string, returnTo?: string): Promise<void> {
  await authJSON('/api/auth/signup', { email, return_to: returnTo })
}

export async function completeEmailVerification(
  token: string,
  password: string,
  displayName: string,
): Promise<void> {
  await authJSON('/api/auth/email/verify', {
    token,
    password,
    display_name: displayName.trim() || undefined,
  })
}

export async function requestPasswordReset(email: string): Promise<void> {
  await authJSON('/api/auth/password/reset/request', { email })
}

export async function completePasswordReset(token: string, password: string): Promise<void> {
  await authJSON('/api/auth/password/reset', { token, password })
}
