import { useCreateOtelExporter, useUpdateOtelExporter } from '@omnara/react'
import type {
  CreateOtelExporterRequest,
  OtelExporter,
  OtelExporterAuthKind,
  OtelExporterAuthOptions,
  OtelExporterEncoding,
  OtelExporterScope,
  OtelTraceVariant,
} from '@omnara/sdk'
import { useForm } from '@tanstack/react-form'
import { useId, useState } from 'react'

import { KeyValueEditor } from '@/components/key-value/KeyValueEditor'
import {
  recordFromSecretRows,
  recordFromTextRows,
  secretRowsFromRecord,
  secretRowsValid,
  textRowsFromRecord,
  textRowsValid,
} from '@/components/key-value/keyValueRows'
import { OverridesCollapsible } from '@/components/machines/MachineOverrideFields'
import { OtelExporterAuthFields } from '@/components/otel-exporters/OtelExporterAuthFields'
import {
  applyVariantSwitch,
  otelEncodingLabels,
  otelExporterScopeComplete,
  variantEncodings,
} from '@/components/otel-exporters/otelExporterOptions'
import {
  OtelExporterScopeField,
  OtelExporterScopeLabel,
} from '@/components/otel-exporters/OtelExporterScopeField'
import { OtelVariantSelect } from '@/components/otel-exporters/OtelVariantSelect'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldError, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { errorMessage } from '@/lib/submit-status'

const endpointHint = 'Enter the full traces URL, such as https://otel.example.com/v1/traces.'

interface OtelExporterFormValues {
  scope: OtelExporterScope
  endpoint: string
  variant: OtelTraceVariant
  encoding: OtelExporterEncoding
  authKind: OtelExporterAuthKind
  authHeaderName: string
  authUsername: string
  credentialSecretId: string
  headerRows: ReturnType<typeof textRowsFromRecord>
  secretHeaderRows: ReturnType<typeof secretRowsFromRecord>
  samplePercent: string
}

function endpointError(value: string) {
  try {
    return ['https:', 'http:'].includes(new URL(value.trim()).protocol) ? undefined : endpointHint
  } catch {
    return endpointHint
  }
}

function samplePercentError(value: string) {
  const percent = Number(value)
  return value.trim() !== '' && percent >= 0 && percent <= 100
    ? undefined
    : 'Enter a percentage from 0 to 100.'
}

function authOptionsForSubmit(
  authKind: OtelExporterAuthKind,
  authHeaderName: string,
  authUsername: string,
): OtelExporterAuthOptions {
  if (authKind === 'api_key_header') return { header_name: authHeaderName.trim() }
  if (authKind === 'basic') return { username: authUsername.trim() }
  return {}
}

function authValid(value: Pick<OtelExporterFormValues, 'authKind' | 'authHeaderName' | 'authUsername' | 'credentialSecretId'>) {
  if (value.authKind === 'none') return true
  if (!value.credentialSecretId) return false
  if (value.authKind === 'api_key_header') return value.authHeaderName.trim().length > 0
  if (value.authKind === 'basic') {
    const username = value.authUsername.trim()
    return username.length > 0 && !username.includes(':')
  }
  return true
}

function defaultFormValues(exporter?: OtelExporter, projectId?: string): OtelExporterFormValues {
  return {
    scope: exporter?.scope ?? { type: 'project', project_id: projectId ?? '' },
    endpoint: exporter?.endpoint ?? '',
    variant: exporter?.variant ?? 'otel',
    encoding: exporter?.encoding ?? 'protobuf',
    authKind: exporter?.auth_kind ?? 'none',
    authHeaderName: exporter?.auth_options.header_name ?? '',
    authUsername: exporter?.auth_options.username ?? '',
    credentialSecretId: exporter?.credential_secret_id ?? '',
    headerRows: textRowsFromRecord(exporter?.headers ?? {}),
    secretHeaderRows: secretRowsFromRecord(exporter?.secret_headers ?? {}),
    samplePercent: String(exporter?.sample_percent ?? 100),
  }
}

function buildBaseSettings(value: OtelExporterFormValues) {
  return {
    endpoint: value.endpoint.trim(),
    variant: value.variant,
    encoding: value.encoding,
    auth_kind: value.authKind,
    auth_options: authOptionsForSubmit(value.authKind, value.authHeaderName, value.authUsername),
    headers: recordFromTextRows(value.headerRows) ?? {},
    secret_headers: recordFromSecretRows(value.secretHeaderRows) ?? {},
    sample_percent: Number(value.samplePercent),
  }
}

