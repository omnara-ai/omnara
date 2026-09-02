import { fetchWebConfig } from '@omnara/sdk/browser'
import { useQuery } from '@tanstack/react-query'

import { canManageOrg } from '@/lib/permissions'
import { useActiveOrg } from '@/lib/use-active-org'

const webConfigQuery = { queryKey: ['web-config'] as const, queryFn: fetchWebConfig }

export function useWebConfig() {
  const { activeOrg } = useActiveOrg()
  const query = useQuery(webConfigQuery)
  const billingURL = query.data?.billingURL

  return {
    ...query,
    data:
      query.data == null
        ? undefined
        : {
            ...query.data,
            billingHref:
              billingURL && canManageOrg(activeOrg.role)
                ? `${billingURL}?org=${activeOrg.id}`
                : undefined,
          },
  }
}
