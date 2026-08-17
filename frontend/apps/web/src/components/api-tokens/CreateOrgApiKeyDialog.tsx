import { useCreateOrgApiKey } from '@omnara/react'
import type { OrgApiKeyRole } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { ApiTokenRevealContent } from '@/components/api-tokens/ApiTokenRevealContent'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { errorMessage } from '@/lib/submit-status'

const ORG_ROLES = ['member', 'admin'] as const

export function CreateOrgApiKeyDialog({
  open,
  onOpenChange,
  orgId,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
}) {
  const createKey = useCreateOrgApiKey(orgId)
  const [name, setName] = useState('')
  const [orgRole, setOrgRole] = useState<OrgApiKeyRole>('member')
  const [plaintext, setPlaintext] = useState<string>()
  const [error, setError] = useState<string>()

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setError(undefined)
    try {
      const result = await createKey.mutateAsync({ name: name.trim(), org_role: orgRole })
      setPlaintext(result.token)
    } catch (err) {
      setError(errorMessage(err, 'Could not create API token'))
    }
  }

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      setName('')
      setOrgRole('member')
      setPlaintext(undefined)
      setError(undefined)
    }
    onOpenChange(nextOpen)
  }

  return (
    <Dialog open={open} onOpenChange={handleOpenChange}>
      <DialogContent showCloseButton={plaintext === undefined}>
        {plaintext ? (
          <ApiTokenRevealContent
            token={plaintext}
            onDone={() => {
              handleOpenChange(false)
            }}
          />
        ) : (
          <>
            <DialogHeader>
              <DialogTitle>New organization API token</DialogTitle>
              <DialogDescription>
                Create an organization API token for integrations and automation that act on behalf
                of the organization rather than a person.
              </DialogDescription>
            </DialogHeader>
            <form onSubmit={(event) => void submit(event)}>
              <FieldGroup>
                <Field>
                  <FieldLabel htmlFor="org-api-key-name">Name</FieldLabel>
                  <Input
                    id="org-api-key-name"
                    required
                    value={name}
                    placeholder="CI deployments"
                    autoComplete="off"
                    onChange={(event) => {
                      setName(event.target.value)
                    }}
                  />
                  <FieldDescription>Describe where you plan to use this token.</FieldDescription>
                </Field>
                <Field>
                  <FieldLabel>Org role</FieldLabel>
                  <div className="flex gap-2">
                    {ORG_ROLES.map((role) => (
                      <Button
                        key={role}
                        type="button"
                        variant={orgRole === role ? 'default' : 'outline'}
                        className="flex-1 capitalize"
                        onClick={() => {
                          setOrgRole(role)
                        }}
                      >
                        {role}
                      </Button>
                    ))}
                  </div>
                  <FieldDescription>
                    Admin tokens can manage the organization and access every project. Member tokens
                    can only access projects they are explicitly granted.
                  </FieldDescription>
                </Field>
                {error && <p className="text-destructive text-sm">{error}</p>}
                <DialogFooter>
                  <Button
                    type="submit"
                    disabled={createKey.isPending || name.trim() === ''}
                    loading={createKey.isPending}
                  >
                    Create token
                  </Button>
                </DialogFooter>
              </FieldGroup>
            </form>
          </>
        )}
      </DialogContent>
    </Dialog>
  )
}
