import { useQuery } from '@tanstack/react-query'

import { webConfigQuery } from '@/lib/web-config'

export function useIntegrationSetupURLs() {
  const { data, isError } = useQuery(webConfigQuery)
  const publicURL = data?.publicURL?.replace(/\/$/, '')
  return {
    publicURL,
    unavailable: isError || data != null,
  }
}
