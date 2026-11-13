import { type ConfigSourceFormat, parseConfigDocument } from '@omnara/sdk/config-source'
import { zAgentConfigDefinitionTool } from '@omnara/sdk/zod'
import { z } from 'zod'

import { configJson, configRecord } from '@/components/agents/agentConfigValidation'

export interface ConfigDiagnostic {
  path: string[]
  kind: 'fatal' | 'advanced' | 'opaque'
  message: string
  error?: boolean
}

export interface AdvancedConfigEntry {
  path: string[]
  kind: 'custom' | 'opaque'
  source: string
  originalPath?: string[]
  removed?: boolean
}

export function customToolNameError(name: string, reservedNames: readonly string[] = []) {
  if (name.trim() === '') return 'Enter a tool name.'
  if (!/^[A-Za-z_][A-Za-z0-9_]{0,63}$/.test(name))
    return 'Use 1–64 letters, digits, or underscores, starting with a letter or underscore.'
  if (name.startsWith('mcp__') || name.startsWith('int__') || name === 'call_deferred_tool')
    return 'This name is reserved for system tools.'
  if (reservedNames.includes(name)) return 'This tool name is already in use or reserved.'
  return undefined
}

export function configPath(path: readonly string[]) {
  return path.length === 0
    ? '/'
    : '/' + path.map((key) => key.replaceAll('~', '~0').replaceAll('/', '~1')).join('/')
}

const customTool = z
  .strictObject({
    ...zAgentConfigDefinitionTool.shape,
    type: z.literal('custom'),
    description: z.string().refine((value) => value.trim() !== '', 'Description is required.'),
    input_schema: zAgentConfigDefinitionTool.shape.input_schema.unwrap().extend({
      properties: configRecord(configRecord(z.unknown())).optional(),
    }),
  })
  .superRefine((tool, ctx) => {
    for (const name of tool.input_schema.required ?? []) {
      if (!Object.hasOwn(tool.input_schema.properties ?? {}, name)) {
        ctx.addIssue({
          code: 'custom',
          path: ['input_schema', 'required'],
          message: `Required field "${name}" is not declared in properties.`,
        })
      }
    }
  })

export function validateAdvancedEntry(
  entry: AdvancedConfigEntry,
  format: ConfigSourceFormat = 'yaml',
): ConfigDiagnostic[] {
  const { doc, diagnostics } = parseConfigDocument(entry.source, format)
  const blockers = diagnostics.filter((issue) => issue.kind === 'fatal')
  if (blockers.length > 0)
    return blockers.map((issue) => ({
      ...issue,
      error: true,
      path: [...entry.path, ...issue.path],
    }))
  if (entry.kind !== 'custom') return []
  const json = configJson.safeParse(doc.toJS())
  const parsed = json.success ? customTool.safeParse(json.data) : json
  if (parsed.success) return []
  return parsed.error.issues.map((issue) => ({
    path: [...entry.path, ...issue.path.map(String)],
    kind: 'advanced',
    error: true,
    message: issue.message,
  }))
}
