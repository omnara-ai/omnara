import { OAuthOutcomeDialog } from '@/components/oauth/OAuthOutcomeDialog'

import { slackOAuthErrorDescription } from './SlackOAuthOutcomeDialogState'

export function SlackOAuthOutcomeDialog() {
  return (
    <OAuthOutcomeDialog
      successParam="integration_oauth"
      errorParam="integration_oauth_error"
      successOutcome={() => ({
        title: 'Slack app connected',
        description:
          'Your Slack connection is ready. Manage the apps that use it from Apps in this project. Reconnecting keeps existing apps and their settings.',
      })}
      errorOutcome={(code) => ({
        title: 'Slack app setup failed',
        description: slackOAuthErrorDescription(code),
      })}
    />
  )
}
