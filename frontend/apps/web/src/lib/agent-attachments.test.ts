import type { AgentConfigModel, InlineMediaContentBlock } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import { selectAgentAttachment } from './agent-attachments'

function model(overrides: Partial<AgentConfigModel> = {}): AgentConfigModel {
  return {
    api_format: 'openai-responses',
    api_variant: 'openai',
    input_modalities: [],
    ...overrides,
  } as AgentConfigModel
}

const canonicalMediaTypes = {
  'image/png': true,
  'image/jpeg': true,
  'image/gif': true,
  'image/webp': true,
  'application/pdf': true,
  'text/plain': true,
  'text/markdown': true,
  'text/csv': true,
  'text/tab-separated-values': true,
  'text/x-iif': true,
  'application/msword': true,
  'application/rtf': true,
  'application/vnd.oasis.opendocument.text': true,
  'application/vnd.apple.pages': true,
  'application/vnd.apple.keynote': true,
  'application/vnd.apple.iwork': true,
  'application/vnd.ms-powerpoint': true,
  'application/vnd.ms-excel': true,
  'application/vnd.openxmlformats-officedocument.wordprocessingml.document': true,
  'application/vnd.openxmlformats-officedocument.presentationml.presentation': true,
  'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet': true,
} satisfies Record<InlineMediaContentBlock['media_type'], true>

