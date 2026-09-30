import { useQuery } from '@tanstack/react-query'

import { webConfigQuery } from '@/lib/web-config'

export function useIntegrationSetupURLs() {
  const { data, isError } = useQuery(webConfigQuery)
  const publicURL = data?.publicURL?.replace(/\/$/, '')
  const apiURL = data?.apiURL ?? publicURL
  return {
    publicURL,
    apiOrigin: apiURL ? new URL(apiURL).origin : undefined,
    unavailable: isError || data != null,
  }
}
