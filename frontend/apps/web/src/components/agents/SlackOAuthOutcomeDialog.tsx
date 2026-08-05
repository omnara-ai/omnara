import { OAuthOutcomeDialog } from '@/components/oauth/OAuthOutcomeDialog'

export function SlackOAuthOutcomeDialog() {
  return (
    <OAuthOutcomeDialog
      successParam="integration_oauth"
      errorParam="integration_oauth_error"
      successOutcome={() => ({
        title: 'Slack app connected',
        description:
          'Your Slack app was created and authorized. Try sending it a DM, or @mention it in a channel.',
      })}
      errorOutcome={(code) => ({
        title: 'Slack app setup failed',
        description: slackOAuthErrorDescription(code),
      })}
    />
  )
}

function slackOAuthErrorDescription(code: string) {
  switch (code) {
    case 'already_connected':
      return 'That Slack app is already connected. Create a new Slack app or disconnect the existing one, then try again.'
    case 'missing_code':
      return 'Slack did not return an authorization code. Please try again.'
    case 'missing_scope':
      return 'Slack did not grant all required permissions. Please approve the requested permissions and try again.'
    case 'exchange_failed':
      return 'Slack authorization did not complete. Please try again.'
    case 'secret_save_failed':
    case 'install_save_failed':
      return 'Omnara could not finish saving the Slack app connection. Please try again.'
    default:
      return 'Slack app setup failed. Please try again.'
  }
}
