import type { OAuthRefreshSecretMaterial, OAuthTokenSetSecretMaterial } from '@omnara/sdk'

export const oauthKeys = [
  { value: 'access_token', label: 'Access token' },
  { value: 'refresh_token', label: 'Refresh token' },
  { value: 'id_token', label: 'ID token' },
  { value: 'client_secret', label: 'Client secret' },
  { value: 'client_id', label: 'Client ID' },
  { value: 'mcp_url', label: 'MCP URL' },
  { value: 'resource', label: 'Resource' },
  { value: 'token_endpoint', label: 'Token endpoint' },
  { value: 'scopes', label: 'Scopes' },
  { value: 'token_type', label: 'Token type' },
  { value: 'access_token_expires_in_seconds', label: 'Access token lifetime (seconds)' },
] as const

export type OAuthKey = (typeof oauthKeys)[number]['value']

export interface OAuthEntry {
  id: string
  key: OAuthKey
  value: string
}

export function isOAuthKey(value: string): value is OAuthKey {
  return oauthKeys.some((key) => key.value === value)
}

export function newOAuthEntry(key: OAuthKey): OAuthEntry {
  return { id: crypto.randomUUID(), key, value: '' }
}

export function newOAuthTokenSetEntries(): OAuthEntry[] {
  return [newOAuthEntry('access_token'), newOAuthEntry('refresh_token')]
}

export function oauthTokenSetMaterial(
  entries: OAuthEntry[],
): OAuthTokenSetSecretMaterial | undefined {
  const values = Object.fromEntries(
    entries.filter((entry) => entry.value !== '').map((entry) => [entry.key, entry.value]),
  ) as Partial<Record<OAuthKey, string>>
  if (!values.access_token) return undefined

  let accessTokenExpiresInSeconds: number | undefined
  if (values.access_token_expires_in_seconds !== undefined) {
    const rawLifetime = values.access_token_expires_in_seconds.trim()
    if (!/^[1-9]\d*$/.test(rawLifetime)) return undefined
    accessTokenExpiresInSeconds = Number(rawLifetime)
    if (
      !Number.isSafeInteger(accessTokenExpiresInSeconds) ||
      accessTokenExpiresInSeconds > 2147483647
    ) {
      return undefined
    }
  }

  const refreshValues = {
    refreshToken: values.refresh_token,
    tokenEndpoint: values.token_endpoint,
    clientID: values.client_id,
    clientSecret: values.client_secret,
    resource: values.resource,
  }
  const hasRefreshMaterial = Object.values(refreshValues).some((value) => value !== undefined)
  let refresh: OAuthRefreshSecretMaterial | undefined
  if (hasRefreshMaterial) {
    if (
      !refreshValues.refreshToken ||
      !refreshValues.tokenEndpoint ||
      !refreshValues.clientID ||
      !refreshValues.resource
    ) {
      return undefined
    }
    refresh = {
      refresh_token: refreshValues.refreshToken,
      token_endpoint: refreshValues.tokenEndpoint,
      client_id: refreshValues.clientID,
      ...(refreshValues.clientSecret === undefined
        ? {}
        : { client_secret: refreshValues.clientSecret }),
      resource: refreshValues.resource,
    }
  }

  return {
    kind: 'oauth_token_set',
    access_token: values.access_token,
    ...(accessTokenExpiresInSeconds === undefined
      ? {}
      : { access_token_expires_in_seconds: accessTokenExpiresInSeconds }),
    ...(refresh === undefined ? {} : { refresh }),
    ...(values.id_token === undefined ? {} : { id_token: values.id_token }),
    ...(values.mcp_url === undefined ? {} : { mcp_url: values.mcp_url }),
    ...(values.scopes === undefined ? {} : { scopes: values.scopes }),
    ...(values.token_type === undefined ? {} : { token_type: values.token_type }),
  }
}
