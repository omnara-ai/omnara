import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterAll, describe, expect, it } from 'vitest'

import { renderConfigAttachment } from './config-attachment.ts'
import { CliInputError } from './output.ts'

const dir = mkdtempSync(join(tmpdir(), 'omnara-config-attachment-'))
afterAll(() => {
  rmSync(dir, { recursive: true, force: true })
})

function writeTemp(name: string, content: string): string {
  const filePath = join(dir, name)
  writeFileSync(filePath, content)
  return filePath
}

describe('renderConfigAttachment', () => {
  it('reads a yaml file', () => {
    const file = writeTemp('agent.yaml', 'instruction: hi\n')
    expect(renderConfigAttachment({ file })).toEqual({
      source: 'instruction: hi\n',
      source_format: 'yaml',
    })
  })

  it('reads a json file', () => {
    const file = writeTemp('agent.json', '{"instruction":"hi"}\n')
    expect(renderConfigAttachment({ file })).toEqual({
      source: '{"instruction":"hi"}\n',
      source_format: 'json',
    })
  })

  it('rejects a file with an unknown extension', () => {
    expect(() => renderConfigAttachment({ file: join(dir, 'agent.toml') })).toThrow(CliInputError)
  })

  it('rejects a missing file', () => {
    expect(() => renderConfigAttachment({ file: join(dir, 'absent.yaml') })).toThrow(CliInputError)
  })

  it('detects inline json', () => {
    expect(renderConfigAttachment({ source: '{"instruction": "hi"}' })).toEqual({
      source: '{"instruction": "hi"}',
      source_format: 'json',
    })
  })

  it('detects inline yaml', () => {
    expect(renderConfigAttachment({ source: 'instruction: hi' })).toEqual({
      source: 'instruction: hi',
      source_format: 'yaml',
    })
  })

  it('rejects an empty attachment', () => {
    expect(() => renderConfigAttachment({})).toThrow(CliInputError)
  })
})
