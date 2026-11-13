import { type Document, isAlias, isMap, isNode, isScalar, isSeq, parseDocument } from 'yaml'
import { z } from 'zod'

import { scalarPreservationReason } from './config-yaml'
export { configNodeSource, serializeConfigDocument } from './config-serialization'
export { configYamlOptions, scalarPreservationReason } from './config-yaml'

export type ConfigSourceFormat = 'yaml' | 'json'

export interface SourceDiagnostic {
  path: string[]
  kind: 'fatal' | 'opaque'
  message: string
  error?: boolean
}

export function parseConfigDocument(source: string, format: ConfigSourceFormat = 'yaml') {
  const doc = parseDocument(source)
  const diagnostics: SourceDiagnostic[] = doc.errors.map((error) => ({
    path: [],
    kind: 'fatal',
    error: true,
    message: error.message,
  }))
  if (format === 'json' && source.trim() !== '') {
    try {
      JSON.parse(source)
    } catch (error) {
      diagnostics.unshift({
        path: [],
        kind: 'fatal',
        error: true,
        message: `This configuration uses JSON. Enter valid JSON: ${error instanceof Error ? error.message : 'Invalid syntax.'}`,
      })
    }
  }
  if (doc.directives.yaml.version === '1.1')
    diagnostics.push({
      path: [],
      kind: 'fatal',
      message:
        'This document declares YAML 1.1, which the Builder cannot safely edit. It remains available in YAML.',
    })
  function inspect(node: Document['contents'], path: string[], documentComments = '') {
    if (node == null) return
    const scalarReason = isScalar(node) ? scalarPreservationReason(node) : null
    if (scalarReason) diagnostics.push({ path, kind: 'opaque', message: scalarReason })
    let blocker: string | undefined
    if (isAlias(node)) blocker = 'YAML aliases'
    else if (node.anchor) blocker = 'YAML anchors'
    else if (node.tag && !node.tag.startsWith('tag:yaml.org,2002:'))
      blocker = `YAML tag ${node.tag}`
    else if (
      format === 'yaml' &&
      /[\u0085\u2028\u2029]/.test(
        [
          isScalar(node) ? String(node.value) : '',
          node.comment,
          node.commentBefore,
          documentComments,
        ].join(''),
      )
    )
      blocker = 'a Unicode line separator (U+0085, U+2028, or U+2029)'
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
  if (doc.errors.length === 0) inspect(doc.contents, [], [doc.commentBefore, doc.comment].join(''))
  return { doc, diagnostics }
}
