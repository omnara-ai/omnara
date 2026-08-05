import { useGrantSecretToProject } from '@omnara/react'
import { useEffect, useRef } from 'react'

import { OAuthOutcomeDialog } from '@/components/oauth/OAuthOutcomeDialog'
import {
  clearPendingMcpSecretGrants,
  takePendingMcpSecretGrants,
} from '@/lib/pending-mcp-secret-grants'
import { useActiveOrg } from '@/lib/use-active-org'

export function McpOAuthOutcomeDialog() {
  const { activeOrg } = useActiveOrg()
  const grantSecret = useGrantSecretToProject(activeOrg.id)
  const applied = useRef(false)

  useEffect(() => {
    if (applied.current) return
    const search = new URLSearchParams(window.location.search)
    if (search.get('mcp_oauth_error')) {
      clearPendingMcpSecretGrants(activeOrg.id)
      return
    }
    if (search.get('mcp_oauth') !== 'success') return
    const secretId = search.get('secret_id')
    // Leave the pending grants in place when the redirect is unusable, so a
    // retried OAuth flow can still apply them.
    if (!secretId) return
    applied.current = true
    const projectIds = takePendingMcpSecretGrants(activeOrg.id)
    if (projectIds.length === 0) return

    void Promise.allSettled(
      projectIds.map((projectID) => grantSecret.mutateAsync({ secretID: secretId, projectID })),
    ).then((results) => {
      const failed = results.filter((result) => result.status === 'rejected').length
      if (failed > 0) {
        window.alert(
          `The MCP secret was connected, but ${failed} project grant${failed === 1 ? '' : 's'} could not be added. You can retry from the secret's actions menu.`,
        )
      }
    })
  }, [activeOrg.id, grantSecret])

  return (
    <OAuthOutcomeDialog
      successParam="mcp_oauth"
      errorParam="mcp_oauth_error"
      extraParams={['secret_id']}
      successOutcome={() => ({
        title: 'MCP secret connected',
        description: 'The MCP server authorized access and the secret was saved.',
      })}
      errorOutcome={(code) => ({
        title: 'MCP OAuth failed',
        description: mcpOAuthErrorDescription(code),
      })}
    />
  )
}

function mcpOAuthErrorDescription(code: string) {
  switch (code) {
    case 'missing_code':
      return 'The MCP server did not return an authorization code. Please try again.'
    case 'exchange_failed':
      return 'The MCP server did not complete the authorization. Please try again.'
    case 'secret_save_failed':
      return 'Omnara could not finish saving the secret. Please try again.'
    case 'access_denied':
      return 'Authorization was denied. Please try again and approve the requested access.'
    default:
      return 'MCP OAuth setup failed. Please try again.'
  }
}
