import { describe, expect, it } from 'vitest'

import { memoryPreview } from './memory-files'

describe('memory file previews', () => {
  it('preserves empty text, Unicode, line endings, and a UTF-8 BOM', () => {
    for (const text of ['', 'hello 🌎\r\n', '\ufeffnotes']) {
      expect(memoryPreview(new TextEncoder().encode(text))).toEqual({ text, type: null })
    }
  })
  it('does not decode binary files as editable text', () => {
    expect(memoryPreview(new Uint8Array([0xff, 0xfe]))).toEqual({ text: null, type: null })
    expect(memoryPreview(new Uint8Array([65, 0, 66]))).toEqual({ text: null, type: null })
  })
  it('uses content signatures for supported previews and treats HTML and SVG as text', () => {
    expect(
      memoryPreview(new Uint8Array([0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a])).type,
    ).toBe('image/png')
    expect(memoryPreview(new TextEncoder().encode('%PDF-1.7')).type).toBe('application/pdf')
    for (const text of ['<script>alert(1)</script>', '<svg onload="alert(1)"></svg>']) {
      expect(memoryPreview(new TextEncoder().encode(text))).toEqual({ text, type: null })
    }
  })
})
