import type { Secret, SecretMaterial } from '@omnara/sdk'

import { isOAuthKey, oauthTokenSetMaterialFromValues } from '@/lib/oauthEntries'

export function requiredSecretKeys(kind: Secret['kind']): string[] | undefined {
  switch (kind) {
    case 'generic':
      return ['value']
    case 'aws_credentials':
      return ['access_key_id', 'secret_access_key']
    case 'oauth_token_set':
      return ['access_token']
    default:
      return undefined
  }
}

function normalizeSecretValues(
  kind: Secret['kind'],
  values: Record<string, string>,
): Record<string, string> {
  return kind === 'aws_credentials'
    ? Object.fromEntries(Object.entries(values).map(([key, value]) => [key, value.trim()]))
    : values
}

export function prepareSecretReplacement(
  secret: Secret,
  updates: Record<string, string>,
  lifetime = '',
): { material?: SecretMaterial; error?: never } | { material?: never; error: string } {
  updates = normalizeSecretValues(secret.kind, updates)
  if (Object.keys(updates).length === 0) return {}
  const required = requiredSecretKeys(secret.kind)
  if (!required) return { error: 'These credentials cannot be replaced here.' }
  if (secret.payload_keys.some((key) => updates[key] === undefined))
    return {
      error:
        'Changing or clearing a field requires re-entering all stored values you want to keep.',
    }
  const present = (key: string) => Boolean(updates[key])
  if (required.some((key) => !present(key)))
    return { error: 'Required credential fields cannot be empty.' }
  if (secret.kind === 'aws_credentials' && present('external_id') && !present('role_arn'))
    return { error: 'An external ID requires a role ARN.' }
  const refresh = ['refresh_token', 'token_endpoint', 'client_id', 'resource']
  if (
    secret.kind === 'oauth_token_set' &&
    [...refresh, 'client_secret'].some(present) &&
    !refresh.every(present)
  ) {
    return {
      error:
        'Refresh credentials require a refresh token, token endpoint, client ID, and resource.',
    }
  }
  if (secret.kind === 'generic')
    return { material: { kind: 'generic', value: updates.value ?? '' } }
  if (secret.kind === 'aws_credentials') {
    const material: SecretMaterial = {
      kind: 'aws_credentials',
      access_key_id: updates.access_key_id ?? '',
      secret_access_key: updates.secret_access_key ?? '',
    }
    if (updates.session_token) material.session_token = updates.session_token
    if (updates.role_arn) material.role_arn = updates.role_arn
    if (updates.external_id) material.external_id = updates.external_id
    return { material }
  }
  if (secret.kind === 'oauth_token_set') {
    const material = oauthTokenSetMaterialFromValues(
      Object.fromEntries(
        Object.entries({ ...updates, access_token_expires_in_seconds: lifetime }).filter(
          ([key, value]) => isOAuthKey(key) && value !== '',
        ),
      ),
    )
    return material
      ? { material }
      : {
          error:
            'Enter a whole-number token lifetime between 1 and 2147483647 seconds, or leave it blank.',
        }
  }
  return { error: 'These credentials cannot be replaced here.' }
}
