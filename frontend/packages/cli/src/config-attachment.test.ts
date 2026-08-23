import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { afterAll, describe, expect, it } from 'vitest'

import { renderConfigSource } from './config-attachment.ts'
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

describe('renderConfigSource', () => {
  it('reads a yaml file', () => {
    const file = writeTemp('agent.yaml', 'instruction: hi\n')
    expect(renderConfigSource({ file })).toEqual({
      source: 'instruction: hi\n',
      source_format: 'yaml',
    })
  })

  it('reads a json file', () => {
    const file = writeTemp('agent.json', '{"instruction":"hi"}\n')
    expect(renderConfigSource({ file })).toEqual({
      source: '{"instruction":"hi"}\n',
      source_format: 'json',
    })
  })

  it('rejects a file with an unknown extension', () => {
    expect(() => renderConfigSource({ file: join(dir, 'agent.toml') })).toThrow(CliInputError)
  })

  it('rejects a missing file', () => {
    expect(() => renderConfigSource({ file: join(dir, 'absent.yaml') })).toThrow(CliInputError)
  })

  it('detects inline json', () => {
    expect(renderConfigSource({ source: '{"instruction": "hi"}' })).toEqual({
      source: '{"instruction": "hi"}',
      source_format: 'json',
    })
  })

  it('detects inline yaml', () => {
    expect(renderConfigSource({ source: 'instruction: hi' })).toEqual({
      source: 'instruction: hi',
      source_format: 'yaml',
    })
  })

  it('rejects an empty attachment', () => {
    expect(() => renderConfigSource({})).toThrow(CliInputError)
  })

  it('rejects a file and inline source together', () => {
    const file = writeTemp('agent.yaml', 'instruction: hi\n')
    expect(() => renderConfigSource({ file, source: 'instruction: hi' })).toThrow(CliInputError)
  })
})
