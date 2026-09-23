import { useCallback, useEffect, useState } from 'react'

import { slackOAuthErrorDescription } from './slackOAuthErrors'

export type SlackOAuthOutcome = { kind: 'success' } | { kind: 'error'; description: string }

const params = ['app_oauth', 'app_oauth_error', 'app_id']

function readOutcome(appId: string): SlackOAuthOutcome | null {
  const search = new URLSearchParams(window.location.search)
  const errorCode = search.get('app_oauth_error')
  if (errorCode) return { kind: 'error', description: slackOAuthErrorDescription(errorCode) }
  return search.get('app_oauth') === 'success' && search.get('app_id') === appId
    ? { kind: 'success' }
    : null
}

export function useSlackOAuthOutcome(appId: string) {
  const [outcome, setOutcome] = useState(() => readOutcome(appId))
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
