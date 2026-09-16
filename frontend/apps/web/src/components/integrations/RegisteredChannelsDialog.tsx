import { useRegisterChannel, useRegisteredChannels } from '@omnara/react'
import type { IntegrationInstall, RegisterManagedChannelRequest } from '@omnara/sdk'
import { type SyntheticEvent, useState } from 'react'

import { DataTable } from '@/components/data-table/DataTable'
import { DetailList } from '@/components/data-table/DetailList'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Field, FieldDescription, FieldLabel } from '@/components/ui/field'
import { Input } from '@/components/ui/input'
import { usePagedQuery } from '@/hooks/use-paged-query'
import { errorMessage } from '@/lib/submit-status'

export function RegisteredChannelsDialog({
  orgId,
  projectId,
  install,
  canManage,
  onClose,
}: {
  orgId: string
  projectId: string
  install: IntegrationInstall
  canManage: boolean
  onClose: () => void
}) {
  const query = useRegisteredChannels(orgId, projectId, install.id)
  const paged = usePagedQuery(query, install.id)
  const register = useRegisterChannel(orgId, projectId, install.id)
  const [address, setAddress] = useState('')
  const [error, setError] = useState('')
  const [registeredName, setRegisteredName] = useState('')
  const githubRepository =
    install.provider === 'github' && /^[1-9][0-9]*$/.test(install.provider_account_ref ?? '')
      ? install.provider_account_ref
      : undefined
  const canRegister =
    canManage &&
    install.integration_kind === 'managed' &&
    install.state === 'active' &&
    (install.provider === 'slack' || install.provider === 'discord' || Boolean(githubRepository))
  const label = install.provider === 'github' ? 'Pull request number' : 'Channel ID'
  async function submit(event: SyntheticEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!canRegister || !address.trim() || register.isPending) return
    setError('')
    setRegisteredName('')
    const value = address.trim()
    if (githubRepository && (!/^[1-9][0-9]*$/.test(value) || Number(value) > 2_147_483_647)) {
      setError('Enter a valid pull request number.')
      return
    }
    try {
      const body: RegisterManagedChannelRequest = {
        source: 'managed',
        provider_ref: githubRepository ? `repo:${githubRepository}:pr:${value}` : value,
      }
      if (githubRepository) body.provider_ref_kind = 'pr'
      const channel = await register.mutateAsync(body)
      setAddress('')
      setRegisteredName(channel.name || channel.provider_ref)
    } catch (err) {
      setError(errorMessage(err, 'Could not add channel'))
    }
  }
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open && !register.isPending) onClose()
      }}
    >
      <DialogContent className="sm:max-w-2xl" showCloseButton={!register.isPending}>
        <DialogHeader>
          <DialogTitle>Channels</DialogTitle>
          <DialogDescription>
            {install.display_name || (install.provider_account_ref ?? 'Connection')} · Add channels
            for agents to use. Agent access is configured separately.
          </DialogDescription>
        </DialogHeader>
        <DataTable
          columns={[
            { id: 'name', header: 'Name', cell: (channel) => channel.name || channel.provider_ref },
            {
              id: 'address',
              header: 'Address',
              cell: (channel) => <span className="break-all">{channel.provider_ref}</span>,
            },
          ]}
          data={paged.rows}
          pagination={paged.pagination}
          getRowId={(channel) => channel.channel_id}
          rowExpanded={(channel) => (
            <DetailList
              items={[
                { label: 'Channel ID', value: channel.channel_id, mono: true },
                {
                  label: 'Parent channel',
                  value: channel.parent_channel_id ?? 'None',
                  mono: Boolean(channel.parent_channel_id),
                },
              ]}
            />
          )}
          isPending={query.isPending}
          isError={query.isError}
          onRetry={() => {
            void query.refetch()
          }}
          emptyMessage="No channels added yet."
        />
        {canManage && install.provider === 'github' && !githubRepository && (
          <p className="text-muted-foreground text-sm">
            Repository information is unavailable for this connection.
          </p>
        )}
        {canRegister && (
          <form onSubmit={(event) => void submit(event)}>
            <fieldset disabled={register.isPending} className="grid gap-3">
              <Field>
                <FieldLabel htmlFor="channel-address">{label}</FieldLabel>
                <Input
                  id="channel-address"
                  required
                  inputMode={githubRepository ? 'numeric' : undefined}
                  value={address}
                  maxLength={2048}
                  onChange={(event) => {
                    setAddress(event.target.value)
                  }}
                />
                <FieldDescription>
                  {install.provider === 'github'
                    ? 'Use a pull request in the connected repository.'
                    : install.provider === 'slack'
                      ? 'For a thread, enter channel ID:timestamp.'
                      : 'Use a channel or thread in the connected server.'}
                </FieldDescription>
              </Field>
              {error && (
                <p role="alert" className="text-destructive text-sm">
                  {error}
                </p>
              )}
              {registeredName && (
                <p role="status" className="text-sm">
                  Added {registeredName}.
                </p>
              )}
              <Button
                type="submit"
                className="justify-self-end"
                loading={register.isPending}
                disabled={register.isPending || !address.trim()}
              >
                Add channel
              </Button>
            </fieldset>
          </form>
        )}
      </DialogContent>
    </Dialog>
  )
}
