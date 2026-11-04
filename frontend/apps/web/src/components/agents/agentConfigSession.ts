import { Document, parseDocument } from 'yaml'

import { parseBasicConfigSource } from '@/components/agents/agentConfigBasicExtract'
import { applyToDocument } from '@/components/agents/agentConfigBasicWrite'
import {
  type AdvancedConfigEntry,
  type ConfigDiagnostic,
  configPath,
  customToolNameError,
  validateAdvancedEntry,
} from '@/components/agents/agentConfigCompatibility'
import { ConfigEditError } from '@/components/agents/agentConfigDocument'
import { configYamlOptions } from '@/components/agents/agentConfigYaml'
import type { BasicConfig } from '@/components/agents/useAgentBuilderForm'

export interface ConfigEvaluation {
  yaml: string
  issues: ConfigDiagnostic[]
  unappliedChanges: boolean
}

export interface BasicConfigSession {
  readonly initialDraft: BasicConfig | null
  readonly diagnostics: ConfigDiagnostic[]
  validate(config: BasicConfig): ConfigDiagnostic[]
  evaluate(config: BasicConfig): ConfigEvaluation
  apply(config: BasicConfig): string
}

export function createBasicConfigSession(source: string): BasicConfigSession {
  const { doc, initialDraft, diagnostics, advancedEntries } = analyzeConfigSource(source)
  const reserved = new Set(advancedEntries.map((entry) => configPath(entry.path)))
  const validationCache = new Map<string, { source: string; issues: ConfigDiagnostic[] }>()
  const validateEntry = (entry: AdvancedConfigEntry) => {
    const key = configPath(entry.path)
    const cached = validationCache.get(key)
    if (cached?.source === entry.source) return cached.issues
    const issues = validateAdvancedEntry(entry)
    validationCache.set(key, { source: entry.source, issues })
    return issues
  }
  const validateConfig = (config: BasicConfig) => {
    let conflictingNames = false
    const issues = [
      ...diagnostics.filter((issue) => issue.error && issue.kind === 'opaque'),
      ...(config.advancedEntries ?? []).flatMap(validateEntry),
    ]
    const seen = new Set<string>()
    for (const entry of config.advancedEntries ?? []) {
      const path = configPath(entry.path)
      const isNew = !reserved.has(path)
      const message = seen.has(path)
        ? 'This tool name is already in use.'
        : isNew && entry.kind === 'custom'
          ? customToolNameError(
              entry.path[1] ?? '',
              config.tools.map((tool) => tool.name),
            )
          : undefined
      if (message) {
        conflictingNames = true
        issues.push({ path: entry.path, kind: 'advanced', error: true, message })
      }
      seen.add(path)
    }
    const named = [
      ['tools', config.tools.map((tool) => tool.name)],
      ['mcp', config.mcpServers.map((server) => server.name)],
      ['subagents', config.subagents.map((agent) => agent.key)],
    ] as const
    for (const [key, names] of named) {
      for (const name of names) {
        if (reserved.has(configPath([key, name]))) {
          issues.push({
            path: [key, name],
            kind: 'opaque',
            error: true,
            message: 'This name is already used by a preserved definition.',
          })
        }
      }
    }
    return { issues, conflictingNames }
  }
  const validate = (config: BasicConfig) => validateConfig(config).issues
  const evaluate = (config: BasicConfig): ConfigEvaluation => {
    const { issues, conflictingNames } = validateConfig(config)
    if (source !== '' && initialDraft == null)
      return { yaml: source, issues, unappliedChanges: false }
    if (conflictingNames) return { yaml: source, issues, unappliedChanges: true }
    const unappliedChanges =
      (config.advancedEntries ?? []).some(
        (entry) =>
          validateEntry(entry).length > 0 &&
          entry.source !==
            advancedEntries.find((saved) => configPath(saved.path) === configPath(entry.path))
              ?.source,
      ) ||
      config.tools.some((entry) => reserved.has(configPath(['tools', entry.name]))) ||
      config.mcpServers.some((entry) => reserved.has(configPath(['mcp', entry.name]))) ||
      config.subagents.some((entry) => reserved.has(configPath(['subagents', entry.key])))
    try {
      return {
        yaml: applyToDocument(
          source === '' ? new Document({}, configYamlOptions) : doc,
          source,
          initialDraft,
          {
            ...config,
            tools: config.tools.filter((entry) => !reserved.has(configPath(['tools', entry.name]))),
            mcpServers: config.mcpServers.filter(
              (entry) => !reserved.has(configPath(['mcp', entry.name])),
            ),
            subagents: config.subagents.filter(
              (entry) => !reserved.has(configPath(['subagents', entry.key])),
            ),
            advancedEntries: config.advancedEntries?.filter(
              (entry) => validateEntry(entry).length === 0,
            ),
          },
        ),
        issues,
        unappliedChanges,
      }
    } catch (error) {
      return {
        yaml: source,
        unappliedChanges: true,
        issues: [
          ...issues,
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

function analyzeConfigSource(source: string): ReturnType<typeof parseBasicConfigSource> {
  try {
    return parseBasicConfigSource(source)
  } catch {
    return {
      doc: parseDocument('{}', configYamlOptions),
      initialDraft: null,
      advancedEntries: [],
      diagnostics: [
        {
          path: [],
          kind: 'fatal',
          message:
            'The Builder could not safely analyze this document. Your source has been preserved in YAML.',
        },
      ],
    }
  }
}
