import { expect, it } from 'vitest'
import { parse } from 'yaml'

import { createBasicConfigSession } from './useAgentBuilderForm'

it.each([
  ['{provider_config: anthropic, name: claude-haiku}', true],
  ['{reasoning: {effort: low}}', true],
  ['{name: claude-haiku}', false],
  ['{provider_config: anthropic}', false],
])('validates subagent model selection %s', (model, valid) => {
  const source = `instruction: Help.
model: {provider_config: anthropic, name: claude-sonnet-5}
subagents:
  fork:
    type: self
    model: ${model}
`
  const session = createBasicConfigSession(source)
  expect(session.initialDraft !== null).toBe(valid)
  if (session.initialDraft !== null) {
    expect(session.apply(session.initialDraft)).toBe(source)
  }
})

it('keeps subagent model overrides authored in YAML', () => {
  const source = `instruction: Do the thing.
model:
  provider_config: anthropic
  name: claude-sonnet-5
subagents:
  fork:
    type: self
    model:
      provider_config: anthropic
      name: claude-haiku
`
  const session = createBasicConfigSession(source)
  const config = session.initialDraft
  if (config === null) throw new Error('expected the config to deserialize')
  expect(config.subagents[0]?.modelOverride).toEqual({
    provider_config: 'anthropic',
    name: 'claude-haiku',
  })
  expect(session.apply(config)).toBe(source)
  const renamed = {
    ...config,
    subagents: config.subagents.map((subagent) => ({ ...subagent, description: 'Fork.' })),
  }
  expect(parse(session.apply(renamed))).toMatchObject({
    subagents: {
      fork: {
        type: 'self',
        description: 'Fork.',
        model: { provider_config: 'anthropic', name: 'claude-haiku' },
      },
    },
  })
})
