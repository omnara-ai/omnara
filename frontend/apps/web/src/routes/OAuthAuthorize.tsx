import { ApiError } from '@omnara/sdk'
import {
  approveOAuthAuthorization,
  denyOAuthAuthorization,
  OAuthAuthorizeError,
  type OAuthAuthorizePending,
  pendingOAuthAuthorization,
} from '@omnara/sdk/browser'
import { useEffect, useState } from 'react'

import { AuthLayout } from '@/components/auth/AuthLayout'
import { Check, X } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'

type OAuthAuthorizeState =
  | { kind: 'loading' }
  | { kind: 'ready'; request: OAuthAuthorizePending }
  | { kind: 'submitting'; request: OAuthAuthorizePending; decision: 'approve' | 'deny' }
  | { kind: 'redirecting' }
  | { kind: 'error'; message: string }

function failureState(error: OAuthAuthorizeError | ApiError): OAuthAuthorizeState {
  if (error instanceof OAuthAuthorizeError) {
    if (error.redirectUrl) {
      window.location.assign(error.redirectUrl)
      return { kind: 'redirecting' }
    }
    return { kind: 'error', message: error.message }
  }
  return { kind: 'error', message: error.message }
}

export function OAuthAuthorize() {
  const query = window.location.search
  const [state, setState] = useState<OAuthAuthorizeState>(
    query ? { kind: 'loading' } : { kind: 'error', message: 'Missing authorization request' },
  )

  useEffect(() => {
    let cancelled = false
    async function load() {
      if (!query) return
      try {
        const request = await pendingOAuthAuthorization(query)
        if (!cancelled) setState({ kind: 'ready', request })
      } catch (error) {
        if (cancelled) return
        if (error instanceof OAuthAuthorizeError || error instanceof ApiError) {
          setState(failureState(error))
        } else {
          setState({ kind: 'error', message: 'Authorization failed' })
        }
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [query])

  async function submit(decision: 'approve' | 'deny') {
    setState((prev) =>
      prev.kind === 'ready' ? { kind: 'submitting', request: prev.request, decision } : prev,
    )
    try {
      const redirectUrl =
        decision === 'approve'
          ? await approveOAuthAuthorization(query)
          : await denyOAuthAuthorization(query)
      window.location.assign(redirectUrl)
      setState({ kind: 'redirecting' })
    } catch (error) {
      if (error instanceof OAuthAuthorizeError || error instanceof ApiError) {
        setState(failureState(error))
      } else {
        setState({ kind: 'error', message: 'Authorization failed' })
      }
    }
  }

  return (
    <AuthLayout>
      <div className="flex flex-col gap-6">
        <div>
          <h1 className="type-title">Authorize access to Omnara</h1>
          <p className="text-muted-foreground mt-1 text-sm">
            This application is asking to act on your behalf through the Omnara MCP server.
          </p>
        </div>

        {state.kind === 'loading' && (
          <div className="flex items-center gap-3 text-sm">
            <Spinner />
            Loading authorization request
          </div>
        )}

        {(state.kind === 'ready' || state.kind === 'submitting') && (
          <div className="space-y-5 rounded-lg border p-5">
            <div className="space-y-3 text-sm">
              <Detail label="Application" value={state.request.clientName} />
              {state.request.clientUri && (
                <Detail label="Website" value={state.request.clientUri} />
              )}
              <Detail label="Client ID" value={state.request.clientId} />
              <Detail label="Redirects to" value={state.request.redirectHost} />
            </div>
            {state.request.loopback && (
              <p className="text-muted-foreground text-sm">
                This application redirects to a program running on your own computer. Only approve
                if you started this authorization yourself.
              </p>
            )}
            <div className="flex flex-wrap gap-3">
              <Button
                loading={state.kind === 'submitting' && state.decision === 'approve'}
                icon={<Check className="h-4 w-4" />}
                onClick={() => {
                  void submit('approve')
                }}
                disabled={state.kind === 'submitting'}
              >
                Approve
              </Button>
              <Button
                variant="outline"
                loading={state.kind === 'submitting' && state.decision === 'deny'}
                icon={<X className="h-4 w-4" />}
                onClick={() => {
                  void submit('deny')
                }}
                disabled={state.kind === 'submitting'}
              >
                Deny
              </Button>
            </div>
          </div>
        )}

        {state.kind === 'redirecting' && (
          <div className="flex items-center gap-3 text-sm">
            <Spinner />
            Returning to the application
          </div>
        )}
        {state.kind === 'error' && (
          <div className="space-y-1 rounded-lg border p-5 text-sm">
            <div className="font-medium">{state.message}</div>
          </div>
        )}
      </div>
    </AuthLayout>
  )
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1 sm:grid-cols-[8rem_1fr]">
      <div className="text-muted-foreground">{label}</div>
      <div className="break-all font-medium">{value}</div>
    </div>
  )
}
