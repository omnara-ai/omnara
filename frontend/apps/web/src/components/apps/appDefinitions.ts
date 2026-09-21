import type { AppType } from '@omnara/sdk'

import discordLogo from '@/assets/apps/discord.svg'
import githubLogo from '@/assets/apps/github-black.svg'
import githubDarkLogo from '@/assets/apps/github-white.svg'
import slackLogo from '@/assets/apps/slack-black.svg'
import slackDarkLogo from '@/assets/apps/slack-white.svg'

export const appCatalog = [
  {
    appType: 'slack_thread',
    logo: slackLogo,
    darkLogo: slackDarkLogo,
    name: 'Slack threads',
    description:
      'Start an agent from a mention, continue in its thread, and answer questions and approvals in Slack.',
  },
  {
    appType: 'discord_thread',
    logo: discordLogo,
    name: 'Discord threads',
    description:
      'Let people choose an agent profile through your Discord bot and continue the conversation in a server thread.',
  },
  {
    appType: 'github_pr',
    logo: githubLogo,
    darkLogo: githubDarkLogo,
    name: 'GitHub PR review',
    description:
      'Start a reviewer when a PR opens or someone mentions your bot. Read changes, leave inline comments, and follow replies.',
  },
] satisfies {
  appType: AppType
  name: string
  description: string
  logo: string
  darkLogo?: string
}[]

export function appTypeLabel(appType?: AppType) {
  return appCatalog.find((app) => app.appType === appType)?.name ?? appType ?? 'App'
}
