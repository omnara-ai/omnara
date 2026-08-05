import {
  useAcceptInvitation,
  useCreateOrganization,
  useDeclineInvitation,
  usePendingInvitations,
} from '@omnara/react'
import { Building2, Check, X } from 'lucide-react'
import type { SyntheticEvent } from 'react'
import { useState } from 'react'

import { BrandMark } from '@/components/brand/OmnaraMark'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from '@/components/ui/card'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Spinner } from '@/components/ui/spinner'
import { errorMessage } from '@/lib/submit-status'

type OnboardingAction =
  | { kind: 'idle' }
  | { kind: 'creating' }
  | { kind: 'acting'; invitationId: string }
  | { kind: 'error'; message: string }

interface OnboardingState {
  name: string
  idempotencyKey: string
  action: OnboardingAction
}

function initialOnboardingState(): OnboardingState {
  return { name: '', idempotencyKey: crypto.randomUUID(), action: { kind: 'idle' } }
}

export function Onboarding() {
  const { data, refetch } = usePendingInvitations()
  const pending = data.data
  const createOrganization = useCreateOrganization()
  const acceptInvitation = useAcceptInvitation()
  const declineInvitation = useDeclineInvitation()

  const [state, setState] = useState<OnboardingState>(initialOnboardingState)
  const creating = state.action.kind === 'creating'
  const actingId = state.action.kind === 'acting' ? state.action.invitationId : null
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

  async function accept(invitationID: string) {
    setState((prev) => ({ ...prev, action: { kind: 'acting', invitationId: invitationID } }))
    try {
      await acceptInvitation.mutateAsync(invitationID)
      window.location.assign('/')
    } catch (err) {
      setState((prev) => ({
        ...prev,
        action: { kind: 'error', message: errorMessage(err, 'Could not accept invitation') },
      }))
    }
  }

  async function decline(invitationID: string) {
    setState((prev) => ({ ...prev, action: { kind: 'acting', invitationId: invitationID } }))
    try {
      await declineInvitation.mutateAsync(invitationID)
      await refetch()
      setState((prev) => ({ ...prev, action: { kind: 'idle' } }))
    } catch (err) {
      setState((prev) => ({
        ...prev,
        action: { kind: 'error', message: errorMessage(err, 'Could not decline invitation') },
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
          <div className="flex flex-col gap-3">
            {pending.map((invite) => (
              <Card key={invite.id}>
                <CardContent className="flex items-center gap-3 py-4">
                  <span className="bg-secondary text-secondary-foreground flex size-9 shrink-0 items-center justify-center rounded-lg">
                    <Building2 className="size-4" />
                  </span>
                  <div className="flex min-w-0 flex-1 flex-col">
                    <span className="truncate text-sm font-medium">Organization invitation</span>
                    <span className="text-muted-foreground truncate text-xs capitalize">
                      Role: {invite.org_role}
                    </span>
                  </div>
                  <div className="flex shrink-0 gap-2">
                    <Button
                      size="sm"
                      disabled={actingId !== null}
                      onClick={() => {
                        void accept(invite.id)
                      }}
                    >
                      {actingId === invite.id ? <Spinner /> : <Check />}
                      Accept
                    </Button>
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={actingId !== null}
                      aria-label="Decline invitation"
                      onClick={() => {
                        void decline(invite.id)
                      }}
                    >
                      <X />
                    </Button>
                  </div>
                </CardContent>
              </Card>
            ))}
          </div>
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
