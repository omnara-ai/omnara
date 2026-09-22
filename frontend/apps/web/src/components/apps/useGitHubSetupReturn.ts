import { schemas } from '@omnara/sdk'
import { useEffect, useState } from 'react'
import { z } from 'zod'

function readReturn() {
  const params = new URLSearchParams(window.location.search)
  // Installation state carries a public credential selection hint, not OAuth authorization.
  const secret = schemas.zSecretId.safeParse(
    params.get('credentials_secret_ref') ?? params.get('state'),
  )
  const installation = z
    .string()
    .regex(/^[1-9][0-9]*$/)
    .safeParse(params.get('installation_id'))
  const reason = params.get('github_setup_error')
  const recoverManually = reason === 'conversion_failed' || reason === 'secret_save_failed'
  let error = ''
  if (reason === 'conversion_failed')
    error =
      'GitHub created the App, but Omnara could not retrieve its credentials. In the GitHub App’s settings, generate a private key and set a webhook secret, then connect that existing App below.'
  else if (reason === 'secret_save_failed')
    error =
      'GitHub created the App, but Omnara could not save its credentials. In the GitHub App’s settings, generate a private key and set a webhook secret, then connect that existing App below.'
  else if (reason === 'app_setup_changed')
    error = secret.success
      ? 'App setup changed while you were on GitHub. Your credential was saved. Review the current app setup before connecting.'
      : 'App setup changed while you were on GitHub. Check your saved credentials or use the existing GitHub App to continue.'
  else if (reason === 'registration_denied')
    error =
      'GitHub App registration was canceled. Continue to GitHub when ready, or use an existing App.'
  else if (reason === 'missing_code')
    error =
      'GitHub did not return a registration code. Check whether the App was created in GitHub; if so, use an existing App to connect it.'
  else if (reason)
    error =
      'GitHub setup did not finish. Check your GitHub Apps and saved credentials before continuing.'
  return {
    secretId: secret.success ? secret.data : '',
    installationId: installation.success ? installation.data : '',
    error,
    recoverManually,
  }
}

export function useGitHubSetupReturn() {
  const [outcome] = useState(readReturn)
  useEffect(() => {
    const url = new URL(window.location.href)
    const keys = [
      'github_setup',
      'github_setup_error',
      'credentials_secret_ref',
      'installation_id',
      'setup_action',
      'state',
    ]
    if (!keys.some((key) => url.searchParams.has(key))) return
    for (const key of keys) url.searchParams.delete(key)
    window.history.replaceState(window.history.state, '', `${url.pathname}${url.search}${url.hash}`)
  }, [])
  return outcome
}