describe('selectAgentAttachment', () => {
  it('accepts every canonical attachment media type', async () => {
    for (const mediaType of Object.keys(canonicalMediaTypes)) {
      const content = mediaType.startsWith('text/') ? 'text' : new Uint8Array([0xff])
      const selected = await selectAgentAttachment(
        new File([content], 'attachment', { type: mediaType }),
        model(),
      )
      expect(selected.mediaType).toBe(mediaType)
    }
  })

  it('accepts arbitrary valid UTF-8 files as plain text', async () => {
    const selected = await selectAgentAttachment(
      new File(['fn main() {}'], 'main.rs', { type: 'application/octet-stream' }),
      model({ input_modalities: ['text'] }),
    )
    expect(selected).toMatchObject({ kind: 'document', mediaType: 'text/plain' })
  })

  it('counts the filename limit in Unicode characters', async () => {
    const filename = `${'界'.repeat(100)}.txt`
    const selected = await selectAgentAttachment(new File(['text'], filename), model())

    expect(selected.file.name).toBe(filename)
  })

  it('accepts a canonical media type without a filename extension', async () => {
    const selected = await selectAgentAttachment(
      new File([new Uint8Array([0xff])], 'report', { type: 'application/pdf' }),
      model({ input_modalities: ['text', 'file'] }),
    )
    expect(selected.mediaType).toBe('application/pdf')
  })

  it('ignores inherited extension-map properties', async () => {
    for (const filename of ['notes.constructor', 'notes.__proto__']) {
      const selected = await selectAgentAttachment(
        new File(['text'], filename, { type: 'text/plain' }),
        model(),
      )
      expect(selected.mediaType).toBe('text/plain')
    }
  })

  it('reads text attachment bytes once', async () => {
    const file = new File(['text'], 'notes.txt', { type: 'text/plain' })
    const arrayBuffer = vi.spyOn(file, 'arrayBuffer')

    await selectAgentAttachment(file, model())

    expect(arrayBuffer).toHaveBeenCalledOnce()
  })

  it.each([
    ['photo', 'image/jpg', 'image/jpeg'],
    ['data', 'application/x-iif', 'text/x-iif'],
  ] as const)('normalizes the %s media type alias', async (filename, alias, mediaType) => {
    const selected = await selectAgentAttachment(
      new File([mediaType.startsWith('text/') ? 'text' : new Uint8Array([0xff])], filename, {
        type: alias,
      }),
      model({ input_modalities: ['text', 'image'] }),
    )
    expect(selected.mediaType).toBe(mediaType)
  })

  it('rejects unknown binary files', async () => {
    await expect(
      selectAgentAttachment(
        new File([new Uint8Array([0xff])], 'archive.bin', {
          type: 'application/octet-stream',
        }),
        model(),
      ),
    ).rejects.toThrow('not a supported image, document, or UTF-8 text file')
  })

  it.each([
    ['report.doc', 'application/msword'],
    ['report.dot', 'application/msword'],
    ['report.docx', 'application/vnd.openxmlformats-officedocument.wordprocessingml.document'],
    ['report.rtf', 'application/rtf'],
    ['report.odt', 'application/vnd.oasis.opendocument.text'],
    ['report.pages', 'application/vnd.apple.pages'],
    ['report.key', 'application/vnd.apple.keynote'],
    ['report.iif', 'text/x-iif'],
    ['report.iwork', 'application/vnd.apple.iwork'],
    ['report.pot', 'application/vnd.ms-powerpoint'],
    ['report.ppa', 'application/vnd.ms-powerpoint'],
    ['report.pps', 'application/vnd.ms-powerpoint'],
    ['report.ppt', 'application/vnd.ms-powerpoint'],
    ['report.pwz', 'application/vnd.ms-powerpoint'],
    ['report.wiz', 'application/vnd.ms-powerpoint'],
    ['report.pptx', 'application/vnd.openxmlformats-officedocument.presentationml.presentation'],
    ['report.xla', 'application/vnd.ms-excel'],
    ['report.xlb', 'application/vnd.ms-excel'],
    ['report.xlc', 'application/vnd.ms-excel'],
    ['report.xlm', 'application/vnd.ms-excel'],
    ['report.xls', 'application/vnd.ms-excel'],
    ['report.xlt', 'application/vnd.ms-excel'],
    ['report.xlw', 'application/vnd.ms-excel'],
    ['report.xlsx', 'application/vnd.openxmlformats-officedocument.spreadsheetml.sheet'],
  ] as const)('accepts %s for file-capable OpenAI models', async (filename, mediaType) => {
    const selected = await selectAgentAttachment(
      new File([mediaType.startsWith('text/') ? 'text' : new Uint8Array([0xff])], filename),
      model({ input_modalities: ['text', 'file'] }),
    )
    expect(selected.mediaType).toBe(mediaType)
  })

  it('accepts fallback documents for Anthropic models', async () => {
    const selected = await selectAgentAttachment(
      new File([new Uint8Array([0xff])], 'report.docx'),
      model({ api_format: 'anthropic-messages', input_modalities: ['text'] }),
    )
    expect(selected.mediaType).toBe(
      'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    )
  })

  it('accepts PDFs and fallback documents for OpenRouter models without native file input', async () => {
    const selected = await selectAgentAttachment(
      new File([new Uint8Array([0xff])], 'report.pdf'),
      model({
        api_format: 'openai-chat-completions',
        api_variant: 'openrouter',
        input_modalities: ['text'],
      }),
    )
    expect(selected.mediaType).toBe('application/pdf')

    const document = await selectAgentAttachment(
      new File([new Uint8Array([0xff])], 'report.docx'),
      model({
        api_format: 'openai-chat-completions',
        api_variant: 'openrouter',
        input_modalities: ['text'],
      }),
    )
    expect(document.mediaType).toBe(
      'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    )
  })

  it('accepts PDFs, UTF-8 text, and fallback documents for direct Chat Completions models', async () => {
    const pdf = await selectAgentAttachment(
      new File([new Uint8Array([0xff])], 'report.pdf'),
      model({ api_format: 'openai-chat-completions', input_modalities: ['text', 'file'] }),
    )
    expect(pdf.mediaType).toBe('application/pdf')

    await expect(
      selectAgentAttachment(
        new File([new Uint8Array([0xff])], 'report.pdf'),
        model({ api_format: 'openai-chat-completions', input_modalities: ['text'] }),
      ),
    ).rejects.toThrow("not supported by this agent's model")

    const document = await selectAgentAttachment(
      new File([new Uint8Array([0xff])], 'report.docx'),
      model({ api_format: 'openai-chat-completions', input_modalities: ['text'] }),
    )
    expect(document.mediaType).toBe(
      'application/vnd.openxmlformats-officedocument.wordprocessingml.document',
    )

    const selected = await selectAgentAttachment(
      new File(['fn main() {}'], 'main.rs'),
      model({ api_format: 'openai-chat-completions', input_modalities: ['text'] }),
    )
    expect(selected.mediaType).toBe('text/plain')
  })

  it('accepts textual documents for text-only Responses and Anthropic models', async () => {
    for (const apiFormat of ['openai-responses', 'anthropic-messages'] as const) {
      const selected = await selectAgentAttachment(
        new File(['a,b\n1,2'], 'data.csv', { type: 'text/csv' }),
        model({ api_format: apiFormat, input_modalities: ['text'] }),
      )
      expect(selected.mediaType).toBe('text/csv')
    }
  })

  it('honors explicit image modality restrictions', async () => {
    await expect(
      selectAgentAttachment(
        new File([new Uint8Array([0xff])], 'photo.png'),
        model({ input_modalities: ['text'] }),
      ),
    ).rejects.toThrow("not supported by this agent's model")
  })
})
