import { describe, expect, it } from 'vitest'
import { parse } from 'yaml'

import { subagentModelSelection, withSubagentModel } from './agentConfigSubagents'
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

describe('withSubagentModel', () => {
  const pick = { providerConfig: 'anthropic', modelName: 'sonnet', reasoningEffort: '' }

  it('sets the model and reasoning effort', () => {
    expect(withSubagentModel(undefined, { ...pick, reasoningEffort: 'high' })).toEqual({
      provider_config: 'anthropic',
      name: 'sonnet',
      reasoning: { effort: 'high' },
    })
  })

  it('keeps fields the builder does not edit', () => {
    expect(
      withSubagentModel(
        { provider_config: 'openai', name: 'gpt', context_window_tokens: 4096 },
        pick,
      ),
    ).toEqual({ provider_config: 'anthropic', name: 'sonnet', context_window_tokens: 4096 })
  })

  it('drops the override once the model is cleared and nothing else is left', () => {
    const cleared = { providerConfig: '', modelName: '', reasoningEffort: '' }
    expect(withSubagentModel({ provider_config: 'openai', name: 'gpt' }, cleared)).toBeUndefined()
    expect(
      withSubagentModel(
        { provider_config: 'openai', name: 'gpt', cache_retention: 'long' },
        cleared,
      ),
    ).toEqual({
      cache_retention: 'long',
    })
  })

  it('round-trips through the picker selection', () => {
    expect(
      subagentModelSelection({
        provider_config: 'anthropic',
        name: 'sonnet',
        reasoning: { effort: 'low' },
      }),
    ).toEqual({ providerConfig: 'anthropic', modelName: 'sonnet', reasoningEffort: 'low' })
    expect(subagentModelSelection(undefined)).toEqual({
      providerConfig: '',
      modelName: '',
      reasoningEffort: '',
    })
  })
})
