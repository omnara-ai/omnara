import { useUpdateModelProvider } from '@omnara/react'
import { type ModelProviderConfig } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import type { SubmitStatus } from '@/lib/submit-status'
import { idle, statusError, submitError } from '@/lib/submit-status'

import { awsRegionPattern } from './CreateModelProviderDialogState'

interface EditModelProviderState {
  baseUrl: string
  endpointPath: string
  timeout: string
  region: string
  status: SubmitStatus
}

export function EditModelProviderDialog({
  open,
  onOpenChange,
  orgId,
  provider,
}: {
  open: boolean
  onOpenChange: (open: boolean) => void
  orgId: string
  provider: ModelProviderConfig
}) {
  const mutation = useUpdateModelProvider(orgId)
  const [state, setState] = useState<EditModelProviderState>({
    baseUrl: provider.base_url,
    endpointPath: provider.endpoint_path,
    timeout: String(provider.request_timeout_ms),
    region: provider.auth_options.region ?? '',
    status: idle,
  })
  const errorMessage = statusError(state.status)

  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    setState((prev) => ({ ...prev, status: idle }))
    try {
      await mutation.mutateAsync({
        modelProviderConfigID: provider.id,
        base_url: state.baseUrl.trim(),
        endpoint_path: state.endpointPath.trim(),
        request_timeout_ms: Number(state.timeout),
        auth_options:
          provider.auth_kind === 'sigv4'
            ? {
                service: provider.auth_options.service,
                region: state.region.trim(),
              }
            : undefined,
      })
      onOpenChange(false)
    } catch (err) {
      const status = submitError(err, 'Could not update model provider')
      setState((prev) => ({
        ...prev,
        status,
      }))
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Edit {provider.name}</DialogTitle>
        </DialogHeader>
        <form onSubmit={(event) => void submit(event)}>
          <FieldGroup>
            <Field>
              <FieldLabel>Base URL</FieldLabel>
              <Input
                value={state.baseUrl}
                onChange={(event) => {
                  setState((prev) => ({ ...prev, baseUrl: event.target.value }))
                }}
              />
            </Field>
            <Field>
              <FieldLabel>Endpoint path</FieldLabel>
              <Input
                value={state.endpointPath}
                onChange={(event) => {
                  setState((prev) => ({ ...prev, endpointPath: event.target.value }))
                }}
              />
            </Field>
            <Field>
              <FieldLabel>Request timeout (ms)</FieldLabel>
              <Input
                type="number"
                min="1"
                value={state.timeout}
                onChange={(event) => {
                  setState((prev) => ({ ...prev, timeout: event.target.value }))
                }}
              />
            </Field>
            {provider.auth_kind === 'sigv4' && (
              <Field>
                <FieldLabel>AWS signing region</FieldLabel>
                <Input
                  required
                  autoCapitalize="none"
                  autoCorrect="off"
                  spellCheck={false}
                  pattern={awsRegionPattern.source}
                  value={state.region}
                  onChange={(event) => {
                    setState((prev) => ({ ...prev, region: event.target.value }))
                  }}
                />
              </Field>
            )}
            {errorMessage && <p className="text-destructive text-sm">{errorMessage}</p>}
            <DialogFooter>
              <Button
                type="submit"
                disabled={
                  mutation.isPending ||
                  state.baseUrl.trim() === '' ||
                  (provider.auth_kind === 'sigv4' && !awsRegionPattern.test(state.region.trim()))
                }
                loading={mutation.isPending}
              >
                Save changes
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
