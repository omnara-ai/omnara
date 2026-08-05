import { getToolCatalogOptions } from '@omnara/sdk/tanstack'
import { useQuery } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

export function useToolCatalog() {
  const client = useOmnaraClient()
  return useQuery(getToolCatalogOptions({ client }))
}
