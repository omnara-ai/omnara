import type { AppType } from '@omnara/sdk'

import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

export function AppCredentialFields({ appType }: { appType: AppType }) {
  if (appType === 'github_pr') {
    return (
      <>
        <Field>
          <FieldLabel htmlFor="webhook-secret">Webhook secret</FieldLabel>
          <Input id="webhook-secret" name="webhookSecret" type="password" required />
        </Field>
        <Field className="sm:col-span-2">
          <FieldLabel htmlFor="private-key">RSA private key (PEM)</FieldLabel>
          <Textarea
            id="private-key"
            name="privateKey"
            className="font-mono"
            required
            spellCheck={false}
          />
        </Field>
      </>
    )
  }
  return (
    <Field>
      <FieldLabel htmlFor="bot-token">Bot token</FieldLabel>
      <Input id="bot-token" name="botToken" type="password" required />
    </Field>
  )
}
