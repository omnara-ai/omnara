import { OAuthOutcomeDialog } from '@/components/oauth/OAuthOutcomeDialog'

import { slackOAuthErrorDescription } from './SlackOAuthOutcomeDialogState'

export function SlackOAuthOutcomeDialog() {
  return (
    <OAuthOutcomeDialog
      successParam="integration_oauth"
      extraParams={['app_id']}
      errorParam="integration_oauth_error"
      successOutcome={() => ({
        title: 'Slack app connected',
        description:
          'Your app is connected. Choose its launch profiles and settings on the app page. Reconnecting keeps this app and its settings.',
      })}
      errorOutcome={(code) => ({
        title: 'Slack app setup failed',
        description: slackOAuthErrorDescription(code),
      })}
    />
  )
}
