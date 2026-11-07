import { describe, expect, it } from 'vitest'

import {
  createModelProviderFormDefaults,
  createModelProviderFormValid,
  modelProviderEndpointDefaults,
  modelProviderKind,
  modelProviderLabel,
  modelProviderOption,
} from './CreateModelProviderDialogState'

describe('createModelProviderFormValid', () => {
  const custom = {
    ...createModelProviderFormDefaults,
    ...modelProviderEndpointDefaults('custom'),
    provider: 'custom' as const,
    name: 'my-endpoint',
    secretId: 'sec_123',
  }

  it('requires an http(s) base URL for every provider but Bedrock', () => {
    expect(createModelProviderFormValid(custom)).toBe(false)
    expect(createModelProviderFormValid({ ...custom, baseUrl: 'api.example.com' })).toBe(false)
    expect(
      createModelProviderFormValid({ ...custom, baseUrl: ' https://api.example.com/v1 ' }),
    ).toBe(true)
    expect(createModelProviderFormValid({ ...custom, baseUrl: 'HTTPS://api.example.com/v1' })).toBe(
      true,
    )
    const openai = {
      ...custom,
      ...modelProviderEndpointDefaults('openai'),
      provider: 'openai' as const,
    }
    expect(createModelProviderFormValid(openai)).toBe(true)
    expect(createModelProviderFormValid({ ...openai, baseUrl: 'not a url' })).toBe(false)
    expect(createModelProviderFormValid({ ...custom, provider: 'bedrock' })).toBe(true)
  })

  it('requires named headers and a secret for each secret header', () => {
    const openai = {
      ...custom,
      ...modelProviderEndpointDefaults('openai'),
      provider: 'openai' as const,
    }
    const header = { id: 'text', key: 'X-Team', value: 'platform' }
    const secretHeader = { id: 'secret', key: 'X-Gateway-Key', secretId: 'sec_456' }
    expect(
      createModelProviderFormValid({
        ...openai,
        headerRows: [header],
        secretHeaderRows: [secretHeader],
      }),
    ).toBe(true)
    expect(createModelProviderFormValid({ ...openai, headerRows: [{ ...header, key: ' ' }] })).toBe(
      false,
    )
    expect(
      createModelProviderFormValid({
        ...openai,
        secretHeaderRows: [{ ...secretHeader, secretId: '' }],
      }),
    ).toBe(false)
  })
})

describe('modelProviderKind', () => {
  const config = { api_variant: 'default', auth_kind: 'bearer', base_url: '' }

  it('matches presets by host', () => {
    expect(modelProviderKind({ ...config, base_url: 'https://api.openai.com/v1' })).toBe('openai')
    expect(modelProviderKind({ ...config, base_url: 'https://api.anthropic.com/v1' })).toBe(
      'anthropic',
    )
    expect(modelProviderKind({ ...config, base_url: 'https://openrouter.ai/api/v1' })).toBe(
      'openrouter',
    )
    expect(
      modelProviderKind({ ...config, base_url: 'https://bedrock-mantle.us-east-1.api.aws/v1' }),
    ).toBe('bedrock')
  })

  it('recognizes OpenLLM on openllm.sh and www.openllm.sh', () => {
    expect(modelProviderKind({ ...config, base_url: 'https://www.openllm.sh/v1' })).toBe('openllm')
    expect(modelProviderKind({ ...config, base_url: 'https://openllm.sh/v1' })).toBe('openllm')
    expect(modelProviderKind({ ...config, base_url: 'https://notopenllm.sh/v1' })).toBe('custom')
    expect(modelProviderKind({ ...config, base_url: 'https://openllm.sh.evil.example/v1' })).toBe(
      'custom',
    )
  })

  it('prefers the API variant and SigV4 auth over the host', () => {
    expect(modelProviderKind({ ...config, api_variant: 'openrouter' })).toBe('openrouter')
    expect(modelProviderKind({ ...config, auth_kind: 'sigv4' })).toBe('bedrock')
  })

  it('falls back to custom for unknown or lookalike hosts', () => {
    expect(modelProviderKind({ ...config, base_url: 'https://llm.example.com/v1' })).toBe('custom')
    expect(modelProviderKind({ ...config, base_url: 'https://notopenai.com/v1' })).toBe('custom')
    expect(modelProviderKind({ ...config, base_url: 'not a url' })).toBe('custom')
  })
})

describe('OpenLLM preset', () => {
  it('uses the OpenLLM cloud endpoint over Chat Completions with an sk-llm key hint', () => {
    expect(modelProviderOption('openllm')).toMatchObject({
      label: 'OpenLLM',
      keyPlaceholder: 'sk-llm-…',
      endpoint: { baseUrl: 'https://www.openllm.sh/v1', apiFormat: 'openai-chat-completions' },
    })
    expect(modelProviderEndpointDefaults('openllm')).toEqual({
      baseUrl: 'https://www.openllm.sh/v1',
      apiFormat: 'openai-chat-completions',
    })
    expect(modelProviderLabel('openllm')).toBe('OpenLLM')
  })

  it('validates the preset form once a name and secret are set', () => {
    const values = {
      ...createModelProviderFormDefaults,
      ...modelProviderEndpointDefaults('openllm'),
      provider: 'openllm' as const,
      name: 'openllm',
      secretId: 'sec_123',
    }
    expect(createModelProviderFormValid(values)).toBe(true)
  })
})
