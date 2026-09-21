import { useDeleteProjectApp, useDisconnectProjectApp } from '@omnara/react'

export function useProjectAppActions(orgId: string, projectId: string) {
  const disconnect = useDisconnectProjectApp(orgId, projectId)
  const remove = useDeleteProjectApp(orgId, projectId)
  return { disconnect, remove, busy: disconnect.isPending || remove.isPending }
}
