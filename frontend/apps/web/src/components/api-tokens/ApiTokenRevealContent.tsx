import { Check, Copy } from 'lucide-react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

export function ApiTokenRevealContent({ token, onDone }: { token: string; onDone: () => void }) {
  const [copied, setCopied] = useState(false)
  const [error, setError] = useState<string>()

  async function copyToken() {
    try {
      await navigator.clipboard.writeText(token)
      setCopied(true)
    } catch {
      setError('Could not copy the API token')
    }
  }

  return (
    <>
      <DialogHeader>
        <DialogTitle>Copy your API token</DialogTitle>
        <DialogDescription>
          This token is shown only once. Store it somewhere secure before closing this window.
        </DialogDescription>
      </DialogHeader>
      <FieldGroup className="gap-4">
        <Field>
          <FieldLabel htmlFor="api-token-plaintext">API token</FieldLabel>
          <div className="flex items-center gap-2">
            <Input
              id="api-token-plaintext"
              className="font-mono"
              value={token}
              readOnly
              onFocus={(event) => {
                event.currentTarget.select()
              }}
            />
            <Button type="button" variant="outline" onClick={() => void copyToken()}>
              {copied ? <Check /> : <Copy />}
              {copied ? 'Copied' : 'Copy'}
            </Button>
          </div>
          <FieldDescription>Use it as a bearer token when calling the Omnara API.</FieldDescription>
        </Field>
        {error && <p className="text-destructive text-sm">{error}</p>}
        <DialogFooter>
          <Button type="button" onClick={onDone}>
            Done
          </Button>
        </DialogFooter>
      </FieldGroup>
    </>
  )
}
