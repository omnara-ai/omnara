import { useDeleteIntegration, useDisconnectIntegration } from '@omnara/react'

export function useIntegrationActions(orgId: string, projectId: string) {
  const disconnect = useDisconnectIntegration(orgId, projectId)
  const remove = useDeleteIntegration(orgId, projectId)
  return { disconnect, remove, busy: disconnect.isPending || remove.isPending }
}
