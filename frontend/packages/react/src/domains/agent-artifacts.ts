import { sdk } from '@omnara/sdk'
import { useMutation } from '@tanstack/react-query'

import { useOmnaraClient } from '../omnara-client'

/** Fetches an attachment's metadata and bytes for a client-side download. */
export function useDownloadAgentArtifact(orgID: string, projectID: string, agentID: string) {
  const client = useOmnaraClient()
  return useMutation({
    mutationFn: async (artifactID: string) => {
      const path = { orgID, projectID, agentID, artifactID }
      const [{ data: artifact }, { data: content }] = await Promise.all([
        sdk.getArtifact({ client, path }),
        sdk.getArtifactContent({ client, path }),
      ])
      return { artifact, content }
    },
  })
}
