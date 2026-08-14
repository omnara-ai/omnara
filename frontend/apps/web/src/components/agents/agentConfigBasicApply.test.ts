import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'

import {
  applyToSource,
  commentedYaml,
  fullConfig,
  minimalYaml,
  mustDeserialize,
} from '@/components/agents/agentConfigBasicYaml.fixtures'

describe('createBasicConfigSession apply', () => {
  it('returns the source verbatim when the draft matches it', () => {
    const config = mustDeserialize(commentedYaml)
    expect(applyToSource(commentedYaml, config)).toBe(commentedYaml)
  })

  it('rewrites only the entries that changed, preserving everything else', () => {
    const config = mustDeserialize(commentedYaml)
    const updated = applyToSource(commentedYaml, {
      ...config,
      tools: config.tools.map((tool) =>
        tool.name === 'browser'
          ? { ...tool, permission: { mode: 'always_allow', parameters: {} } }
          : tool,
      ),
    })
    expect(updated).toContain('# top comment')
    expect(updated).toContain('# which model to use')
    expect(updated).toContain('provider_config: "anthropic"')
    expect(updated).toContain('unknown_field: keep me')
    expect(updated).toContain(
      '# keep shell locked down\n  shell:\n    permission:\n      mode: always_ask',
    )
    expect(updated).toContain('# our search proxy')
    expect(updated).toContain('# primary pool')
    expect(parse(updated)).toMatchObject({
      version: 'v1',
      tools: {
        shell: { permission: { mode: 'always_ask' } },
        browser: { type: 'built_in', permission: { mode: 'always_allow' } },
      },
    })
  })

  it('preserves untouched machine source entries when another one changes', () => {
    const config = mustDeserialize(commentedYaml)
    const updated = applyToSource(commentedYaml, {
      ...config,
      machineSources: config.machineSources.map((source) =>
        source.kind === 'machine' ? { ...source, defaultCwd: '/builds' } : source,
      ),
    })
    expect(updated).toContain('# primary pool')
    expect(updated).toContain('machine_pool_name: "default-pool"')
    expect(parse(updated)).toMatchObject({
      machine_sources: [
        { machine_pool_name: 'default-pool', cwd: '/workspace' },
        { machine_name: 'build-box', cwd: '/builds' },
      ],
    })
  })

  it('removes sections whose entries were all removed', () => {
    const config = mustDeserialize(commentedYaml)
    const updated = applyToSource(commentedYaml, { ...config, mcpServers: [] })
    expect(parse(updated)).not.toHaveProperty('mcp')
    expect(updated).toContain('# keep shell locked down')
  })

  it('updates the instruction without disturbing the rest of the document', () => {
    const config = mustDeserialize(commentedYaml)
    const updated = applyToSource(commentedYaml, { ...config, instruction: 'New plan.' })
    expect(updated).toContain('instruction: New plan.')
    expect(updated).toContain('# top comment')
    expect(updated).toContain('# our search proxy')
    expect(mustDeserialize(updated).instruction).toBe('New plan.')
  })

  it('emits valid YAML for instructions with irregular indentation', () => {
    const instruction = '  indented first line\nsecond line'
    const source = applyToSource('', { ...fullConfig, instruction })
    expect(mustDeserialize(source).instruction).toBe(instruction)
  })

  it('does not rewrite a row when only its resolved provider changed', () => {
    const source = `${minimalYaml}machine_sources:
  - machine_pool_name: "default-pool"
    machine_provider_options_overlay: {"image":"my-image"}
`
    const config = mustDeserialize(source)
    const backfilled = config.machineSources.map((row) => ({
      ...row,
      provider: 'blaxel',
      managementKind: 'cluster',
    }))
    expect(applyToSource(source, { ...config, machineSources: backfilled })).toBe(source)
  })

  it('serializes incomplete drafts so the YAML tab can mirror them', () => {
    const config = mustDeserialize(minimalYaml)
    const updated = applyToSource(minimalYaml, {
      ...config,
      mcpServers: [
        {
          id: 'mcp-incomplete',
          name: '',
          url: '',
          permission: { mode: 'always_ask', parameters: {} },
          defaultEnabled: true,
          authType: 'none',
          secretId: '',
          service: '',
          region: '',
        },
      ],
    })
    expect(parse(updated)).toMatchObject({ mcp: { '': { url: '' } } })
  })

  it('builds a fresh document when there is no baseline source', () => {
    const source = applyToSource('', fullConfig)
    expect(parse(source)).toMatchObject({
      instruction: fullConfig.instruction,
      model: { provider_config: 'anthropic', name: 'claude-sonnet-5' },
      skills: ['skl_1', 'skl_2'],
      tools: { shell: { type: 'built_in' } },
    })
  })
})
