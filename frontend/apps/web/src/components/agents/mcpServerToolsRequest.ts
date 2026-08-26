import type { McpServerToolsRequest } from '@omnara/sdk'

import type { BasicMcpServer } from '@/components/agents/useAgentBuilderForm'

export function mcpServerToolsRequest(
  server: Pick<BasicMcpServer, 'url' | 'authType' | 'secretId' | 'service' | 'region'>,
): McpServerToolsRequest | null {
  const url = server.url.trim()
  if (!/^https?:\/\/\S+$/i.test(url)) return null
  switch (server.authType) {
    case 'none':
      return { url, auth: { type: 'none' } }
    case 'bearer':
    case 'oauth': {
      const secretId = server.secretId.trim()
      if (secretId === '') return null
      return { url, auth: { type: server.authType, secret_id: secretId } }
    }
    case 'sigv4': {
      const secretId = server.secretId.trim()
      const service = server.service.trim()
      const region = server.region.trim()
      if (secretId === '' || service === '' || region === '') return null
      return { url, auth: { type: 'sigv4', secret_id: secretId, service, region } }
    }
  }
}
