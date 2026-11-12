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
import {
  type ConfigSourceFormat,
  configSourceFormat,
} from '@/components/agents/agentConfigSourceFormat'
import { configYamlOptions } from '@/components/agents/agentConfigYaml'
import type { BasicConfig } from '@/components/agents/useAgentBuilderForm'
import { memoryGbToMb } from '@/lib/machine-memory'

interface ConfigEvaluation {
  yaml: string
  issues: ConfigDiagnostic[]
  unappliedChanges: boolean
}

export interface BasicConfigSession {
  readonly sourceFormat: ConfigSourceFormat
  readonly initialDraft: BasicConfig | null
  readonly diagnostics: ConfigDiagnostic[]
  evaluate(config: BasicConfig): ConfigEvaluation
  apply(config: BasicConfig): string
}

export function createBasicConfigSession(
  source: string,
  storedFormat?: ConfigSourceFormat,
): BasicConfigSession {
  const sourceFormat = configSourceFormat(source, storedFormat)
  const { doc, initialDraft, diagnostics, advancedEntries } = analyzeConfigSource(
    source,
    sourceFormat,
  )
  const originalPaths = new Set(advancedEntries.map((entry) => configPath(entry.path)))
  const validationCache = new Map<string, { source: string; issues: ConfigDiagnostic[] }>()
  const validateEntry = (entry: AdvancedConfigEntry) => {
    if (entry.removed) return []
    const key = configPath(entry.path)
    const cached = validationCache.get(key)
    if (cached?.source === entry.source) return cached.issues
    const issues = validateAdvancedEntry(entry, sourceFormat)
    validationCache.set(key, { source: entry.source, issues })
    return issues
  }
  const validateConfig = (config: BasicConfig) => {
    const reserved = new Set(originalPaths)
    for (const entry of config.advancedEntries ?? []) {
      if (entry.kind === 'custom' && entry.originalPath && validateEntry(entry).length === 0)
        reserved.delete(configPath(entry.originalPath))
    }
    for (const entry of config.advancedEntries ?? []) {
      if (!entry.removed) reserved.add(configPath(entry.path))
    }
    let conflictingNames = false
    const issues = [
      ...diagnostics.filter((issue) => issue.error && issue.kind === 'opaque'),
      ...(config.advancedEntries ?? []).flatMap(validateEntry),
    ]
    const seen = new Set<string>()
    for (const entry of config.advancedEntries ?? []) {
      if (entry.removed) continue
      const path = configPath(entry.path)
      const isNew = !originalPaths.has(path)
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
    return { issues, conflictingNames, reserved }
  }
  const evaluate = (config: BasicConfig): ConfigEvaluation => {
    const { issues, conflictingNames, reserved } = validateConfig(config)
    if (source !== '' && initialDraft == null)
      return { yaml: source, issues, unappliedChanges: false }
    if (conflictingNames) return { yaml: source, issues, unappliedChanges: true }
    const numbers = patchableNumbers(config, initialDraft)
    issues.push(...numbers.issues)
    const unappliedChanges =
      numbers.issues.length > 0 ||
      (config.advancedEntries ?? []).some(
        (entry) =>
          validateEntry(entry).length > 0 &&
          (entry.source !==
            advancedEntries.find(
              (saved) => configPath(saved.path) === configPath(entry.originalPath ?? entry.path),
            )?.source ||
            (entry.originalPath !== undefined &&
              configPath(entry.originalPath) !== configPath(entry.path))),
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
            ...numbers.config,
            tools: config.tools.filter((entry) => !reserved.has(configPath(['tools', entry.name]))),
            mcpServers: config.mcpServers.filter(
              (entry) => !reserved.has(configPath(['mcp', entry.name])),
            ),
            subagents: numbers.config.subagents.filter(
              (entry) => !reserved.has(configPath(['subagents', entry.key])),
            ),
            advancedEntries: config.advancedEntries?.filter(
              (entry) => validateEntry(entry).length === 0,
            ),
          },
          sourceFormat,
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
              error instanceof ConfigEditError
                ? error.message
                : 'The Builder cannot safely patch this edit. Your source has been preserved.',
          } satisfies ConfigDiagnostic,
        ],
      }
    }
  }
  return {
    sourceFormat,
    initialDraft,
    diagnostics,
    evaluate,
    apply: (config) => evaluate(config).yaml,
  }
}

function patchableNumbers(config: BasicConfig, baseline: BasicConfig | null) {
  const issues: ConfigDiagnostic[] = []
  const number = (
    value: string,
    previous: string | undefined,
    path: string[],
    convert: (value: string) => number = Number,
  ) => {
    if (Number.isFinite(convert(value))) return value
    issues.push({ path, kind: 'advanced', error: true, message: 'Enter a valid number.' })
    return previous ?? ''
  }
  return {
    issues,
    config: {
      ...config,
      maxSubagents: number(config.maxSubagents, baseline?.maxSubagents, ['max_subagents']),
      maxDepth: number(config.maxDepth, baseline?.maxDepth, ['max_depth']),
      machineSources: config.machineSources.map((row, index) => {
        if (row.kind !== 'pool') return row
        const previous = baseline?.machineSources.find((saved) => saved.id === row.id)
        const updated = { ...row }
        for (const [field, key] of [
          ['initialNumMachines', 'initial_num_machines'],
          ['maxMachines', 'max_machines'],
          ['deleteAfterIdleMinutes', 'delete_after_idle_minutes'],
          ['machineCpu', 'machine_cpu'],
          ['machineMemoryGb', 'machine_memory_mb'],
        ] as const) {
          updated[field] = number(
            row[field],
            previous?.[field],
            ['machine_sources', String(row.sourceIndex ?? index), key],
            field === 'machineMemoryGb' ? memoryGbToMb : Number,
          )
        }
        return updated
      }),
      subagents: config.subagents.map((row) => {
        const previous = baseline?.subagents.find((saved) => saved.key === row.key)
        return {
          ...row,
          maxInstances: number(row.maxInstances, previous?.maxInstances, [
            'subagents',
            row.key,
            'max_instances',
          ]),
          archiveAfterIdleMinutes: number(
            row.archiveAfterIdleMinutes,
            previous?.archiveAfterIdleMinutes,
            ['subagents', row.key, 'archive_after_idle_minutes'],
          ),
        }
      }),
    },
  }
}

function analyzeConfigSource(
  source: string,
  format: ConfigSourceFormat,
): ReturnType<typeof parseBasicConfigSource> {
  try {
    return parseBasicConfigSource(source, format)
  } catch {
    return {
      doc: parseDocument('{}'),
      initialDraft: null,
      advancedEntries: [],
      diagnostics: [
        {
          path: [],
          kind: 'fatal',
          message:
            'The Builder could not safely analyze this document. Your source has been preserved in the source editor.',
        },
      ],
    }
  }
}
