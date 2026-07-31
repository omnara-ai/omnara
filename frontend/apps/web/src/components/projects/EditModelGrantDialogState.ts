import type {
  ConfiguredModel,
  ProjectModelGrant,
  UpdateProjectModelGrantRequest,
} from '@omnara/sdk'

import {
  numberDraft,
  optionalIntOrNull,
  optionalPositiveInt32Valid,
} from '@/components/machines/machineOverrides'

export type InheritableToggleDraft = 'inherit' | 'enabled' | 'disabled'
export type CacheRetentionDraft = 'inherit' | 'none' | 'short' | 'long'

export interface ModelGrantOverrideDraft {
  contextWindow: string
  maxOutput: string
  defaultMaxOutput: string
  cacheRetention: CacheRetentionDraft
  supportsTools: InheritableToggleDraft
  supportsReasoning: InheritableToggleDraft
  reasoningEffort: string
  reasoningEfforts: string
  inputModalities: string
  outputModalities: string
}

function toggleDraft(value: boolean | null | undefined): InheritableToggleDraft {
  if (value == null) return 'inherit'
  return value ? 'enabled' : 'disabled'
}

export function modelGrantDraftFromGrant(grant: ProjectModelGrant): ModelGrantOverrideDraft {
  return {
    contextWindow: numberDraft(grant.context_window_tokens),
    maxOutput: numberDraft(grant.max_output_tokens),
    defaultMaxOutput: numberDraft(grant.default_max_output_tokens),
    cacheRetention: grant.default_cache_retention ?? 'inherit',
    supportsTools: toggleDraft(grant.supports_tools),
    supportsReasoning: toggleDraft(grant.supports_reasoning),
    reasoningEffort: grant.default_reasoning_effort ?? '',
    reasoningEfforts: grant.supported_reasoning_efforts.join(', '),
    inputModalities: grant.input_modalities.join(', '),
    outputModalities: grant.output_modalities.join(', '),
  }
}

export interface ModelGrantTokenField {
  key: 'contextWindow' | 'maxOutput' | 'defaultMaxOutput'
  label: string
  inherited: (model: ConfiguredModel) => number | null | undefined
}

export const MODEL_GRANT_TOKEN_FIELDS: ModelGrantTokenField[] = [
  {
    key: 'contextWindow',
    label: 'Context window (tokens)',
    inherited: (model) => model.context_window_tokens,
  },
  { key: 'maxOutput', label: 'Max output (tokens)', inherited: (model) => model.max_output_tokens },
  {
    key: 'defaultMaxOutput',
    label: 'Default max output (tokens)',
    inherited: (model) => model.default_max_output_tokens,
  },
]

export interface ModelGrantTextField {
  key: 'reasoningEffort' | 'reasoningEfforts' | 'inputModalities' | 'outputModalities'
  label: string
  commaSeparated: boolean
  inherited: (model: ConfiguredModel) => string
}

export const MODEL_GRANT_TEXT_FIELDS: ModelGrantTextField[] = [
  {
    key: 'reasoningEffort',
    label: 'Default reasoning effort',
    commaSeparated: false,
    inherited: (model) => model.default_reasoning_effort,
  },
  {
    key: 'reasoningEfforts',
    label: 'Supported reasoning efforts',
    commaSeparated: true,
    inherited: (model) => model.supported_reasoning_efforts.join(', '),
  },
  {
    key: 'inputModalities',
    label: 'Input modalities',
    commaSeparated: true,
    inherited: (model) => model.input_modalities.join(', '),
  },
  {
    key: 'outputModalities',
    label: 'Output modalities',
    commaSeparated: true,
    inherited: (model) => model.output_modalities.join(', '),
  },
]

export function modelGrantOverridesValid(draft: ModelGrantOverrideDraft) {
  return [draft.contextWindow, draft.maxOutput, draft.defaultMaxOutput].every(
    optionalPositiveInt32Valid,
  )
}

function listFromDraft(value: string): string[] {
  return value
    .split(',')
    .map((entry) => entry.trim())
    .filter((entry) => entry !== '')
}

function toggleValue(value: InheritableToggleDraft): boolean | null {
  if (value === 'inherit') return null
  return value === 'enabled'
}

/**
 * The edit form is prefilled from the stored grant, so every field is sent
 * explicitly: cleared inputs reset the override and the project inherits
 * from the configured model.
 */
export function modelGrantUpdateRequest(
  draft: ModelGrantOverrideDraft,
): UpdateProjectModelGrantRequest {
  return {
    context_window_tokens: optionalIntOrNull(draft.contextWindow),
    max_output_tokens: optionalIntOrNull(draft.maxOutput),
    default_max_output_tokens: optionalIntOrNull(draft.defaultMaxOutput),
    default_cache_retention: draft.cacheRetention === 'inherit' ? null : draft.cacheRetention,
    supports_tools: toggleValue(draft.supportsTools),
    supports_reasoning: toggleValue(draft.supportsReasoning),
    default_reasoning_effort:
      draft.reasoningEffort.trim() === '' ? null : draft.reasoningEffort.trim(),
    supported_reasoning_efforts: listFromDraft(draft.reasoningEfforts),
    input_modalities: listFromDraft(draft.inputModalities),
    output_modalities: listFromDraft(draft.outputModalities),
  }
}
