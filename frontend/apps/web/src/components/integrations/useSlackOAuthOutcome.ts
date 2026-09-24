import { useCallback, useEffect, useState } from 'react'

import { slackOAuthErrorDescription } from './slackOAuthErrors'

export type SlackOAuthOutcome = { kind: 'success' } | { kind: 'error'; description: string }

const params = ['integration_oauth', 'integration_oauth_error', 'integration_id']

function readOutcome(integrationId: string): SlackOAuthOutcome | null {
  const search = new URLSearchParams(window.location.search)
  const errorCode = search.get('integration_oauth_error')
  if (errorCode) return { kind: 'error', description: slackOAuthErrorDescription(errorCode) }
  return search.get('integration_oauth') === 'success' &&
    search.get('integration_id') === integrationId
    ? { kind: 'success' }
    : null
}

export function useSlackOAuthOutcome(integrationId: string) {
  const [outcome, setOutcome] = useState(() => readOutcome(integrationId))
  const clear = useCallback(() => {
    setOutcome(null)
  }, [])
  useEffect(() => {
    const url = new URL(window.location.href)
    if (!params.some((param) => url.searchParams.has(param))) return
    for (const param of params) url.searchParams.delete(param)
    window.history.replaceState(window.history.state, '', `${url.pathname}${url.search}${url.hash}`)
  }, [])
  return { outcome, clear }
}
