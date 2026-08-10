interface SecretSubtitleInput {
  kind: string
  metadata: Record<string, string>
}

export function secretSubtitle(secret: SecretSubtitleInput) {
  if (secret.kind === 'oauth_token_set') {
    const mcpUrl = secret.metadata.mcp_url
    if (mcpUrl !== undefined && mcpUrl !== '') {
      return `MCP OAuth Token Pair · ${mcpUrl}`
    }
    return 'OAuth Token Pair'
  }
  if (secret.kind === 'generic') {
    return 'Generic'
  }
  if (secret.kind === 'aws_credentials') {
    return 'AWS Credentials'
  }
  return secret.kind
}
