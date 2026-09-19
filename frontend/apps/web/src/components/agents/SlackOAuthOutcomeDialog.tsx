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
          'Slack authorization is saved. New setups launch this profile on a DM or mention. Reconnecting preserves existing app settings, including disabled launchers.',
      })}
      errorOutcome={(code) => ({
        title: 'Slack app setup failed',
        description: slackOAuthErrorDescription(code),
      })}
    />
  )
}
