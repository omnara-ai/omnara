import { useCreateIntegrationApp, useIntegrationApp, useUpdateIntegrationApp } from '@omnara/react'
import type { UpdateIntegrationAppRequest } from '@omnara/sdk'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Spinner } from '@/components/ui/spinner'

import { IntegrationAppForm } from './IntegrationAppForm'

export function CreateIntegrationAppDialog({
  orgId,
  onClose,
}: {
  orgId: string
  onClose: () => void
}) {
  const mutation = useCreateIntegrationApp(orgId)
  const [credentialsPending, setCredentialsPending] = useState(false)
  const pending = mutation.isPending || credentialsPending
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose()
      }}
    >
      <DialogContent showCloseButton={!pending}>
        <DialogHeader>
          <DialogTitle>New app</DialogTitle>
          <DialogDescription>
            Register your GitHub, Discord, or Slack app for project connections.
          </DialogDescription>
        </DialogHeader>
        <IntegrationAppForm
          orgId={orgId}
          pending={pending}
          onCredentialsPendingChange={setCredentialsPending}
          onCancel={onClose}
          onSave={async ({ state: _state, ...body }) => {
            await mutation.mutateAsync(body)
            onClose()
          }}
        />
      </DialogContent>
    </Dialog>
  )
}

export function EditIntegrationAppDialog({
  orgId,
  appId,
  onClose,
}: {
  orgId: string
  appId: string
  onClose: () => void
}) {
  const query = useIntegrationApp(orgId, appId)
  const mutation = useUpdateIntegrationApp(orgId, appId)
  const [credentialsPending, setCredentialsPending] = useState(false)
  const pending = mutation.isPending || credentialsPending
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose()
      }}
    >
      <DialogContent showCloseButton={!pending}>
        <DialogHeader>
          <DialogTitle>Edit app</DialogTitle>
          <DialogDescription>
            Update the app name, configuration, credentials, or status.
          </DialogDescription>
        </DialogHeader>
        {query.isPending ? (
          <Spinner />
        ) : query.isError ? (
          <div role="alert">
            <p>Could not load app.</p>
            <Button
              variant="outline"
              onClick={() => {
                void query.refetch()
              }}
            >
              Retry
            </Button>
          </div>
        ) : (
          <IntegrationAppForm
            key={query.data.id}
            orgId={orgId}
            app={query.data}
            pending={pending}
            onCredentialsPendingChange={setCredentialsPending}
            onCancel={onClose}
            onSave={async ({ name, credential_secret_id, provider_config, state }) => {
              const body: UpdateIntegrationAppRequest = {}
              if (name !== query.data.name) body.name = name
              if (state !== query.data.state) body.state = state
              if (credential_secret_id !== (query.data.credential_secret_id ?? ''))
                body.credential_secret_id = credential_secret_id
              if (provider_config.client_id !== query.data.provider_config.client_id)
                body.provider_config = provider_config
              await mutation.mutateAsync(body)
              onClose()
            }}
          />
        )}
      </DialogContent>
    </Dialog>
  )
}
