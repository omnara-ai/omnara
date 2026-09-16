import { zIntegrationOAuthFlowId } from '@omnara/sdk/zod'
import { z } from 'zod'

export const integrationSetupSearch = z.object({
  integration_oauth: z.enum(['success', 'select_repository']).optional().catch(undefined),
  integration_oauth_flow_id: zIntegrationOAuthFlowId.optional().catch(undefined),
  integration_oauth_error: z.string().max(512).optional().catch(undefined),
})

export type IntegrationSetupSearch = z.infer<typeof integrationSetupSearch>

export function integrationAuthorizationUrl(value: string) {
  try {
    const url = new URL(value)
    return url.protocol === 'https:' && !url.username && !url.password ? url.href : undefined
  } catch {
    return undefined
  }
}

export function integrationSetupError(code: string) {
  switch (code) {
    case 'access_denied':
      return 'Authorization was canceled or denied. Try connecting again and approve the requested access.'
    case 'already_connected':
      return 'This account is already connected. Review the existing connection before trying again.'
    case 'missing_scope':
      return 'The app did not receive all required permissions. Try connecting again and approve the requested access.'
    default:
      return 'The connection could not be completed. Start again from an available app.'
  }
}
