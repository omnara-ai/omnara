import type { AWSCredentialsSecretFormSecret } from '@/components/org/CreateSecretDialogState'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'

export function AWSCredentialsSecretFields({
  value,
  onChange,
}: {
  value: AWSCredentialsSecretFormSecret
  onChange: (patch: Partial<AWSCredentialsSecretFormSecret>) => void
}) {
  return (
    <>
      <Field>
        <FieldLabel htmlFor="aws-access-key-id">Access key ID</FieldLabel>
        <Input
          id="aws-access-key-id"
          required
          value={value.accessKeyId}
          autoComplete="off"
          onChange={(event) => {
            onChange({ accessKeyId: event.target.value })
          }}
        />
      </Field>
      <Field>
        <FieldLabel htmlFor="aws-secret-access-key">Secret access key</FieldLabel>
        <Input
          id="aws-secret-access-key"
          type="password"
          required
          value={value.secretAccessKey}
          autoComplete="new-password"
          onChange={(event) => {
            onChange({ secretAccessKey: event.target.value })
          }}
        />
      </Field>
      <Field>
        <FieldLabel htmlFor="aws-session-token">Session token</FieldLabel>
        <Input
          id="aws-session-token"
          type="password"
          value={value.sessionToken}
          autoComplete="new-password"
          onChange={(event) => {
            onChange({ sessionToken: event.target.value })
          }}
        />
        <FieldDescription>
          Optional temporary credential token. Rotate this secret before the token expires.
        </FieldDescription>
      </Field>
      <Field>
        <FieldLabel htmlFor="aws-role-arn">Role ARN</FieldLabel>
        <Input
          id="aws-role-arn"
          value={value.roleArn}
          autoComplete="off"
          placeholder="arn:aws:iam::123456789012:role/ReadOnly"
          onChange={(event) => {
            onChange({ roleArn: event.target.value })
          }}
        />
        <FieldDescription>Optional role to assume before accessing AWS.</FieldDescription>
      </Field>
      <Field>
        <FieldLabel htmlFor="aws-external-id">External ID</FieldLabel>
        <Input
          id="aws-external-id"
          value={value.externalId}
          autoComplete="off"
          onChange={(event) => {
            onChange({ externalId: event.target.value })
          }}
        />
        <FieldDescription>Optional; requires a role ARN.</FieldDescription>
      </Field>
    </>
  )
}
