export type AgentConfigMode<Extra extends string = never> = 'builder' | 'yaml' | Extra

export interface AgentConfigModeState<Extra extends string = never> {
  mode: AgentConfigMode<Extra>
  builderYaml: string
  editorYaml: string
  confirmDiscard: boolean
}

export type AgentConfigModeAction<Extra extends string = never> =
  | { type: 'switch-mode'; mode: AgentConfigMode<Extra> }
  | { type: 'builder-yaml-changed'; yaml: string }
  | { type: 'editor-yaml-changed'; yaml: string }
  | { type: 'discard-yaml-edits' }
  | { type: 'set-confirm-discard'; open: boolean }

export function initialAgentConfigModeState<Extra extends string = never>(
  mode: AgentConfigMode<Extra>,
): AgentConfigModeState<Extra> {
  return { mode, builderYaml: '', editorYaml: '', confirmDiscard: false }
}

export function yamlDiverged(state: AgentConfigModeState<string>) {
  return state.editorYaml !== state.builderYaml
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
      if (action.mode === 'yaml' && !yamlDiverged(state)) {
        return { ...state, mode: action.mode, editorYaml: state.builderYaml }
      }
      return { ...state, mode: action.mode }
    }
    case 'builder-yaml-changed':
      if (action.yaml === state.builderYaml) return state
      return {
        ...state,
        builderYaml: action.yaml,
        editorYaml: yamlDiverged(state) ? state.editorYaml : action.yaml,
      }
    case 'editor-yaml-changed':
      if (action.yaml === state.editorYaml) return state
      return { ...state, editorYaml: action.yaml }
    case 'discard-yaml-edits':
      return {
        ...state,
        mode: 'builder',
        editorYaml: state.builderYaml,
        confirmDiscard: false,
      }
    case 'set-confirm-discard':
      return { ...state, confirmDiscard: action.open }
  }
}
