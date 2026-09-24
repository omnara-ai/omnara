import type { IntegrationType } from '@omnara/sdk'

import discordLogo from '@/assets/integrations/discord.svg'
import githubLogo from '@/assets/integrations/github-black.svg'
import githubDarkLogo from '@/assets/integrations/github-white.svg'
import slackLogo from '@/assets/integrations/slack-black.svg'
import slackDarkLogo from '@/assets/integrations/slack-white.svg'

export const integrationCatalog = [
  {
    integrationType: 'slack_thread',
    logo: slackLogo,
    darkLogo: slackDarkLogo,
    name: 'Slack threads',
    description:
      'Start an agent from a mention, continue in its thread, and answer questions and approvals in Slack.',
  },
  {
    integrationType: 'discord_thread',
    logo: discordLogo,
    name: 'Discord threads',
    description:
      'Let people choose an agent profile through your Discord bot and continue the conversation in a server thread.',
  },
  {
    integrationType: 'github_pr',
    logo: githubLogo,
    darkLogo: githubDarkLogo,
    name: 'GitHub PR review',
    description:
      'Start a reviewer when a PR opens or someone mentions your bot. Read changes, leave inline comments, and follow replies.',
  },
] satisfies {
  integrationType: IntegrationType
  name: string
  description: string
  logo: string
  darkLogo?: string
}[]

export function integrationTypeLabel(integrationType?: IntegrationType) {
  return (
    integrationCatalog.find((integration) => integration.integrationType === integrationType)
      ?.name ??
    integrationType ??
    'Integration'
  )
}
