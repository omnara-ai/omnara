import { describe, expect, it } from 'vitest'

import { selectDefaultModel } from './default-model'

const router = { provider_config: 'omnara-openrouter', name: 'openai/gpt-6.1-sol' }
const gemma = { provider_config: 'omnara-openlake', name: 'google/gemma-4-31b-it' }
const glm = { provider_config: 'omnara-openlake', name: 'z-ai/glm-5.2' }

describe('new agent default model', () => {
  it('prefers OpenLake Gemma even when OpenRouter and another OpenLake model appear first', () => {
    expect(selectDefaultModel([router, glm, gemma])).toBe(gemma)
  })

  it('uses another available OpenLake model when Gemma is not shared', () => {
    expect(selectDefaultModel([router, glm])).toBe(glm)
  })

  it('preserves existing fallbacks for projects without OpenLake or without any models', () => {
    const other = { provider_config: 'custom-provider', name: 'custom-model' }
    expect(selectDefaultModel([other, router])).toBe(router)
    expect(selectDefaultModel([other])).toBe(other)
    expect(selectDefaultModel<typeof gemma>([])).toBeUndefined()
  })
})
