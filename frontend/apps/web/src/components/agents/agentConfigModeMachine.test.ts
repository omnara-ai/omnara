import { describe, expect, it } from 'vitest'

import {
  agentConfigModeReducer,
  initialAgentConfigModeState,
  yamlDiverged,
} from './agentConfigModeMachine'

describe('agentConfigModeReducer', () => {
  it('mirrors the builder until the editor diverges', () => {
    let state = initialAgentConfigModeState('yaml')
    state = agentConfigModeReducer(state, {
      type: 'editor-yaml-changed',
      yaml: 'a: 1\n',
      builderYaml: 'a: 1\n',
    })
    expect(state.editorYaml).toBeNull()
    state = agentConfigModeReducer(state, {
      type: 'editor-yaml-changed',
      yaml: 'a: 2\n',
      builderYaml: 'a: 1\n',
    })
    expect(state.editorYaml).toBe('a: 2\n')
    expect(yamlDiverged(state)).toBe(true)
  })

  it('collapses the fork when edits return to the builder yaml', () => {
    let state = initialAgentConfigModeState('yaml')
    state = agentConfigModeReducer(state, {
      type: 'editor-yaml-changed',
      yaml: 'a: 2\n',
      builderYaml: 'a: 1\n',
    })
    state = agentConfigModeReducer(state, {
      type: 'editor-yaml-changed',
      yaml: 'a: 1\n',
      builderYaml: 'a: 1\n',
    })
    expect(state.editorYaml).toBeNull()
  })

  it('asks for confirmation before switching to the builder with a fork, then discard clears it', () => {
    let state = initialAgentConfigModeState('yaml')
    state = agentConfigModeReducer(state, {
      type: 'editor-yaml-changed',
      yaml: 'a: 2\n',
      builderYaml: 'a: 1\n',
    })
    state = agentConfigModeReducer(state, { type: 'switch-mode', mode: 'builder' })
    expect(state.mode).toBe('yaml')
    expect(state.confirmDiscard).toBe(true)
    state = agentConfigModeReducer(state, { type: 'discard-yaml-edits' })
    expect(state.mode).toBe('builder')
    expect(state.editorYaml).toBeNull()
    expect(state.confirmDiscard).toBe(false)
  })

  it('adopt-yaml-edits enters the builder and clears the fork', () => {
    let state = initialAgentConfigModeState('yaml')
    state = agentConfigModeReducer(state, {
      type: 'editor-yaml-changed',
      yaml: 'a: 2\n',
      builderYaml: 'a: 1\n',
    })
    state = agentConfigModeReducer(state, { type: 'adopt-yaml-edits' })
    expect(state.mode).toBe('builder')
    expect(state.editorYaml).toBeNull()
    expect(state.confirmDiscard).toBe(false)
  })
})
