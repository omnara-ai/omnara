import { isScalar, parseDocument, type Scalar, Schema } from 'yaml'

const backendTags = new Set([
  'tag:yaml.org,2002:int',
  'tag:yaml.org,2002:float',
  'tag:yaml.org,2002:timestamp',
])
const compat = new Schema({ schema: 'yaml-1.1' }).tags.filter(
  (tag) => backendTags.has(tag.tag) && tag.format !== 'TIME',
)
const backendSchema = new Schema({ schema: 'core', customTags: (tags) => [...compat, ...tags] })

export const configYamlOptions = { keepSourceTokens: true, compat }

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
