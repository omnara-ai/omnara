import type {
  AwsCredentialsSecretMaterial,
  CreateSecretRequest,
  McpoAuthStartRequest,
  McpoAuthStartResponse,
  Secret,
  SecretOwnerInput,
} from '@omnara/sdk'

import type { SecretDialogState } from '@/components/org/CreateSecretDialogState'
import { collectGrantFailures } from '@/lib/grant-failures'
import { oauthTokenSetMaterial } from '@/lib/oauthEntries'
import { errorMessage } from '@/lib/submit-status'

interface SecretSubmissionOperations {
  createSecret: (request: CreateSecretRequest) => Promise<Secret>
  grantSecret: (input: { secretID: string; projectID: string }) => Promise<unknown>
  startMcpOAuth: (request: McpoAuthStartRequest) => Promise<McpoAuthStartResponse>
  savePendingMcpGrants: (projectIds: string[]) => void
}

export type SecretSubmissionResult =
  | { kind: 'complete'; secret: Secret }
  | {
      kind: 'grant-failures'
      secret: Secret
      failedProjectIds: string[]
      message: string
    }
  | { kind: 'redirect'; authorizationUrl: string }
  | { kind: 'failed'; secret: Secret | null; message: string }

function awsCredentialsMaterial(secret: {
  accessKeyId: string
  secretAccessKey: string
  sessionToken: string
  roleArn: string
  externalId: string
}): AwsCredentialsSecretMaterial {
  const material: AwsCredentialsSecretMaterial = {
    kind: 'aws_credentials',
    access_key_id: secret.accessKeyId.trim(),
    secret_access_key: secret.secretAccessKey.trim(),
  }
  const sessionToken = secret.sessionToken.trim()
  const roleArn = secret.roleArn.trim()
  const externalId = secret.externalId.trim()
  if (sessionToken !== '') material.session_token = sessionToken
  if (roleArn !== '') material.role_arn = roleArn
  if (externalId !== '') material.external_id = externalId
  return material
}

export async function submitSecretTransaction({
  state,
  owner,
  returnTo,
  operations,
}: {
  state: SecretDialogState
  owner: SecretOwnerInput
  returnTo: string
  operations: SecretSubmissionOperations
}): Promise<SecretSubmissionResult> {
  let secret = state.createdSecret

  try {
    if (state.secret.kind === 'mcp_oauth' && !secret) {
      const clientId = state.secret.clientId.trim()
      const clientSecret = state.secret.clientSecret?.trim() ?? ''
      const request: McpoAuthStartRequest = {
        owner,
        name: state.name,
        mcp_url: state.secret.serverUrl.trim(),
        return_to: returnTo,
      }
      if (clientId !== '') request.client_id = clientId
      if (clientSecret !== '') request.client_secret = clientSecret
      const response = await operations.startMcpOAuth(request)
      operations.savePendingMcpGrants(state.projectGrantIds)
      return { kind: 'redirect', authorizationUrl: response.authorization_url }
    }

    if (!secret) {
      if (state.secret.kind === 'mcp_oauth') {
        return { kind: 'failed', secret: null, message: 'Could not resume secret creation' }
      }
      const material =
        state.secret.kind === 'generic'
          ? ({ kind: 'generic', value: state.secret.value } as const)
          : state.secret.kind === 'aws_credentials'
            ? awsCredentialsMaterial(state.secret)
            : oauthTokenSetMaterial(state.secret.entries)
      if (material === undefined) {
        return { kind: 'failed', secret: null, message: 'OAuth token material is incomplete' }
      }
      secret = await operations.createSecret({ owner, name: state.name, material })
    }

    const submittedSecret = secret
    const grantResults = await Promise.allSettled(
      state.projectGrantIds.map((projectID) =>
        operations.grantSecret({ secretID: submittedSecret.id, projectID }),
      ),
    )
    const failures = collectGrantFailures(state.projectGrantIds, grantResults)
    if (failures) {
      return {
        kind: 'grant-failures',
        secret: submittedSecret,
        failedProjectIds: failures.failedProjectIds,
        message: `The secret was created, but ${failures.message}`,
      }
    }
    return { kind: 'complete', secret: submittedSecret }
  } catch (error) {
    return {
      kind: 'failed',
      secret,
      message: errorMessage(error, 'Could not create secret'),
    }
  }
}
