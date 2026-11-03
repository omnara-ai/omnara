import { Document } from 'yaml'

import { parseBasicConfigSource } from '@/components/agents/agentConfigBasicExtract'
import { applyToDocument } from '@/components/agents/agentConfigBasicWrite'
import {
  type ConfigDiagnostic,
  validateAdvancedEntry,
} from '@/components/agents/agentConfigCompatibility'
import { ConfigEditError } from '@/components/agents/agentConfigDocument'
import type { BasicConfig } from '@/components/agents/useAgentBuilderForm'

export interface ConfigEvaluation {
  yaml: string
  issues: ConfigDiagnostic[]
}

export interface BasicConfigSession {
  readonly initialDraft: BasicConfig | null
  readonly diagnostics: ConfigDiagnostic[]
  validate(config: BasicConfig): ConfigDiagnostic[]
  evaluate(config: BasicConfig): ConfigEvaluation
  apply(config: BasicConfig): string
}

export function createBasicConfigSession(source: string): BasicConfigSession {
  const { doc, initialDraft, diagnostics, advancedEntries } = parseBasicConfigSource(source)
  const validate = (config: BasicConfig) => {
    const issues = [
      ...diagnostics.filter((issue) => issue.error && issue.kind === 'opaque'),
      ...(config.advancedEntries ?? []).flatMap(validateAdvancedEntry),
    ]
    const named = [
      ['tools', config.tools.map((tool) => tool.name)],
      ['mcp', config.mcpServers.map((server) => server.name)],
      ['subagents', config.subagents.map((agent) => agent.key)],
    ] as const
    for (const [key, names] of named) {
      for (const name of names) {
        if (advancedEntries.some((entry) => entry.path[0] === key && entry.path[1] === name)) {
          issues.push({
            path: [key, name],
            kind: 'opaque',
            error: true,
            message: 'This name is already used by a preserved definition.',
          })
        }
      }
    }
    return issues
  }
  const evaluate = (config: BasicConfig): ConfigEvaluation => {
    const issues = validate(config)
    if (source !== '' && initialDraft == null) return { yaml: source, issues }
    try {
      return {
        yaml: applyToDocument(source === '' ? new Document({}) : doc, source, initialDraft, {
          ...config,
          advancedEntries: config.advancedEntries?.filter(
            (entry) => validateAdvancedEntry(entry).length === 0,
          ),
        }),
        issues,
      }
    } catch (error) {
      return {
        yaml: source,
        issues: [
          {
            path: error instanceof ConfigEditError ? error.path.map(String) : [],
            kind: 'fatal',
            error: true,
            message:
              error instanceof Error ? error.message : 'The Builder cannot safely patch this edit.',
          } satisfies ConfigDiagnostic,
        ],
      }
    }
  }
  return { initialDraft, diagnostics, validate, evaluate, apply: (config) => evaluate(config).yaml }
}
