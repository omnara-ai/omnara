import { describe, expect, it } from 'vitest'

import { shownArtifactFromToolPart } from '@/components/agents/shownArtifact'

type ToolPart = Parameters<typeof shownArtifactFromToolPart>[0]

function shownPart(contentType = 'image/png', outcome = 'succeeded'): ToolPart {
  return {
    type: 'dynamic-tool',
    toolType: 'built_in',
    toolName: 'show_artifact',
    toolCallId: 'call-1',
    state: 'output-available',
    input: { artifact_id: 'art_test' },
    output: {
      outcome,
      contentBlocks: [
        {
          type: 'structured_data',
          value: {
            artifact_id: 'art_test',
            filename: 'report.pdf',
            content_type: contentType,
            size_bytes: 2048,
          },
        },
      ],
    },
  } as ToolPart
}

describe('shownArtifactFromToolPart', () => {
  it('projects successful show_artifact metadata', () => {
    expect(shownArtifactFromToolPart(shownPart())).toEqual({
      artifactId: 'art_test',
      filename: 'report.pdf',
      contentType: 'image/png',
      sizeBytes: 2048,
    })
  })

  it('projects pdfs through the same artifact data', () => {
    expect(shownArtifactFromToolPart(shownPart('application/pdf'))?.contentType).toBe(
      'application/pdf',
    )
  })

  it('ignores failed and unrelated tool results', () => {
    expect(shownArtifactFromToolPart(shownPart('image/png', 'failed'))).toBeNull()
    expect(shownArtifactFromToolPart({ ...shownPart(), toolName: 'web_fetch' })).toBeNull()
    expect(shownArtifactFromToolPart({ ...shownPart(), toolType: 'custom' })).toBeNull()
  })
})
