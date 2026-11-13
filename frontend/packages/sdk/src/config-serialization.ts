import { applyEdits, format as formatJson } from 'jsonc-parser'
import { Document, isNode, type Scalar, type ScalarTag, visit } from 'yaml'

import type { ConfigSourceFormat } from './config-source'
import { configYamlOptions, scalarPreservationReason } from './config-yaml'

const isString = (value: Scalar['value']): value is string => typeof value === 'string'
const isNumber = (value: Scalar['value']): value is number => typeof value === 'number'

const sourceScalarTags: ScalarTag[] = ['int', 'float', 'str'].map((type) => ({
  tag: `tag:yaml.org,2002:${type}`,
  default: true,
  format: 'SOURCE',
  identify: type === 'str' ? isString : isNumber,
  ...(type !== 'str' && { test: /$^/ }),
  resolve: (source) => source,
  stringify: (node) => {
    if (node.source === undefined) throw new Error('Missing scalar source.')
    return node.source
  },
}))

export function serializeConfigDocument(document: Document, format: ConfigSourceFormat) {
  const doc = document.clone()
  doc.setSchema('1.2', {
    ...configYamlOptions,
    customTags: (tags) => [...tags, ...sourceScalarTags],
  })
  visit(doc, {
    Collection(_, node) {
      const flow = format === 'json' || node.items.length === 0
      if (node.flow && !flow && node.comment) {
        node.commentBefore = [node.commentBefore, node.comment].filter(Boolean).join('\n')
        node.comment = undefined
      }
      node.flow = flow
    },
    Scalar(_, node) {
      if (
        node.type === 'PLAIN' &&
        node.source &&
        (isNumber(node.value) || (node.value === node.source && scalarPreservationReason(node)))
      )
        node.format = 'SOURCE'
      if (format === 'json' && isString(node.value)) node.type = 'QUOTE_DOUBLE'
    },
  })
  const source = doc.toString({
    lineWidth: 0,
    ...(format === 'json' && {
      defaultKeyType: 'QUOTE_DOUBLE',
      defaultStringType: 'QUOTE_DOUBLE',
      doubleQuotedAsJSON: true,
      doubleQuotedMinMultiLineLength: Infinity,
    }),
  })
  return format === 'json'
    ? applyEdits(source, formatJson(source, undefined, { insertSpaces: true, tabSize: 2 }))
    : source
}

export function configNodeSource(doc: Document, path: string[], format: ConfigSourceFormat) {
  const node = doc.getIn(path, true)
  return isNode(node) ? serializeConfigDocument(new Document(node.clone()), format) : ''
}
