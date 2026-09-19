import type { ProfileAppProvider } from '@omnara/sdk'

export const appCatalog = [
  {
    provider: 'slack',
    name: 'Slack',
    description:
      'Start an agent from a mention, continue in its thread, and answer questions and approvals in Slack.',
  },
  {
    provider: 'discord',
    name: 'Discord',
    description:
      'Let people choose an agent profile through your Discord bot and continue the conversation in a server thread.',
  },
  {
    provider: 'github',
    name: 'GitHub',
    description:
      'Start a reviewer when a PR opens or someone mentions your bot. Read changes, leave inline comments, and follow replies.',
  },
] satisfies { provider: ProfileAppProvider; name: string; description: string }[]

export function appDefinitionLabel(definition?: string) {
  return (
    appCatalog.find((app) => `omnara.${app.provider}` === definition)?.name ?? definition ?? 'App'
  )
}

export function appProvider(definition?: string) {
  return appCatalog.find((app) => `omnara.${app.provider}` === definition)?.provider
}
