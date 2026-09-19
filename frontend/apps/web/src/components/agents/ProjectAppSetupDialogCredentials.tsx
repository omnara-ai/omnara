import type { ProfileAppProvider } from '@omnara/sdk'

import { Field, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'

export function AppCredentialFields({ provider }: { provider: ProfileAppProvider }) {
  if (provider === 'github') {
    return (
      <>
        <Field>
          <FieldLabel htmlFor="private-key">RSA private key (PEM)</FieldLabel>
          <Textarea id="private-key" name="privateKey" required spellCheck={false} />
        </Field>
        <Field>
          <FieldLabel htmlFor="webhook-secret">Webhook secret</FieldLabel>
          <Input id="webhook-secret" name="webhookSecret" type="password" required />
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
