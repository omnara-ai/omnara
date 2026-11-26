import type { CodeContent } from '@/components/overview/CodeBlock'

function shellLines(parts: string[]) {
  return parts.join(' \\\n  ')
}

const chatMessage = 'Hi! What can you do?'

interface ChatCommands {
  cli: CodeContent
  sdk: CodeContent
  curl: CodeContent
}

export function inputCommands(input: {
  apiUrl: string
  orgId: string
  projectId: string
  agentId: string
}): ChatCommands {
  const message = chatMessage
  const cli = shellLines([
    `npx omnara agents input ${input.agentId}`,
    `--org ${input.orgId}`,
    `--project ${input.projectId}`,
    `--message "${message}"`,
  ])
  const sdk = `import { bearerToken, createOmnaraClient, sdk } from '@omnara/sdk'

await sdk.createAgentInput({
  client: createOmnaraClient({
    baseUrl: '${input.apiUrl}',
    auth: bearerToken(process.env.OMNARA_TOKEN),
  }),
  path: {
    orgID: '${input.orgId}',
    projectID: '${input.projectId}',
    agentID: '${input.agentId}',
  },
  body: {
    content_blocks: [{ type: 'text', text: '${message}' }],
  },
})`
  const curl = `curl "${input.apiUrl}/orgs/${input.orgId}/projects/${input.projectId}/agents/${input.agentId}/inputs" \\
  -H "Authorization: Bearer $OMNARA_TOKEN" \\
  -H "Content-Type: application/json" \\
  -d '{
    "content_blocks": [{ "type": "text", "text": "${message}" }]
  }'`
  return {
    cli: { copy: cli, segments: [{ text: cli }], language: 'shell' },
    sdk: { copy: sdk, segments: [{ text: sdk }], language: 'typescript' },
    curl: { copy: curl, segments: [{ text: curl }], language: 'shell' },
  }
}
