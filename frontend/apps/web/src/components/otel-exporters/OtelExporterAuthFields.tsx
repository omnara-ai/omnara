import type { OtelExporterAuthKind, OtelTraceVariant } from '@omnara/sdk'
import { useId } from 'react'

import {
  otelAuthKindLabels,
  otelAuthKinds,
  otelVariantLabels,
} from '@/components/otel-exporters/otelExporterOptions'
import { CredentialSecretField } from '@/components/secrets/CredentialSecretField'
import { Field, FieldError, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'

function credentialCopy(variant: OtelTraceVariant, authKind: OtelExporterAuthKind) {
  const label = otelVariantLabels[variant]
  if (authKind === 'basic') {
    const isLangfuse = variant === 'langfuse'
    return {
      usernameLabel: isLangfuse ? 'Public key' : 'Username',
      usernamePlaceholder: isLangfuse ? 'pk-lf-…' : 'username',
      secretLabel: isLangfuse ? 'Secret key' : 'Password',
      secretPlaceholder: isLangfuse ? 'sk-lf-…' : '••••••••',
      defaultSecretName: isLangfuse ? 'langfuse-secret-key' : `${variant}-password`,
      emptyDescription: `No secrets yet — use New secret to store your ${label} ${isLangfuse ? 'secret key' : 'password'}.`,
    }
  }
  return {
    usernameLabel: '',
    usernamePlaceholder: '',
    secretLabel: authKind === 'bearer_token' ? 'Bearer token' : 'API key',
    secretPlaceholder: variant === 'langsmith' ? 'lsv2_…' : 'sk-…',
    defaultSecretName: `${variant}-api-key`,
    emptyDescription: `No secrets yet — use New secret to store your ${label} credential.`,
  }
}

export function OtelExporterAuthFields({
  orgId,
  projectId,
  variant,
  authKind,
  authHeaderName,
  authUsername,
  credentialSecretId,
  onAuthKindChange,
  onAuthHeaderNameChange,
  onAuthUsernameChange,
  onCredentialSecretIdChange,
}: {
  orgId: string
  /** The exporter's project, whose available secrets are offered. Empty until one is chosen. */
  projectId: string
  variant: OtelTraceVariant
  authKind: OtelExporterAuthKind
  authHeaderName: string
  authUsername: string
  credentialSecretId: string
  onAuthKindChange: (nextKind: OtelExporterAuthKind) => void
  onAuthHeaderNameChange: (value: string) => void
  onAuthUsernameChange: (value: string) => void
  onCredentialSecretIdChange: (value: string) => void
}) {
  const id = useId()
  const copy = credentialCopy(variant, authKind)
  return (
    <div className="grid items-start gap-4 sm:grid-cols-2">
      <Field>
        <FieldLabel htmlFor={id + '-auth-kind'}>Authentication</FieldLabel>
        <Select
          value={authKind}
          onValueChange={(nextValue) => {
            const nextKind = otelAuthKinds.find((item) => item === nextValue)
            if (nextKind) onAuthKindChange(nextKind)
          }}
        >
          <SelectTrigger id={id + '-auth-kind'}>
            <SelectValue>{otelAuthKindLabels[authKind]}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {otelAuthKinds.map((item) => (
              <SelectItem key={item} value={item}>
                {otelAuthKindLabels[item]}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </Field>
      {authKind === 'api_key_header' && (
        <Field>
          <FieldLabel htmlFor={id + '-auth-header'}>Header name</FieldLabel>
          <Input
            id={id + '-auth-header'}
            required
            placeholder="x-api-key"
            value={authHeaderName}
            onChange={(event) => {
              onAuthHeaderNameChange(event.target.value)
            }}
          />
        </Field>
      )}
      {authKind === 'basic' && (
        <Field>
          <FieldLabel htmlFor={id + '-auth-username'}>{copy.usernameLabel}</FieldLabel>
          <Input
            id={id + '-auth-username'}
            required
            maxLength={1024}
            placeholder={copy.usernamePlaceholder}
            value={authUsername}
            onChange={(event) => {
              onAuthUsernameChange(event.target.value)
            }}
          />
          {authUsername.includes(':') && <FieldError>Cannot contain &ldquo;:&rdquo;.</FieldError>}
        </Field>
      )}
      {authKind !== 'none' && (
        <div className="sm:col-span-2">
          <CredentialSecretField
            orgId={orgId}
            projectId={projectId}
            enabled={projectId !== ''}
            value={credentialSecretId}
            onChange={onCredentialSecretIdChange}
            label={copy.secretLabel}
            placeholder="Search secrets…"
            emptyDescription={copy.emptyDescription}
            defaultSecretName={copy.defaultSecretName}
            secretValuePlaceholder={copy.secretPlaceholder}
          />
        </div>
      )}
    </div>
  )
}
