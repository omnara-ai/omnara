import {
  approveDeviceAuth,
  denyDeviceAuth,
  type DeviceAuthPending,
  pendingDeviceAuth,
} from '@omnara/sdk/browser'
import { Check, X } from 'lucide-react'
import { useEffect, useMemo, useState } from 'react'

import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'

type DeviceAuthState =
  | { kind: 'loading' }
  | { kind: 'ready'; flow: DeviceAuthPending }
  | { kind: 'submitting'; flow: DeviceAuthPending }
  | { kind: 'approved' }
  | { kind: 'denied' }
  | { kind: 'error'; message: string }

export function DeviceAuth() {
  const userCode = useMemo(() => {
    return new URLSearchParams(window.location.search).get('user_code')?.trim() ?? ''
  }, [])
  const [state, setState] = useState<DeviceAuthState>(
    userCode ? { kind: 'loading' } : { kind: 'error', message: 'Missing device code' },
  )

  useEffect(() => {
    let cancelled = false
    async function load() {
      if (!userCode) return
      try {
        const flow = await pendingDeviceAuth(userCode)
        if (!cancelled) setState({ kind: 'ready', flow })
      } catch (error) {
        if (!cancelled) {
          setState({ kind: 'error', message: errorMessage(error, 'Device approval failed') })
        }
      }
    }
    void load()
    return () => {
      cancelled = true
    }
  }, [userCode])

  async function submit(decision: 'approve' | 'deny') {
    setState((prev) => (prev.kind === 'ready' ? { kind: 'submitting', flow: prev.flow } : prev))
    try {
      if (decision === 'approve') {
        await approveDeviceAuth(userCode)
        setState({ kind: 'approved' })
      } else {
        await denyDeviceAuth(userCode)
        setState({ kind: 'denied' })
      }
    } catch (error) {
      setState({ kind: 'error', message: errorMessage(error, 'Device approval failed') })
    }
  }

  return (
    <div className="mx-auto flex max-w-xl flex-col gap-6">
      <div>
        <h1 className="text-2xl font-semibold tracking-tight">Approve device login</h1>
        <p className="text-muted-foreground mt-1 text-sm">
          Review the request before granting API access.
        </p>
      </div>

      {state.kind === 'loading' && (
        <div className="flex items-center gap-3 text-sm">
          <Spinner />
          Loading device request
        </div>
      )}

      {(state.kind === 'ready' || state.kind === 'submitting') && (
        <div className="space-y-5 rounded-lg border p-5">
          <div className="space-y-3 text-sm">
            <Detail label="Client" value={state.flow.clientName || 'Unknown client'} />
            <Detail label="Token" value={state.flow.tokenName || 'Device token'} />
            <Detail label="Code" value={userCode} />
          </div>
          <div className="flex flex-wrap gap-3">
            <Button
              onClick={() => {
                void submit('approve')
              }}
              disabled={state.kind === 'submitting'}
            >
              {state.kind === 'submitting' ? (
                <Spinner />
              ) : (
                <Check className="h-4 w-4" aria-hidden />
              )}
              Approve
            </Button>
            <Button
              variant="outline"
              onClick={() => {
                void submit('deny')
              }}
              disabled={state.kind === 'submitting'}
            >
              <X className="h-4 w-4" aria-hidden />
              Deny
            </Button>
          </div>
        </div>
      )}

      {state.kind === 'approved' && <Result title="Device approved" />}
      {state.kind === 'denied' && <Result title="Device denied" />}
      {state.kind === 'error' && <Result title={state.message} />}
    </div>
  )
}

function Detail({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-1 sm:grid-cols-[8rem_1fr]">
      <div className="text-muted-foreground">{label}</div>
      <div className="break-words font-medium">{value}</div>
    </div>
  )
}

function Result({ title }: { title: string }) {
  return <div className="rounded-lg border p-5 text-sm font-medium">{title}</div>
}
