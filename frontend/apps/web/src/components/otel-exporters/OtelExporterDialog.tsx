import { useCreateOtelExporter, useUpdateOtelExporter } from '@omnara/react'
import type {
  OtelExporter,
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
import {
  endpointForVariant,
  headersForVariant,
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
  // Encoding and sampling rarely change, so they stay folded away unless already customised.
  const [advancedOpen, setAdvancedOpen] = useState(
    exporter !== undefined && (exporter.encoding !== 'protobuf' || exporter.sample_percent !== 100),
  )
  const id = useId()
  const initialScope: OtelExporterScope =
    exporter?.scope ?? (projectId ? { type: 'project', project_id: projectId } : { type: 'org' })
  const variant: OtelTraceVariant = exporter?.variant ?? 'otel'
  const encoding: OtelExporterEncoding = exporter?.encoding ?? 'protobuf'
  const form = useForm({
    defaultValues: {
      scope: initialScope,
      endpoint: exporter?.endpoint ?? '',
      variant,
      encoding,
      headerRows: textRowsFromRecord(exporter?.headers ?? {}),
      secretHeaderRows: secretRowsFromRecord(exporter?.secret_headers ?? {}),
      samplePercent: String(exporter?.sample_percent ?? 100),
    },
    onSubmit: async ({ value }) => {
      if (pending || !valid(value)) return
      setError('')
      const settings = {
        endpoint: value.endpoint.trim(),
        variant: value.variant,
        encoding: value.encoding,
        headers: recordFromTextRows(value.headerRows) ?? {},
        secret_headers: recordFromSecretRows(value.secretHeaderRows) ?? {},
        sample_percent: Number(value.samplePercent),
      }
      try {
        if (exporter) await update.mutateAsync({ ...settings, exporterID: exporter.id })
        else await create.mutateAsync({ ...settings, scope: value.scope })
        onClose()
      } catch (err) {
        setError(errorMessage(err, 'Could not save exporter'))
      }
    },
  })

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
                  <OtelVariantSelect
                    value={field.state.value}
                    onValueChange={(next) => {
                      field.handleChange(next)
                      form.setFieldValue(
                        'endpoint',
                        endpointForVariant(next, form.getFieldValue('endpoint')),
                      )
                      const headers = headersForVariant(
                        next,
                        form.getFieldValue('headerRows'),
                        form.getFieldValue('secretHeaderRows'),
                      )
                      form.setFieldValue('headerRows', headers.textRows)
                      form.setFieldValue('secretHeaderRows', headers.secretRows)
                      // A backend that takes one encoding moves the exporter onto it.
                      const [fallback] = variantEncodings[next]
                      if (
                        fallback &&
                        !variantEncodings[next].includes(form.getFieldValue('encoding'))
                      )
                        form.setFieldValue('encoding', fallback)
                    }}
                  />
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
                  headerRows: state.values.headerRows,
                  secretHeaderRows: state.values.secretHeaderRows,
                })}
              >
                {({ headerRows, secretHeaderRows }) => (
                  <KeyValueEditor
                    orgId={orgId}
                    enabled
                    label="Headers"
                    itemLabel="Header"
                    keyPlaceholder="Authorization"
                    textRows={headerRows}
                    secretRows={secretHeaderRows}
                    onChange={({ textRows, secretRows }) => {
                      form.setFieldValue('headerRows', textRows)
                      form.setFieldValue('secretHeaderRows', secretRows)
                    }}
                  />
                )}
              </form.Subscribe>
              <OverridesCollapsible
                title="Advanced"
                open={advancedOpen}
                onOpenChange={setAdvancedOpen}
              >
                <div className="grid items-start gap-4 sm:grid-cols-2">
                  <form.Subscribe selector={(state) => state.values.variant}>
                    {(selectedVariant) => (
                      <form.Field name="encoding">
                        {(field) => (
                          <Field>
                            <FieldLabel htmlFor={id + '-encoding'}>Encoding</FieldLabel>
                            <Select
                              value={field.state.value}
                              disabled={variantEncodings[selectedVariant].length < 2}
                              onValueChange={(value) => {
                                const next = variantEncodings[selectedVariant].find(
                                  (option) => option === value,
                                )
                                if (next) field.handleChange(next)
                              }}
                            >
                              <SelectTrigger id={id + '-encoding'}>
                                <SelectValue>{otelEncodingLabels[field.state.value]}</SelectValue>
                              </SelectTrigger>
                              <SelectContent>
                                {variantEncodings[selectedVariant].map((value) => (
                                  <SelectItem key={value} value={value}>
                                    {otelEncodingLabels[value]}
                                  </SelectItem>
                                ))}
                              </SelectContent>
                            </Select>
                          </Field>
                        )}
                      </form.Field>
                    )}
                  </form.Subscribe>
                  <form.Field name="samplePercent">
                    {(field) => (
                      <Field>
                        <FieldLabel htmlFor={id + '-sample'}>Turn sample rate (%)</FieldLabel>
                        <Input
                          id={id + '-sample'}
                          type="number"
                          required
                          min={0}
                          max={100}
                          step="any"
                          value={field.state.value}
                          onChange={(event) => {
                            field.handleChange(event.target.value)
                          }}
                        />
                        {samplePercentError(field.state.value) && (
                          <FieldError>{samplePercentError(field.state.value)}</FieldError>
                        )}
                      </Field>
                    )}
                  </form.Field>
                </div>
              </OverridesCollapsible>
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

function valid(value: {
  scope: OtelExporterScope
  endpoint: string
  headerRows: Parameters<typeof textRowsValid>[0]
  secretHeaderRows: Parameters<typeof secretRowsValid>[0]
  samplePercent: string
}) {
  return (
    otelExporterScopeComplete(value.scope) &&
    !endpointError(value.endpoint) &&
    !samplePercentError(value.samplePercent) &&
    textRowsValid(value.headerRows) &&
    value.headerRows.every((row) => !!row.value) &&
    secretRowsValid(value.secretHeaderRows)
  )
}
