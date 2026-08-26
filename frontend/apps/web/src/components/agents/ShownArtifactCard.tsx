import { useOmnaraClient } from '@omnara/react'
import { getArtifactContentOptions } from '@omnara/sdk/tanstack'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useState } from 'react'

import { downloadBlob } from '@/components/agents/downloadBlob'
import type { ShownArtifact } from '@/components/agents/shownArtifact'
import { FileArchive } from '@/components/icons'
import { Button } from '@/components/ui/button'

const previewContentTypes = new Set(['image/png', 'image/jpeg', 'image/gif', 'image/webp'])

function formatByteSize(bytes: number) {
  if (bytes < 1024) return `${String(bytes)} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

function BlobImage({
  content,
  filename,
  onError,
}: {
  content: Blob
  filename: string
  onError: () => void
}) {
  const [preview, setPreview] = useState<{ content: Blob; url: string }>()
  useEffect(() => {
    const url = URL.createObjectURL(content)
    setPreview({ content, url })
    return () => {
      URL.revokeObjectURL(url)
    }
  }, [content])
  if (preview?.content !== content) return null
  return (
    <img
      src={preview.url}
      alt={filename}
      className="bg-muted/30 max-h-[32rem] w-full object-contain"
      onError={onError}
    />
  )
}

export function ShownArtifactCard({
  artifact,
  orgID,
  projectID,
  agentID,
}: {
  artifact: ShownArtifact
  orgID: string
  projectID: string
  agentID: string
}) {
  const client = useOmnaraClient()
  const queryClient = useQueryClient()
  const previewable = previewContentTypes.has(artifact.contentType)
  const [previewFailed, setPreviewFailed] = useState(false)
  const path = { orgID, projectID, agentID, artifactID: artifact.artifactId }
  const contentQuery = getArtifactContentOptions({ client, path, parseAs: 'blob' })
  const preview = useQuery({
    ...contentQuery,
    enabled: previewable,
    staleTime: Infinity,
  })
  const download = useMutation({
    mutationFn: () => queryClient.fetchQuery({ ...contentQuery, staleTime: Infinity }),
  })

  async function downloadArtifact() {
    const content = await download.mutateAsync()
    downloadBlob(content, artifact.filename)
  }

  const showPreview = previewable && !previewFailed && preview.data != null
  return (
    <div className="bg-card max-w-xl overflow-hidden rounded-lg border shadow-sm">
      {showPreview ? (
        <BlobImage
          content={preview.data}
          filename={artifact.filename}
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
          <p className="truncate text-sm font-medium">{artifact.filename}</p>
          <p className="text-muted-foreground truncate text-xs">
            {artifact.contentType} · {formatByteSize(artifact.sizeBytes)}
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
