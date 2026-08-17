export type AgentConfigMode<Extra extends string = never> = 'builder' | 'yaml' | Extra

export interface AgentConfigModeState<Extra extends string = never> {
  mode: AgentConfigMode<Extra>
  editorYaml: string | null
  confirmDiscard: boolean
}

export type AgentConfigModeAction<Extra extends string = never> =
  | { type: 'switch-mode'; mode: AgentConfigMode<Extra> }
  | { type: 'editor-yaml-changed'; yaml: string; builderYaml: string }
  | { type: 'adopt-yaml-edits' }
  | { type: 'discard-yaml-edits' }
  | { type: 'set-confirm-discard'; open: boolean }

export function initialAgentConfigModeState<Extra extends string = never>(
  mode: AgentConfigMode<Extra>,
): AgentConfigModeState<Extra> {
  return { mode, editorYaml: null, confirmDiscard: false }
}

export function yamlDiverged(state: AgentConfigModeState<string>) {
  return state.editorYaml !== null
}

export function agentConfigModeReducer<Extra extends string = never>(
  state: AgentConfigModeState<Extra>,
  action: AgentConfigModeAction<Extra>,
): AgentConfigModeState<Extra> {
  switch (action.type) {
    case 'switch-mode': {
      if (action.mode === state.mode) return state
      if (action.mode === 'builder' && yamlDiverged(state)) {
        return { ...state, confirmDiscard: true }
      }
      return { ...state, mode: action.mode }
    }
    case 'editor-yaml-changed':
      if (action.yaml === (state.editorYaml ?? action.builderYaml)) return state
      return { ...state, editorYaml: action.yaml === action.builderYaml ? null : action.yaml }
    case 'adopt-yaml-edits':
    case 'discard-yaml-edits':
      return { ...state, mode: 'builder', editorYaml: null, confirmDiscard: false }
    case 'set-confirm-discard':
      return { ...state, confirmDiscard: action.open }
  }
}
