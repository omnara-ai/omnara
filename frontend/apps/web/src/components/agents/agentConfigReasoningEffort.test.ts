import { describe, expect, it } from 'vitest'

import { withReasoningEffort } from './agentConfigReasoningEffort'

describe('withReasoningEffort', () => {
  it('adds the effort to a model without reasoning', () => {
    const source = `# Support agent
instruction: Help.
model:
  provider_config: openrouter # shared
  name: claude-sonnet-5
`
    expect(withReasoningEffort(source, 'yaml', 'low')).toBe(`# Support agent
instruction: Help.
model:
  provider_config: openrouter # shared
  name: claude-sonnet-5
  reasoning:
    effort: low
`)
  })

  it('replaces an existing effort in a flow map', () => {
    const source = `instruction: Help.
model: {provider_config: openai, name: gpt, reasoning: {effort: high}}
`
    expect(withReasoningEffort(source, 'yaml', 'none')).toBe(`instruction: Help.
model: { provider_config: openai, name: gpt, reasoning: { effort: none } }
`)
  })

  it('keeps a json source as json', () => {
    const source = '{"instruction": "Help.", "model": {"provider_config": "openai", "name": "gpt"}}'
    expect(JSON.parse(withReasoningEffort(source, 'json', 'low') ?? '')).toEqual({
      instruction: 'Help.',
      model: { provider_config: 'openai', name: 'gpt', reasoning: { effort: 'low' } },
    })
  })

  it.each([
    'instruction: [',
    'instruction: Help.\n',
    'model: gpt\n',
    'model:\n  name: gpt\n  reasoning: high\n',
  ])('rejects source without editable model reasoning: %j', (source) => {
    expect(withReasoningEffort(source, 'yaml', 'low')).toBeNull()
  })
})
