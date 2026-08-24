import { useStartSecretMcpOAuth } from '@omnara/react'

import type { BasicMcpServer } from '@/components/agents/useAgentBuilderForm'

export function isMcpOAuthLoginUrl(value: string) {
  try {
    return new URL(value).protocol === 'https:'
  } catch {
    return false
  }
}

export function defaultMcpSecretName(server: BasicMcpServer) {
  const serverName = server.name.trim()
  if (serverName !== '') return `${serverName}-oauth`
  try {
    return `${new URL(server.url.trim()).hostname}-oauth`
  } catch {
    return 'mcp-oauth'
  }
}

export function useMcpOAuthLogin({
  orgId,
  projectId,
  server,
  onBeforeRedirect,
}: {
  orgId: string
  projectId: string
  server: BasicMcpServer
  onBeforeRedirect: () => void
}) {
  const startMcpOAuth = useStartSecretMcpOAuth(orgId)
  return {
    pending: startMcpOAuth.isPending,
    async start(input: { name: string; clientId?: string; clientSecret?: string }) {
      const clientId = input.clientId?.trim() ?? ''
      const clientSecret = input.clientSecret?.trim() ?? ''
      const response = await startMcpOAuth.mutateAsync({
        owner: { kind: 'project', project_id: projectId },
        name: input.name.trim(),
        mcp_url: server.url.trim(),
        return_to: window.location.pathname + window.location.search + window.location.hash,
        ...(clientId !== '' ? { client_id: clientId } : {}),
        ...(clientSecret !== '' ? { client_secret: clientSecret } : {}),
      })
      onBeforeRedirect()
      window.location.assign(response.authorization_url)
    },
  }
}
