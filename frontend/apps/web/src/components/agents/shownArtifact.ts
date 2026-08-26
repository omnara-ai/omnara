import type { OmnaraUIMessage } from '@omnara/react'

export interface ShownArtifact {
  artifactId: string
  filename: string
  contentType: string
  sizeBytes: number
}

type DynamicToolPart = Extract<OmnaraUIMessage['parts'][number], { type: 'dynamic-tool' }>

function unknownRecord(value: unknown): Record<string, unknown> | null {
  return typeof value === 'object' && value != null ? (value as Record<string, unknown>) : null
}

export function shownArtifactFromToolPart(part: DynamicToolPart): ShownArtifact | null {
  if (
    part.toolType !== 'built_in' ||
    part.toolName !== 'show_artifact' ||
    part.state !== 'output-available'
  ) {
    return null
  }
  const output = unknownRecord(part.output)
  if (output?.outcome !== 'succeeded' || !Array.isArray(output.contentBlocks)) {
    return null
  }
  const block = output.contentBlocks
    .map(unknownRecord)
    .find((candidate) => candidate?.type === 'structured_data')
  const record = unknownRecord(block?.value)
  if (
    record == null ||
    typeof record.artifact_id !== 'string' ||
    typeof record.filename !== 'string' ||
    typeof record.content_type !== 'string' ||
    typeof record.size_bytes !== 'number' ||
    !Number.isSafeInteger(record.size_bytes) ||
    record.size_bytes < 0
  ) {
    return null
  }
  return {
    artifactId: record.artifact_id,
    filename: record.filename,
    contentType: record.content_type,
    sizeBytes: record.size_bytes,
  }
}
