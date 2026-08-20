import { useCreatePersonalAccessToken } from '@omnara/react'
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
import { ResourceNameFieldError } from '@/components/ui/resource-name-error'
import { resourceNameInputMaxLength, resourceNameValid } from '@/lib/resource-name'
import { errorMessage } from '@/lib/submit-status'

export function CreatePersonalAccessTokenDialog({
  open,
  onOpenChange,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const createToken = useCreatePersonalAccessToken()
  const [name, setName] = useState('')
  const [plaintext, setPlaintext] = useState<string>()
  const [error, setError] = useState<string>()

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setError(undefined)
    try {
      const result = await createToken.mutateAsync({ name })
      setPlaintext(result.token)
    } catch (err) {
      setError(errorMessage(err, 'Could not create API token'))
    }
  }

  function handleOpenChange(nextOpen: boolean) {
    if (!nextOpen) {
      setName('')
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
              <DialogTitle>New API token</DialogTitle>
              <DialogDescription>
                Create a personal access token for API clients and command-line tools.
              </DialogDescription>
            </DialogHeader>
            <form onSubmit={(event) => void submit(event)}>
              <FieldGroup>
                <Field>
                  <FieldLabel htmlFor="api-token-name">Name</FieldLabel>
                  <Input
                    id="api-token-name"
                    required
                    maxLength={resourceNameInputMaxLength}
                    value={name}
                    placeholder="Local development"
                    autoComplete="off"
                    onChange={(event) => {
                      setName(event.target.value)
                    }}
                  />
                  <ResourceNameFieldError value={name} />
                  <FieldDescription>Describe where you plan to use this token.</FieldDescription>
                </Field>
                {error && <p className="text-destructive text-sm">{error}</p>}
                <DialogFooter>
                  <Button
                    type="submit"
                    disabled={createToken.isPending || !resourceNameValid(name)}
                    loading={createToken.isPending}
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
