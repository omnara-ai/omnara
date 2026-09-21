import type { AppType } from '@omnara/sdk'

export const appCatalog = [
  {
    appType: 'slack_thread',
    name: 'Slack threads',
    description:
      'Start an agent from a mention, continue in its thread, and answer questions and approvals in Slack.',
  },
  {
    appType: 'discord_thread',
    name: 'Discord threads',
    description:
      'Let people choose an agent profile through your Discord bot and continue the conversation in a server thread.',
  },
  {
    appType: 'github_pr',
    name: 'GitHub PR review',
    description:
      'Start a reviewer when a PR opens or someone mentions your bot. Read changes, leave inline comments, and follow replies.',
  },
] satisfies { appType: AppType; name: string; description: string }[]

export function appTypeLabel(appType?: AppType) {
  return appCatalog.find((app) => app.appType === appType)?.name ?? appType ?? 'App'
}
