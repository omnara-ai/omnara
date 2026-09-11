import * as z from 'zod'

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

type AuthRequestBody = Record<string, string | undefined>

const zAuthConnectorsResponse = z.object({
  connectors: z.array(
    z.object({
      slug: z.string(),
      kind: z.string(),
      display_name: z.string(),
      login_url: z.string(),
    }),
  ),
})

const zWebConfigResponse = z.object({
  billing_url: z.string().optional(),
  api_url: z.string().optional(),
})

const zDeviceAuthPendingResponse = z.object({
  client_name: z.string(),
  token_name: z.string(),
  created_at: z.string(),
  expires_at: z.string(),
})

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

async function authJSON(path: string, body?: AuthRequestBody): Promise<void> {
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
  const data = zAuthConnectorsResponse.parse(await response.json())
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
  const data = zWebConfigResponse.parse(await response.json())
  return { billingURL: data.billing_url, apiURL: data.api_url }
}

export async function pendingDeviceAuth(userCode: string): Promise<DeviceAuthPending> {
  const params = new URLSearchParams({ user_code: userCode })
  const response = await fetch(`/api/auth/device/pending?${params}`, { credentials: 'include' })
  if (!response.ok) throw await ApiError.fromResponse(response)
  const data = zDeviceAuthPendingResponse.parse(await response.json())
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

export interface OAuthAuthorizePending {
  clientId: string
  clientName: string
  clientUri?: string
  redirectUri: string
  redirectHost: string
  loopback: boolean
  resource: string
}

const zOAuthAuthorizePendingResponse = z.object({
  client_id: z.string(),
  client_name: z.string(),
  client_uri: z.string(),
  redirect_uri: z.string(),
  redirect_host: z.string(),
  loopback: z.boolean(),
  resource: z.string(),
})

const zOAuthAuthorizeRedirectResponse = z.object({ redirect_url: z.string().min(1) })

const zOAuthAuthorizeErrorResponse = z.object({
  error: z.string().min(1),
  error_description: z.string().optional(),
  redirect_url: z.string().optional(),
})

export class OAuthAuthorizeError extends Error {
  readonly code: string
  readonly redirectUrl?: string

  constructor(code: string, description: string | undefined, redirectUrl: string | undefined) {
    super(description ?? code)
    this.name = 'OAuthAuthorizeError'
    this.code = code
    this.redirectUrl = redirectUrl
  }
}

async function oauthAuthorizeFailure(response: Response): Promise<Error> {
  const text = await response.text()
  try {
    const parsed = zOAuthAuthorizeErrorResponse.parse(JSON.parse(text))
    return new OAuthAuthorizeError(parsed.error, parsed.error_description, parsed.redirect_url)
  } catch {
    return await ApiError.fromResponse(
      new Response(text, { status: response.status, headers: response.headers }),
    )
  }
}

export async function pendingOAuthAuthorization(query: string): Promise<OAuthAuthorizePending> {
  const response = await fetch(`/api/auth/oauth/authorize/pending${query}`, {
    credentials: 'include',
  })
  if (!response.ok) throw await oauthAuthorizeFailure(response)
  const data = zOAuthAuthorizePendingResponse.parse(await response.json())
  return {
    clientId: data.client_id,
    clientName: data.client_name,
    clientUri: data.client_uri || undefined,
    redirectUri: data.redirect_uri,
    redirectHost: data.redirect_host,
    loopback: data.loopback,
    resource: data.resource,
  }
}

async function oauthAuthorizeDecision(path: string, query: string): Promise<string> {
  const headers = new Headers({ 'Content-Type': 'application/json' })
  const token = csrfToken()
  if (token) headers.set('X-Omnara-Csrf', token)
  const response = await fetch(path, {
    method: 'POST',
    credentials: 'include',
    headers,
    body: JSON.stringify({ query }),
  })
  if (!response.ok) throw await oauthAuthorizeFailure(response)
  return zOAuthAuthorizeRedirectResponse.parse(await response.json()).redirect_url
}

export function approveOAuthAuthorization(query: string): Promise<string> {
  return oauthAuthorizeDecision('/api/auth/oauth/authorize/approve', query)
}

export function denyOAuthAuthorization(query: string): Promise<string> {
  return oauthAuthorizeDecision('/api/auth/oauth/authorize/deny', query)
}
