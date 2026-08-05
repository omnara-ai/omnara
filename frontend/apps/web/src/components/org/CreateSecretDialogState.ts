import type { Secret } from '@omnara/sdk'

import { newOAuthTokenSetEntries, type OAuthEntry } from '@/lib/oauthEntries'

export type SecretKind = 'generic' | 'oauth_token_set' | 'mcp_oauth'

export const secretKinds = [
  { value: 'generic', label: 'Generic' },
  { value: 'oauth_token_set', label: 'OAuth token set' },
  { value: 'mcp_oauth', label: 'MCP OAuth secret' },
] satisfies { value: SecretKind; label: string }[]

export function isSecretKind(value: string): value is SecretKind {
  return secretKinds.some((kind) => kind.value === value)
}

export type SecretFormSecret =
  | GenericSecretFormSecret
  | OAuthTokenSetSecretFormSecret
  | McpOAuthSecretFormSecret

export interface GenericSecretFormSecret {
  kind: 'generic'
  value: string
}

export interface OAuthTokenSetSecretFormSecret {
  kind: 'oauth_token_set'
  entries: OAuthEntry[]
}

export interface McpOAuthSecretFormSecret {
  kind: 'mcp_oauth'
  serverUrl: string
  clientId: string
  clientSecret?: string
}

export interface SecretDialogState {
  name: string
  secret: SecretFormSecret
  projectGrantIds: string[]
  submitting: boolean
  error: string
  /**
   * Set once creation succeeds so retrying failed project grants never
   * creates a duplicate secret. The MCP OAuth flow never sets it — grants
   * for that flow are applied after the redirect.
   */
  createdSecret: Secret | null
}

export type SecretDialogAction =
  | { type: 'set-name'; name: string }
  | { type: 'set-kind'; kind: SecretKind }
  | { type: 'set-generic-value'; value: string }
  | { type: 'patch-mcp-oauth'; patch: Partial<McpOAuthSecretFormSecret> }
  | { type: 'set-oauth-entries'; entries: OAuthEntry[] }
  | { type: 'set-project-grant-ids'; ids: string[] }
  | { type: 'submit-start' }
  | { type: 'submit-fail'; message: string }
  | { type: 'submit-settled' }
  | { type: 'created'; secret: Secret }
  | { type: 'grant-failures'; failedProjectIds: string[]; message: string }
  | { type: 'reset' }
  | { type: 'closed' }

export function newSecretDialogState(): SecretDialogState {
  return {
    name: '',
    secret: newSecretFormSecret('generic'),
    projectGrantIds: [],
    submitting: false,
    error: '',
    createdSecret: null,
  }
}

export function newSecretFormSecret(kind: SecretKind): SecretFormSecret {
  if (kind === 'oauth_token_set') {
    return {
      kind,
      entries: newOAuthTokenSetEntries(),
    }
  }
  if (kind === 'mcp_oauth') {
    return {
      kind,
      serverUrl: '',
      clientId: '',
    }
  }
  return {
    kind: 'generic',
    value: '',
  }
}

export function secretDialogReducer(
  state: SecretDialogState,
  action: SecretDialogAction,
): SecretDialogState {
  switch (action.type) {
    case 'set-name':
      return { ...state, name: action.name }
    case 'set-kind':
      return { ...state, secret: newSecretFormSecret(action.kind) }
    case 'set-generic-value':
      return state.secret.kind === 'generic'
        ? { ...state, secret: { ...state.secret, value: action.value } }
        : state
    case 'patch-mcp-oauth':
      return state.secret.kind === 'mcp_oauth'
        ? { ...state, secret: { ...state.secret, ...action.patch } }
        : state
    case 'set-oauth-entries':
      return state.secret.kind === 'oauth_token_set'
        ? { ...state, secret: { ...state.secret, entries: action.entries } }
        : state
    case 'set-project-grant-ids':
      return { ...state, projectGrantIds: action.ids }
    case 'submit-start':
      return { ...state, submitting: true, error: '' }
    case 'submit-fail':
      return { ...state, submitting: false, error: action.message }
    case 'submit-settled':
      return state.submitting ? { ...state, submitting: false } : state
    case 'created':
      return { ...state, createdSecret: action.secret }
    case 'grant-failures':
      return {
        ...state,
        projectGrantIds: action.failedProjectIds,
        error: action.message,
      }
    case 'reset':
      return newSecretDialogState()
    case 'closed':
      return { ...state, createdSecret: null, error: '' }
  }
}
