export const DOCS_BASE_URL = 'https://docs.omnara.com'

export function docsUrl(path: string) {
  return `${DOCS_BASE_URL}/${path}`
}

export const guides = {
  agents: 'agents/overview',
  agentProfiles: 'agents/configuration',
  apiTokens: 'api/authentication',
  machinePools: 'machines/pools',
  machines: 'machines/connect',
  members: 'organization/members',
  modelProviders: 'organization/model-providers',
  secrets: 'organization/secrets',
  skills: 'tools/skills',
} as const

export type Guide = (typeof guides)[keyof typeof guides]
