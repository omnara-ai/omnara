import type { AwsCredentialsSecretMaterial, Secret } from '@omnara/sdk'

import { newOAuthTokenSetEntries, type OAuthEntry } from '@/lib/oauthEntries'

export type SecretKind = 'generic' | 'oauth_token_set' | 'mcp_oauth' | 'aws_credentials'

export const secretKinds = [
  { value: 'generic', label: 'Generic' },
  { value: 'oauth_token_set', label: 'OAuth token set' },
  { value: 'mcp_oauth', label: 'MCP OAuth secret' },
  { value: 'aws_credentials', label: 'AWS credentials' },
] satisfies { value: SecretKind; label: string }[]

export function isSecretKind(value: string): value is SecretKind {
  return secretKinds.some((kind) => kind.value === value)
}

export type SecretFormSecret =
  | GenericSecretFormSecret
  | OAuthTokenSetSecretFormSecret
  | McpOAuthSecretFormSecret
  | AWSCredentialsSecretFormSecret

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

export interface AWSCredentialsSecretFormSecret {
  kind: 'aws_credentials'
  accessKeyId: string
  secretAccessKey: string
  sessionToken: string
  roleArn: string
  externalId: string
}

export function newAWSCredentialsSecret(): AWSCredentialsSecretFormSecret {
  return {
    kind: 'aws_credentials',
    accessKeyId: '',
    secretAccessKey: '',
    sessionToken: '',
    roleArn: '',
    externalId: '',
  }
}

export function awsCredentialsMaterial(
  secret: AWSCredentialsSecretFormSecret,
): AwsCredentialsSecretMaterial | undefined {
  const accessKeyId = secret.accessKeyId.trim()
  const secretAccessKey = secret.secretAccessKey.trim()
  const sessionToken = secret.sessionToken.trim()
  const roleArn = secret.roleArn.trim()
  const externalId = secret.externalId.trim()
  if (accessKeyId === '' || secretAccessKey === '') return undefined
  if (externalId !== '' && roleArn === '') return undefined

  const material: AwsCredentialsSecretMaterial = {
    kind: 'aws_credentials',
    access_key_id: accessKeyId,
    secret_access_key: secretAccessKey,
  }
  if (sessionToken !== '') material.session_token = sessionToken
  if (roleArn !== '') material.role_arn = roleArn
  if (externalId !== '') material.external_id = externalId
  return material
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
  | { type: 'patch-aws-credentials'; patch: Partial<AWSCredentialsSecretFormSecret> }
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
  if (kind === 'aws_credentials') {
    return newAWSCredentialsSecret()
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
    case 'patch-aws-credentials':
      return state.secret.kind === 'aws_credentials'
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
