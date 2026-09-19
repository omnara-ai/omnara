import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'

import { type BasicConfig, createBasicConfigSession } from './useAgentBuilderForm'

const minimalYaml = `instruction: Do the thing.
model:
  provider_config: anthropic
  name: claude-sonnet-5
`

function deserialize(source: string): BasicConfig | null {
  return createBasicConfigSession(source).initialDraft
}

function mustDeserialize(source: string): BasicConfig {
  const config = createBasicConfigSession(source).initialDraft
  if (config == null) throw new Error('expected the config to deserialize')
  return config
}

function applyToSource(source: string, config: BasicConfig): string {
  return createBasicConfigSession(source).apply(config)
}

describe('builder source tools', () => {
  it('does not add a default machine source to existing configs without one', () => {
    const config = mustDeserialize(minimalYaml)
    expect(config.machineSources).toEqual([])
    expect(applyToSource(minimalYaml, config)).toBe(minimalYaml)
  })

  it('preserves explicit machine tool permissions when changing sources', () => {
    const source = `${minimalYaml}tools:
  run_command:
    permission:
      mode: always_ask
machine_sources:
  - machine_name: build-box
`
    const config = mustDeserialize(source)
    config.machineSources = config.machineSources.map((source) => ({
      ...source,
      name: 'another-box',
    }))
    expect(parse(applyToSource(source, config))).toHaveProperty('tools', {
      run_command: { permission: { mode: 'always_ask' } },
    })
  })

  it('leaves tool defaulting to the preview endpoint when changing sources', () => {
    const source = `${minimalYaml}machine_sources:
  - machine_name: build-box
`
    const config = mustDeserialize(source)
    config.machineSources = config.machineSources.map((source) => ({
      ...source,
      name: 'another-box',
    }))
    expect(parse(applyToSource(source, config))).not.toHaveProperty('tools')
  })

  it.each(['run_command', 'skill', 'spawn_agent', 'slack_post_message', 'web_search'])(
    'round-trips explicitly disabled %s',
    (name) => {
      const source = `${minimalYaml}tools:
  ${name}:
    enabled: false
    permission:
      mode: always_ask
`
      const config = mustDeserialize(source)
      expect(config.tools).toEqual([
        { name, enabled: false, permission: { mode: 'always_ask', parameters: {} } },
      ])
      expect(applyToSource(source, config)).toBe(source)
      config.instruction = 'Changed instruction.'
      expect(parse(applyToSource(source, config))).toHaveProperty(['tools', name, 'enabled'], false)
      config.tools = config.tools.map((tool) => ({ ...tool, enabled: true }))
      expect(parse(applyToSource(source, config))).toHaveProperty(['tools', name], {
        type: 'built_in',
        permission: { mode: 'always_ask' },
      })
    },
  )

  it('round-trips disabled tool names without catalog knowledge', () => {
    const source = `${minimalYaml}tools:
  shell:
    enabled: false
    permission:
      mode: always_ask
`
    const config = deserialize(source)
    expect(config).not.toBeNull()
    if (config) expect(applyToSource(source, config)).toBe(source)
  })
})
