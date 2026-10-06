import { describe, expect, it } from 'vitest'

import {
  zCreateSkillRequest,
  zDownloadDaemonFileResponse,
  zDownloadMemoryFileResponse,
  zGetArtifactContentResponse,
  zGetDaemonSkillArchiveResponse,
  zUpdateSkillRequest,
  zUploadDaemonFileBody,
  zWriteMemoryFileBody,
} from './generated/zod.gen'

describe('generated binary schemas', () => {
  it.each([
    ['memory upload', zWriteMemoryFileBody],
    ['memory download', zDownloadMemoryFileResponse],
    ['artifact content', zGetArtifactContentResponse],
    ['daemon file upload', zUploadDaemonFileBody],
    ['daemon file download', zDownloadDaemonFileResponse],
    ['skill upload', zCreateSkillRequest.shape.archive],
    ['skill download', zGetDaemonSkillArchiveResponse],
  ] as const)('%s accepts Blob and File values and rejects strings', (_, schema) => {
    for (const value of [new Blob(['content']), new File(['content'], 'file.txt')]) {
      expect(schema.parse(value)).toBe(value)
    }
    expect(schema.safeParse('content').success).toBe(false)
  })

  it('preserves skill archive and text update alternatives', () => {
    expect(zUpdateSkillRequest.safeParse({ archive: new Blob(['archive']) }).success).toBe(true)
    expect(zUpdateSkillRequest.safeParse({ skill_md: 'text' }).success).toBe(true)
    expect(zUpdateSkillRequest.safeParse({ archive: 'archive' }).success).toBe(false)
  })
})
