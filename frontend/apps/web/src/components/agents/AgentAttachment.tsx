import { useDownloadAgentArtifact } from '@omnara/react'
import { useParams } from '@tanstack/react-router'
import { Paperclip } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Spinner } from '@/components/ui/spinner'
import { useActiveOrg } from '@/lib/use-active-org'

/**
 * A chat attachment indicator. The message only carries the artifact
 * reference, so the chip downloads the bytes on demand instead of rendering
 * the file inline.
 */
export function AgentAttachment({ artifactId }: { artifactId?: string }) {
  const { activeOrg } = useActiveOrg()
  const params = useParams({ strict: false })
  const download = useDownloadAgentArtifact(
    activeOrg.id,
    params.projectId ?? '',
    params.agentId ?? '',
  )

  async function save(artifactID: string) {
    const { artifact, content } = await download.mutateAsync(artifactID)
    const filename = artifact.filename?.trim()
    const url = URL.createObjectURL(content)
    try {
      const anchor = document.createElement('a')
      anchor.href = url
      anchor.download = filename == null || filename === '' ? 'attachment' : filename
      anchor.click()
    } finally {
      URL.revokeObjectURL(url)
    }
  }

  if (artifactId == null) {
    return (
      <span className="text-muted-foreground inline-flex items-center gap-1.5 text-xs">
        <Paperclip className="size-3.5" /> Attachment unavailable
      </span>
    )
  }
  return (
    <span className="inline-flex flex-col items-start gap-1">
      <Button
        type="button"
        variant="outline"
        size="sm"
        disabled={download.isPending}
        onClick={() => void save(artifactId).catch(() => undefined)}
      >
        {download.isPending ? <Spinner /> : <Paperclip className="size-3.5" />} Attachment
      </Button>
      {download.error != null && (
        <span className="text-destructive text-xs">Could not download the attachment.</span>
      )}
    </span>
  )
}
