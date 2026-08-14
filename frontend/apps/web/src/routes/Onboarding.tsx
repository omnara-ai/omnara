import { useCreateOrganization, usePendingInvitations } from '@omnara/react'
import type { SyntheticEvent } from 'react'
import { useState } from 'react'

import { BrandMark } from '@/components/brand/OmnaraMark'
import { PendingInvitationList } from '@/components/invitations/PendingInvitationList'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'

type OnboardingAction = { kind: 'idle' } | { kind: 'creating' } | { kind: 'error'; message: string }

interface OnboardingState {
  name: string
  idempotencyKey: string
  action: OnboardingAction
}

function initialOnboardingState(): OnboardingState {
  return { name: '', idempotencyKey: crypto.randomUUID(), action: { kind: 'idle' } }
}

export function Onboarding() {
  const { data } = usePendingInvitations()
  const pending = data.data
  const createOrganization = useCreateOrganization()

  const [state, setState] = useState<OnboardingState>(initialOnboardingState)
  const creating = state.action.kind === 'creating'
  const error = state.action.kind === 'error' ? state.action.message : null

  async function createOrg(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setState((prev) => ({ ...prev, action: { kind: 'creating' } }))
    try {
      await createOrganization.mutateAsync({
        body: { name: state.name },
        idempotencyKey: state.idempotencyKey,
      })
      window.location.assign('/')
    } catch (err) {
      setState((prev) => ({
        ...prev,
        action: { kind: 'error', message: errorMessage(err, 'Could not create organization') },
      }))
    }
  }

  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-8 p-6">
      <div className="flex items-center gap-2 text-base font-semibold tracking-tight">
        <BrandMark />
        Omnara
      </div>

      <div className="flex w-full max-w-md flex-col gap-6">
        <div className="flex flex-col gap-1 text-center">
          <h1 className="text-2xl font-semibold tracking-tight">Welcome to Omnara</h1>
          <p className="text-muted-foreground text-sm">
            {pending.length > 0
              ? 'Accept an invitation or create your own organization to get started.'
              : 'Create your organization to get started.'}
          </p>
        </div>

        {error && <p className="text-destructive text-center text-sm">{error}</p>}

        {pending.length > 0 && (
          <PendingInvitationList
            invitations={pending}
            onAccepted={() => {
              window.location.assign('/')
            }}
          />
        )}

        <Card>
          <CardHeader>
            <CardTitle>Create an organization</CardTitle>
            <CardDescription>You become the owner and can invite teammates later.</CardDescription>
          </CardHeader>
          <CardContent>
            <form
              onSubmit={(event) => {
                void createOrg(event)
              }}
            >
              <FieldGroup>
                <Field>
                  <FieldLabel htmlFor="org-name">Organization name</FieldLabel>
                  <Input
                    id="org-name"
                    required
                    value={state.name}
                    autoComplete="organization"
                    placeholder="Acme Inc."
                    onChange={(event) => {
                      setState((prev) => ({
                        ...prev,
                        name: event.target.value,
                        idempotencyKey: crypto.randomUUID(),
                      }))
                    }}
                  />
                </Field>
                <Button
                  type="submit"
                  className="w-full"
                  disabled={creating || state.name.trim() === ''}
                >
                  {creating && <Spinner />}
                  Create organization
                </Button>
              </FieldGroup>
            </form>
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
