import type {
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
      const response = await operations.startMcpOAuth({
        owner,
        name: state.name,
        mcp_url: state.secret.serverUrl.trim(),
        return_to: returnTo,
        ...(clientId !== '' ? { client_id: clientId } : {}),
        ...(clientSecret !== '' ? { client_secret: clientSecret } : {}),
      })
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
            ? ({
                kind: 'aws_credentials',
                access_key_id: state.secret.accessKeyId.trim(),
                secret_access_key: state.secret.secretAccessKey.trim(),
                ...(state.secret.sessionToken.trim() !== ''
                  ? { session_token: state.secret.sessionToken.trim() }
                  : {}),
                ...(state.secret.roleArn.trim() !== ''
                  ? { role_arn: state.secret.roleArn.trim() }
                  : {}),
                ...(state.secret.externalId.trim() !== ''
                  ? { external_id: state.secret.externalId.trim() }
                  : {}),
              } as const)
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