function OtelExporterAdvancedFields({
  id,
  open,
  onOpenChange,
  variant,
  encoding,
  samplePercent,
  onEncodingChange,
  onSamplePercentChange,
}: {
  id: string
  open: boolean
  onOpenChange: (open: boolean) => void
  variant: OtelTraceVariant
  encoding: OtelExporterEncoding
  samplePercent: string
  onEncodingChange: (value: OtelExporterEncoding) => void
  onSamplePercentChange: (value: string) => void
}) {
  const sampleError = samplePercentError(samplePercent)
  return (
    <OverridesCollapsible title="Advanced" open={open} onOpenChange={onOpenChange}>
      <div className="grid items-start gap-4 sm:grid-cols-2">
        <Field>
          <FieldLabel htmlFor={id + '-encoding'}>Encoding</FieldLabel>
          <Select
            value={encoding}
            disabled={variantEncodings[variant].length < 2}
            onValueChange={(value) => {
              const next = variantEncodings[variant].find((option) => option === value)
              if (next) onEncodingChange(next)
            }}
          >
            <SelectTrigger id={id + '-encoding'}>
              <SelectValue>{otelEncodingLabels[encoding]}</SelectValue>
            </SelectTrigger>
            <SelectContent>
              {variantEncodings[variant].map((value) => (
                <SelectItem key={value} value={value}>
                  {otelEncodingLabels[value]}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </Field>
        <Field>
          <FieldLabel htmlFor={id + '-sample'}>Turn sample rate (%)</FieldLabel>
          <Input
            id={id + '-sample'}
            type="number"
            required
            min={0}
            max={100}
            step="any"
            value={samplePercent}
            onChange={(event) => {
              onSamplePercentChange(event.target.value)
            }}
          />
          {sampleError && <FieldError>{sampleError}</FieldError>}
        </Field>
      </div>
    </OverridesCollapsible>
  )
}

export function OtelExporterDialog({
  orgId,
  projectId,
  exporter,
  onClose,
}: {
  orgId: string
  /** The project the dialog was opened from, when there is one. */
  projectId?: string
  exporter?: OtelExporter
  onClose: () => void
}) {
  const create = useCreateOtelExporter(orgId)
  const update = useUpdateOtelExporter(orgId)
  const pending = create.isPending || update.isPending
  const [error, setError] = useState('')
  const [advancedOpen, setAdvancedOpen] = useState(
    exporter !== undefined && (exporter.encoding !== 'protobuf' || exporter.sample_percent !== 100),
  )
  const id = useId()
  const form = useForm({
    defaultValues: defaultFormValues(exporter, projectId),
    onSubmit: async ({ value }) => {
      if (pending || !valid(value)) return
      setError('')
      const baseSettings = buildBaseSettings(value)
      try {
        if (exporter) {
          await update.mutateAsync({
            ...baseSettings,
            credential_secret_id: value.authKind === 'none' ? null : value.credentialSecretId,
            exporterID: exporter.id,
          })
        } else {
          const request: CreateOtelExporterRequest = { ...baseSettings, scope: value.scope }
          if (value.authKind !== 'none') request.credential_secret_id = value.credentialSecretId
          await create.mutateAsync(request)
        }
        onClose()
      } catch (err) {
        setError(errorMessage(err, 'Could not save exporter'))
      }
    },
  })

  function handleVariantChange(next: OtelTraceVariant) {
    const updated = applyVariantSwitch({
      nextVariant: next,
      endpoint: form.getFieldValue('endpoint'),
      encoding: form.getFieldValue('encoding'),
      authKind: form.getFieldValue('authKind'),
      authHeaderName: form.getFieldValue('authHeaderName'),
      authUsername: form.getFieldValue('authUsername'),
      credentialSecretId: form.getFieldValue('credentialSecretId'),
      headerRows: form.getFieldValue('headerRows'),
      secretHeaderRows: form.getFieldValue('secretHeaderRows'),
    })
    form.setFieldValue('variant', next)
    form.setFieldValue('endpoint', updated.endpoint)
    form.setFieldValue('encoding', updated.encoding)
    form.setFieldValue('authKind', updated.authKind)
    form.setFieldValue('authHeaderName', updated.authHeaderName)
    form.setFieldValue('authUsername', updated.authUsername)
    form.setFieldValue('credentialSecretId', updated.credentialSecretId)
    form.setFieldValue('headerRows', updated.headerRows)
    form.setFieldValue('secretHeaderRows', updated.secretHeaderRows)
  }

  function handleAuthKindChange(nextKind: OtelExporterAuthKind) {
    form.setFieldValue('authKind', nextKind)
    if (nextKind === 'none') {
      form.setFieldValue('credentialSecretId', '')
      form.setFieldValue('authHeaderName', '')
      form.setFieldValue('authUsername', '')
    } else if (nextKind === 'api_key_header' && !form.getFieldValue('authHeaderName')) {
      form.setFieldValue('authHeaderName', 'x-api-key')
    }
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose()
      }}
    >
      <DialogContent className="sm:max-w-2xl">
        <DialogHeader>
          <DialogTitle>{exporter ? 'Edit exporter' : 'Add exporter'}</DialogTitle>
          <DialogDescription className="sr-only">
            Send agent turns to OpenTelemetry, Langfuse, Braintrust, or LangSmith as traces.
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            void form.handleSubmit()
          }}
        >
          <fieldset disabled={pending}>
            <FieldGroup>
              <form.Field name="variant">
                {(field) => (
                  <OtelVariantSelect value={field.state.value} onValueChange={handleVariantChange} />
                )}
              </form.Field>
              <div className="grid items-start gap-4 sm:grid-cols-2">
                {exporter ? (
                  <Field>
                    <div className="type-label">Export traces from</div>
                    <div className="flex min-h-9 items-center text-sm">
                      <OtelExporterScopeLabel
                        orgId={orgId}
                        projectId={projectId}
                        scope={exporter.scope}
                      />
                    </div>
                  </Field>
                ) : (
                  <form.Field name="scope">
                    {(field) => (
                      <OtelExporterScopeField
                        orgId={orgId}
                        projectId={projectId}
                        value={field.state.value}
                        onChange={field.handleChange}
                      />
                    )}
                  </form.Field>
                )}
              </div>
              <form.Field name="endpoint">
                {(field) => (
                  <Field>
                    <FieldLabel htmlFor={id + '-endpoint'}>OTLP Endpoint URL</FieldLabel>
                    <Input
                      id={id + '-endpoint'}
                      type="url"
                      required
                      maxLength={2048}
                      placeholder="https://otel.example.com/v1/traces"
                      value={field.state.value}
                      onChange={(event) => {
                        field.handleChange(event.target.value)
                      }}
                    />
                    {field.state.value && endpointError(field.state.value) && (
                      <FieldError>{endpointError(field.state.value)}</FieldError>
                    )}
                  </Field>
                )}
              </form.Field>
              <form.Subscribe
                selector={(state) => ({
                  variant: state.values.variant,
                  authKind: state.values.authKind,
                  authHeaderName: state.values.authHeaderName,
                  authUsername: state.values.authUsername,
                  credentialSecretId: state.values.credentialSecretId,
                })}
              >
                {(auth) => (
                  <OtelExporterAuthFields
                    orgId={orgId}
                    variant={auth.variant}
                    authKind={auth.authKind}
                    authHeaderName={auth.authHeaderName}
                    authUsername={auth.authUsername}
                    credentialSecretId={auth.credentialSecretId}
                    onAuthKindChange={handleAuthKindChange}
                    onAuthHeaderNameChange={(next) => {
                      form.setFieldValue('authHeaderName', next)
                    }}
                    onAuthUsernameChange={(next) => {
                      form.setFieldValue('authUsername', next)
                    }}
                    onCredentialSecretIdChange={(next) => {
                      form.setFieldValue('credentialSecretId', next)
                    }}
                  />
                )}
              </form.Subscribe>
              <form.Subscribe
                selector={(state) => ({
                  headerRows: state.values.headerRows,
                  secretHeaderRows: state.values.secretHeaderRows,
                })}
              >
                {({ headerRows, secretHeaderRows }) => (
                  <KeyValueEditor
                    orgId={orgId}
                    enabled
                    label="Extra headers"
                    itemLabel="Header"
                    keyPlaceholder="x-custom-header"
                    textRows={headerRows}
                    secretRows={secretHeaderRows}
                    onChange={({ textRows, secretRows }) => {
                      form.setFieldValue('headerRows', textRows)
                      form.setFieldValue('secretHeaderRows', secretRows)
                    }}
                  />
                )}
              </form.Subscribe>
              <form.Subscribe
                selector={(state) => ({
                  variant: state.values.variant,
                  encoding: state.values.encoding,
                  samplePercent: state.values.samplePercent,
                })}
              >
                {({ variant, encoding, samplePercent }) => (
                  <OtelExporterAdvancedFields
                    id={id}
                    open={advancedOpen}
                    onOpenChange={setAdvancedOpen}
                    variant={variant}
                    encoding={encoding}
                    samplePercent={samplePercent}
                    onEncodingChange={(next) => {
                      form.setFieldValue('encoding', next)
                    }}
                    onSamplePercentChange={(next) => {
                      form.setFieldValue('samplePercent', next)
                    }}
                  />
                )}
              </form.Subscribe>
              {error && (
                <p role="alert" className="text-destructive text-sm">
                  {error}
                </p>
              )}
              <DialogFooter className="-mx-4 border-t px-4 pt-4 sm:-mx-6 sm:px-6">
                <Button type="button" variant="outline" onClick={onClose}>
                  Cancel
                </Button>
                <form.Subscribe selector={(state) => state.values}>
                  {(value) => (
                    <Button type="submit" loading={pending} disabled={pending || !valid(value)}>
                      {exporter ? 'Save changes' : 'Add exporter'}
                    </Button>
                  )}
                </form.Subscribe>
              </DialogFooter>
            </FieldGroup>
          </fieldset>
        </form>
      </DialogContent>
    </Dialog>
  )
}

function valid(value: OtelExporterFormValues) {
  return (
    otelExporterScopeComplete(value.scope) &&
    !endpointError(value.endpoint) &&
    authValid(value) &&
    !samplePercentError(value.samplePercent) &&
    textRowsValid(value.headerRows) &&
    value.headerRows.every((row) => !!row.value) &&
    secretRowsValid(value.secretHeaderRows)
  )
}
