import { useOmnaraClient } from '@omnara/react'
import { getArtifactContentOptions, getArtifactOptions } from '@omnara/sdk/tanstack'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useState } from 'react'

import { downloadBlob } from '@/components/agents/downloadBlob'
import { FileArchive } from '@/components/icons'
import { Button } from '@/components/ui/button'

const previewContentTypes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])

function formatByteSize(bytes: number) {
  if (bytes < 1024) return `${String(bytes)} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

export function ShownArtifactCard({
  artifactID,
  orgID,
  projectID,
  agentID,
}: {
  artifactID: string
  orgID: string
  projectID: string
  agentID: string
}) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const [previewFailed, setPreviewFailed] = useState(false)
  const path = { orgID, projectID, agentID, artifactID }
  const artifact = useQuery({
    ...getArtifactOptions({ client, path }),
    staleTime: Infinity,
  }).data
  const rawFilename = artifact?.filename?.trim()
  const filename = rawFilename == null || rawFilename === '' ? 'artifact' : rawFilename
  const contentType = artifact?.content_type ?? 'application/octet-stream'
  const previewable = previewContentTypes.has(contentType)
  const contentQuery = getArtifactContentOptions({ client, path, parseAs: 'blob' })
  const contentURL = client.buildUrl({
    url: '/orgs/{orgID}/projects/{projectID}/agents/{agentID}/artifacts/{artifactID}/content',
    path,
  })
  const download = useMutation({
    mutationFn: () => queryClient.fetchQuery({ ...contentQuery, staleTime: Infinity }),
  })

  async function downloadArtifact() {
    const content = await download.mutateAsync()
    downloadBlob(content, filename)
  }

  const showPreview = previewable && !previewFailed
  return (
    <div className="bg-card max-w-xl overflow-hidden rounded-lg border shadow-sm">
      {showPreview ? (
        <img
          src={contentURL}
          alt={filename}
          loading="lazy"
          className="bg-muted/30 max-h-[32rem] w-full object-contain"
          onError={() => {
            setPreviewFailed(true)
          }}
        />
      ) : (
        <div className="bg-muted/30 flex h-32 items-center justify-center">
          <FileArchive className="text-muted-foreground size-10" />
        </div>
      )}
      <div className="flex items-center gap-3 border-t px-3 py-2.5">
        <div className="min-w-0 flex-1">
          <p className="truncate text-sm font-medium">{filename}</p>
          <p className="text-muted-foreground truncate text-xs">
            {contentType}
            {artifact?.size_bytes == null ? '' : ` · ${formatByteSize(artifact.size_bytes)}`}
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={download.isPending}
          loading={download.isPending}
          onClick={() => {
            void downloadArtifact().catch(() => undefined)
          }}
        >
          {download.isError ? 'Retry' : 'Download'}
        </Button>
      </div>
    </div>
  )
}
