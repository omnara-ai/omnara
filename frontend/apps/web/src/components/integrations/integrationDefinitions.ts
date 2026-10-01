import type { IntegrationKind } from '@omnara/sdk'

// Official logos: https://slack.com/media-kit, https://discord.com/branding,
// https://brand.github.com/foundations/logo
import discordLogo from '@/assets/integrations/discord.svg'
import githubLogo from '@/assets/integrations/github-black.svg'
import githubDarkLogo from '@/assets/integrations/github-white.svg'
import slackLogo from '@/assets/integrations/slack-black.svg'
import slackDarkLogo from '@/assets/integrations/slack-white.svg'

export const integrationCatalog = [
  {
    integrationKind: 'slack_thread',
    logo: slackLogo,
    darkLogo: slackDarkLogo,
    name: 'Slack bot',
    description:
      'Start an agent from a mention, continue in its thread, and answer questions and approvals in Slack.',
  },
  {
    integrationKind: 'discord_thread',
    logo: discordLogo,
    name: 'Discord bot',
    description:
      'Let people choose an agent profile through your Discord bot and continue the conversation in a server thread.',
  },
  {
    integrationKind: 'github_pr',
    logo: githubLogo,
    darkLogo: githubDarkLogo,
    name: 'GitHub PR review',
    description:
      'Start a reviewer when a PR opens or someone mentions your bot. Read changes, leave inline comments, and follow replies.',
  },
] satisfies {
  integrationKind: IntegrationKind
  name: string
  description: string
  logo: string
  darkLogo?: string
}[]

export function integrationKindLabel(integrationKind?: IntegrationKind) {
  return (
    integrationCatalog.find((integration) => integration.integrationKind === integrationKind)
      ?.name ??
    integrationKind ??
    'Integration'
  )
}
