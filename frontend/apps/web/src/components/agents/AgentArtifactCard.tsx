import { useOmnaraClient } from '@omnara/react'
import { getArtifactOptions } from '@omnara/sdk/tanstack'
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'

import { Download, FileArchive } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { attachmentSize } from '@/lib/agent-attachments'

const previewContentTypes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])

function displayFilename(primary: string | undefined, fallback: string | undefined): string {
  const preferred = primary?.trim()
  if (preferred != null && preferred !== '') return preferred
  const secondary = fallback?.trim()
  return secondary == null || secondary === '' ? 'attachment' : secondary
}

export function AgentArtifactCard({
  artifactID,
  orgID,
  projectID,
  agentID,
  data,
  mediaType,
  filename,
  sizeBytes,
}: {
  artifactID?: string
  orgID: string
  projectID: string
  agentID: string
  data?: string
  mediaType?: string
  filename?: string
  sizeBytes?: number
}) {
  const client = useOmnaraClient()
  const [previewFailed, setPreviewFailed] = useState(false)
  const path = { orgID, projectID, agentID, artifactID: artifactID ?? '' }
  const artifactQuery = useQuery({
    ...getArtifactOptions({ client, path }),
    enabled: artifactID != null,
    staleTime: Infinity,
  })
  const artifact = artifactQuery.data
  const resolvedFilename = displayFilename(artifact?.filename, filename)
  const resolvedMediaType = artifact?.content_type ?? mediaType ?? 'application/octet-stream'
  const resolvedSize = artifact?.size_bytes ?? sizeBytes
  const previewable = previewContentTypes.has(resolvedMediaType)
  const contentURL =
    artifactID == null
      ? undefined
      : client.buildUrl({
          url: '/api/v1/orgs/{orgID}/projects/{projectID}/agents/{agentID}/artifacts/{artifactID}/content',
          path,
        })
  const inlineURL = data == null ? undefined : `data:${resolvedMediaType};base64,${data}`
  const url = contentURL ?? inlineURL
  const showPreview = previewable && url != null && !previewFailed

  return (
    <div
      data-slot="attachment"
      className="bg-card max-w-xl overflow-hidden rounded-lg border shadow-sm"
    >
      {showPreview ? (
        <img
          src={url}
          alt={resolvedFilename}
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
          <p className="truncate text-sm font-medium">{resolvedFilename}</p>
          <p className="text-muted-foreground truncate text-xs">
            {resolvedMediaType}
            {resolvedSize == null ? '' : ` · ${attachmentSize(resolvedSize)}`}
          </p>
        </div>
        {url == null ? (
          <Button type="button" variant="outline" size="sm" disabled>
            Download
          </Button>
        ) : (
          <Button asChild variant="outline" size="sm">
            <a href={url} download={resolvedFilename}>
              <Download className="size-3.5" /> Download
            </a>
          </Button>
        )}
      </div>
      {artifactQuery.isError && (
        <p className="text-destructive border-t px-3 py-2 text-xs">
          Could not load the attachment.
        </p>
      )}
    </div>
  )
}
