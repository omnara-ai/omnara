import { isScalar, parseDocument, type Scalar, type ScalarTag, Schema } from 'yaml'

const backendTags = new Set([
  'tag:yaml.org,2002:int',
  'tag:yaml.org,2002:float',
  'tag:yaml.org,2002:timestamp',
  'tag:yaml.org,2002:merge',
])
const digits = (set: string) => `[${set}][${set}_]*`
const backendNumberForms = [
  `0_*[xX]_*${digits('0-9a-fA-F')}`,
  `0_*[bB]_*[-+]?_*${digits('01')}`,
  `0_*[oO]_*[-+]?_*${digits('0-7')}`,
  `(?:\\._*${digits('0-9')}|${digits('0-9')}(?:\\.[0-9_]*)?)(?:[eE]_*[-+]?_*${digits('0-9')})?`,
]
const backendNumber: ScalarTag = {
  tag: 'tag:yaml.org,2002:float',
  default: true,
  test: new RegExp(`^(?:[-+]_*)?(?:${backendNumberForms.join('|')})$`),
  resolve: (source) => Number(source.replaceAll('_', '')),
}
const compat = [
  ...new Schema({ schema: 'yaml-1.1' }).tags.filter(
    (tag) => backendTags.has(tag.tag) && tag.format !== 'TIME',
  ),
  backendNumber,
]
const backendSchema = new Schema({ schema: 'core', customTags: (tags) => [...compat, ...tags] })

export const configYamlOptions = { compat }

export function scalarPreservationReason(node: Scalar) {
  if (Number.isInteger(node.value) && !Number.isSafeInteger(node.value))
    return 'This number exceeds the Builder’s exact integer range. Its original text is preserved; edit it in raw YAML or JSON.'
  if (node.type !== 'PLAIN' || node.tag === 'tag:yaml.org,2002:str' || !node.source) return null
  if (!compat.some((tag) => tag.test?.test(node.source ?? ''))) return null
  const backend = parseDocument(node.source, { schema: backendSchema }).contents
  return isScalar(backend) && !Object.is(node.value, backend.value)
    ? 'This plain YAML scalar has version-dependent meaning. It is preserved; use a quoted string or an explicit decimal value to edit it in Builder.'
    : null
}
