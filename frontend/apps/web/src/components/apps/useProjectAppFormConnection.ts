import { useIntegrationConnection, useIntegrationConnections } from '@omnara/react'
import type { IntegrationConnection, ProfileAppProvider, ProjectApp } from '@omnara/sdk'
import { useState } from 'react'

import { useInfiniteQueryItems } from '@/hooks/use-infinite-query-items'

/** Resolve explicit selections independently of the current page of available connections. */
export function useProjectAppFormConnection({
  orgId,
  projectId,
  provider,
  app,
  initialConnectionId,
  savedConnection,
}: {
  orgId: string
  projectId: string
  provider: ProfileAppProvider
  app?: ProjectApp
  initialConnectionId?: string
  savedConnection?: IntegrationConnection
}) {
  const connectionsQuery = useIntegrationConnections(orgId, projectId, { enabled: !app })
  const connections = useInfiniteQueryItems(connectionsQuery).filter(
    (connection) => connection.provider === provider && connection.state === 'active',
  )
  const [selection, setSelection] = useState(
    app?.settings.resource.connection ?? initialConnectionId ?? (provider === 'slack' ? '' : 'new'),
  )
  const listedConnection = connections.find((connection) => connection.id === selection)
  const connectionQuery = useIntegrationConnection(
    orgId,
    projectId,
    listedConnection || selection === 'new' ? '' : selection,
  )
  return {
    ...resolveConnectionSelection(
      provider,
      app,
      selection,
      connections,
      listedConnection,
      connectionQuery.data,
      savedConnection,
    ),
    selection,
    setSelection,
    connectionsQuery,
    connectionQuery,
  }
}

function resolveConnectionSelection(
  provider: ProfileAppProvider,
  app: ProjectApp | undefined,
  selection: string,
  connections: IntegrationConnection[],
  listedConnection?: IntegrationConnection,
  fetchedConnection?: IntegrationConnection,
  savedConnection?: IntegrationConnection,
) {
  const connection =
    savedConnection ??
    listedConnection ??
    (fetchedConnection?.provider === provider ? fetchedConnection : undefined)
  const creating = !app && selection === 'new' && !savedConnection
  const choices =
    connection && !connections.some((item) => item.id === connection.id)
      ? [...connections, connection]
      : connections
  return {
    connection,
    creating,
    connections: choices,
    providerMismatch: Boolean(fetchedConnection && fetchedConnection.provider !== provider),
    settingsReady: Boolean(app) || provider !== 'slack' || connection?.state === 'active',
    invalidConnection: !app && !creating && connection?.state !== 'active',
  }
}
