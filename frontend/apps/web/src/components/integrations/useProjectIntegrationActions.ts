import { useDeleteProjectIntegration, useDisconnectProjectIntegration } from '@omnara/react'

export function useProjectIntegrationActions(orgId: string, projectId: string) {
  const disconnect = useDisconnectProjectIntegration(orgId, projectId)
  const remove = useDeleteProjectIntegration(orgId, projectId)
  return { disconnect, remove, busy: disconnect.isPending || remove.isPending }
}
