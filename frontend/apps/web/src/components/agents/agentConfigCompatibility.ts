import { zAgentConfigDefinitionTool } from '@omnara/sdk/zod'
import { type Document, isAlias, isMap, isNode, isScalar, isSeq, parseDocument } from 'yaml'
import { z } from 'zod'

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
}

export function configPath(path: readonly string[]) {
  return path.length === 0
    ? '/'
    : '/' + path.map((key) => key.replaceAll('~', '~0').replaceAll('/', '~1')).join('/')
}

const customTool = z
  .looseObject({
    ...zAgentConfigDefinitionTool.shape,
    type: z.literal('custom'),
    description: z.string().refine((value) => value.trim() !== '', 'Description is required.'),
    input_schema: zAgentConfigDefinitionTool.shape.input_schema.unwrap(),
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

export function parseConfigDocument(source: string) {
  const doc = parseDocument(source, { keepSourceTokens: true })
  const diagnostics: ConfigDiagnostic[] = doc.errors.map((error) => ({
    path: [],
    kind: 'fatal',
    error: true,
    message: error.message,
  }))
  function inspect(node: Document['contents'], path: string[]) {
    if (node == null) return
    if (
      isScalar(node) &&
      node.type === 'PLAIN' &&
      node.tag !== 'tag:yaml.org,2002:str' &&
      node.source &&
      /^(?:[+-]?(?:0[0-9_]+|0[bB][01_]+|[0-9][0-9_.eE+-]*_[0-9_.eE+-]+)|[0-9]{4}-[0-9]{2}-[0-9]{2}(?:[Tt ].*)?)$/.test(
        node.source,
      )
    ) {
      diagnostics.push({
        path,
        kind: 'opaque',
        message:
          'This plain YAML scalar has version-dependent meaning. It is preserved; use a quoted string or an explicit decimal value to edit it in Builder.',
      })
    }
    let blocker: string | undefined
    if (isAlias(node)) blocker = 'YAML aliases'
    else if (node.anchor) blocker = 'YAML anchors'
    else if (node.tag && !node.tag.startsWith('tag:yaml.org,2002:'))
      blocker = `YAML tag ${node.tag}`
    if (blocker) {
      diagnostics.push({
        path,
        kind: 'fatal',
        message: `This configuration uses ${blocker}, which the Builder cannot safely edit.`,
      })
    }
    if (isMap(node)) {
      for (const pair of node.items) {
        const key = z.string().safeParse(isScalar(pair.key) ? pair.key.value : null)
        if (!key.success) {
          diagnostics.push({
            path,
            kind: 'fatal',
            error: true,
            message: 'The Builder requires string mapping keys.',
          })
          continue
        }
        const childPath = [...path, key.data]
        if (
          isScalar(pair.key) &&
          key.data === '<<' &&
          (pair.key.type === 'PLAIN' || pair.key.tag === 'tag:yaml.org,2002:merge')
        ) {
          diagnostics.push({
            path: childPath,
            kind: 'fatal',
            message:
              'This configuration uses YAML merge keys, which the Builder cannot safely edit.',
          })
        }
        if (isNode(pair.key)) inspect(pair.key, childPath)
        if (isNode(pair.value)) inspect(pair.value, childPath)
      }
    } else if (isSeq(node)) {
      node.items.forEach((item, index) => {
        if (isNode(item)) inspect(item, [...path, String(index)])
      })
    }
  }
  if (doc.errors.length === 0) inspect(doc.contents, [])
  return { doc, diagnostics }
}

export function validateAdvancedEntry(entry: AdvancedConfigEntry): ConfigDiagnostic[] {
  const { doc, diagnostics } = parseConfigDocument(entry.source)
  const blockers = diagnostics.filter((issue) => issue.kind === 'fatal')
  if (blockers.length > 0)
    return blockers.map((issue) => ({
      ...issue,
      error: true,
      path: [...entry.path, ...issue.path],
    }))
  if (entry.kind !== 'custom') return []
  const json = z.json().safeParse(doc.toJS())
  const parsed = json.success ? customTool.safeParse(json.data) : json
  if (parsed.success) return []
  return parsed.error.issues.map((issue) => ({
    path: [...entry.path, ...issue.path.map(String)],
    kind: 'advanced',
    error: true,
    message: issue.message,
  }))
}
