export function slackOAuthErrorDescription(code: string) {
  switch (code) {
    case 'missing_code':
      return 'Slack did not return an authorization code. Please try again.'
    case 'missing_scope':
      return 'Slack did not grant all required permissions. Please approve the requested permissions and try again.'
    case 'exchange_failed':
      return 'Slack authorization did not complete. Please try again.'
    case 'secret_save_failed':
      return 'Omnara could not save the Slack credentials. Check secret permissions and project limits, then try again.'
    case 'setup_save_failed':
      return 'Omnara could not save the Slack connection and app setup. Check project app limits, profile availability, and connection ownership, then try again.'
    default:
      return 'Slack app setup failed. Please try again.'
  }
}
