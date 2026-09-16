import { useRefreshIntegrationConnections } from '@omnara/react'
import { useEffect } from 'react'

import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

import { GitHubConnectionDialog } from './GitHubConnectionDialog'
import { integrationSetupError, type IntegrationSetupSearch } from './integrationOAuth'

export function IntegrationConnectionOutcome({
  orgId,
  projectId,
  canManage,
  search,
  onClose,
  onConnected,
}: {
  orgId: string
  projectId: string
  canManage: boolean
  search: IntegrationSetupSearch
  onClose: () => void
  onConnected: () => void
}) {
  const refresh = useRefreshIntegrationConnections(orgId, projectId)
  const success = search.integration_oauth === 'success' && !search.integration_oauth_error
  useEffect(() => {
    if (success) void refresh()
  }, [success, refresh])
  const selecting = search.integration_oauth === 'select_repository'
  if (!success && !selecting && !search.integration_oauth_error) return null
  if (
    selecting &&
    !search.integration_oauth_error &&
    canManage &&
    search.integration_oauth_flow_id
  ) {
    return (
      <GitHubConnectionDialog
        key={search.integration_oauth_flow_id}
        orgId={orgId}
        projectId={projectId}
        flowId={search.integration_oauth_flow_id}
        onClose={onClose}
        onConnected={onConnected}
      />
    )
  }
  const description = success
    ? 'The app is connected to this project.'
    : search.integration_oauth_error
      ? integrationSetupError(search.integration_oauth_error)
      : !canManage
        ? 'Project management access is required to finish this connection.'
        : 'This setup link is incomplete or invalid. Close it and connect the app again.'
  return (
    <Dialog
      open
      onOpenChange={(open) => {
        if (!open) onClose()
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{success ? 'App connected' : 'Connection not completed'}</DialogTitle>
          <DialogDescription>{description}</DialogDescription>
        </DialogHeader>
        <DialogFooter>
          <Button onClick={onClose}>Got it</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
