export function slackOAuthErrorDescription(code: string) {
  switch (code) {
    case 'integration_setup_changed':
      return 'This integration’s setup changed while authorization was open. Refresh the integration and start setup again.'
    case 'integration_deleted':
      return 'This integration was deleted. Choose or create an integration before starting setup again.'
    case 'flow_consumed':
      return 'This authorization has already been used. Refresh the integration to see its current setup.'
    case 'missing_code':
      return 'Slack did not return an authorization code. Please try again.'
    case 'missing_scope':
      return 'Slack did not grant all required permissions. Please approve the requested permissions and try again.'
    case 'exchange_failed':
      return 'Slack authorization did not complete. Please try again.'
    case 'secret_save_failed':
      return 'Omnara could not save the Slack credentials. Check secret permissions and project limits, then try again.'
    case 'setup_save_failed':
      return 'Omnara could not finish saving Slack setup. Check project limits and integration access, then try again.'
    default:
      return 'Slack app setup failed. Please try again.'
  }
}
