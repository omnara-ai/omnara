import { useCreateMemoryStore, useDeleteMemoryStore, useUpdateMemoryStore } from '@omnara/react'
import type { MemoryStore } from '@omnara/sdk'
import { useNavigate } from '@tanstack/react-router'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { CheckboxField, Field, FieldGroup, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { errorMessage } from '@/lib/submit-status'

export function MemoryStoreDialog({
  orgId,
  projectId,
  store,
  onClose,
}: {
  orgId: string
  projectId: string
  store?: MemoryStore
  onClose: () => void
}) {
  const [name, setName] = useState(store?.name ?? '')
  const [description, setDescription] = useState(store?.description ?? '')
  const [readOnly, setReadOnly] = useState(store?.read_only ?? false)
  const create = useCreateMemoryStore(orgId, projectId)
  const scope = { orgID: orgId, projectID: projectId, memoryStoreID: store?.id ?? '' }
  const update = useUpdateMemoryStore(scope)
  const remove = useDeleteMemoryStore(scope)
  const navigate = useNavigate()
  const saveMutation = store ? update : create
  const pending = saveMutation.isPending || remove.isPending
  const error = saveMutation.error ?? remove.error
  function save() {
    remove.reset()
    if (store) update.mutate({ description, read_only: readOnly }, { onSuccess: onClose })
    else create.mutate({ name, description, read_only: readOnly }, { onSuccess: onClose })
  }

  function deleteStore() {
    if (!store || !window.confirm(`Delete ${store.name} and all its files?`)) return
    update.reset()
    remove.mutate(undefined, {
      onSuccess: () =>
        void navigate({
          to: '/projects/$projectId/memory',
          params: { projectId },
          ignoreBlocker: true,
        }),
    })
  }

  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !pending) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{store ? 'Store settings' : 'Create memory store'}</DialogTitle>
          <DialogDescription>
            {store
              ? 'Manage how this store is used.'
              : 'Share files and knowledge across agents in this project.'}
          </DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(event) => {
            event.preventDefault()
            save()
          }}
        >
          <FieldGroup>
            <Field>
              <FieldLabel htmlFor="memory-store-name">Name</FieldLabel>
              <Input
                id="memory-store-name"
                value={name}
                onChange={(event) => {
                  setName(event.target.value)
                }}
                required
                pattern="[a-z0-9]+(-[a-z0-9]+)*"
                maxLength={64}
                disabled={store !== undefined || pending}
                placeholder="team-notes"
              />
            </Field>
            <Field>
              <FieldLabel htmlFor="memory-store-description">Description</FieldLabel>
              <Textarea
                id="memory-store-description"
                value={description}
                onChange={(event) => {
                  setDescription(event.target.value)
                }}
                disabled={pending}
                placeholder="What should agents keep here?"
              />
            </Field>
            <CheckboxField
              label="Read-only for agents"
              checked={readOnly}
              onChange={(event) => {
                setReadOnly(event.target.checked)
              }}
              disabled={pending}
            />
            {error && (
              <p role="alert" className="text-destructive text-sm">
                {errorMessage(
                  error,
                  remove.isError ? 'Could not delete memory store' : 'Could not save memory store',
                )}
              </p>
            )}
            <DialogFooter>
              {store && (
                <Button
                  variant="ghost"
                  className="text-destructive hover:text-destructive"
                  onClick={deleteStore}
                  loading={remove.isPending}
                  disabled={pending}
                >
                  Delete store
                </Button>
              )}
              <Button type="submit" loading={saveMutation.isPending} disabled={pending}>
                {store ? 'Save changes' : 'Create store'}
              </Button>
            </DialogFooter>
          </FieldGroup>
        </form>
      </DialogContent>
    </Dialog>
  )
}
