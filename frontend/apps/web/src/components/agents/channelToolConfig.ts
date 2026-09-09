import { type Document, isAlias, isMap, isNode, isScalar, type Node, visit } from 'yaml'

export const legacyBindingManagedToolNames = new Set(['list_channels', 'send_channel_message'])

export function withoutLegacyBindingManagedTools(document: Document): Document {
  const draft = document.clone()
  removeLegacyBindingManagedTools(draft)
  return draft
}

export function removeLegacyBindingManagedTools(document: Document): number {
  const tools = document.getIn(['tools'], true)
  if (!isMap(tools)) return 0
  const removedNodes = new Set<Node>()
  for (const name of legacyBindingManagedToolNames) {
    const entry = tools.items.find(({ key }) => isScalar(key) && key.value === name)
    for (const node of [entry?.key, entry?.value]) {
      if (isNode(node)) {
        visit(node, {
          Node: (_, child) => {
            removedNodes.add(child)
          },
        })
      }
    }
  }
  if (removedNodes.size === 0) return 0
  if (tools.items.every(({ key }) => isNode(key) && removedNodes.has(key))) {
    removedNodes.add(tools)
  }
  // Materialize references before deleting their definitions. Let YAML assign
  // fresh anchors for shared/cyclic values so copied nested anchors cannot
  // change the meaning of an unrelated alias later in the document.
  visit(document, {
    Node: (_, node) => {
      if (removedNodes.has(node)) return visit.SKIP
      if (isAlias(node)) {
        const source = node.resolve(document)
        if (source && removedNodes.has(source)) {
          const value: unknown = source.toJS(document)
          const replacement = document.createNode(value)
          replacement.comment = node.comment ?? source.comment
          replacement.commentBefore = node.commentBefore ?? source.commentBefore
          return replacement
        }
      }
      return undefined
    },
  })
  let removed = 0
  for (const name of legacyBindingManagedToolNames) {
    if (tools.delete(name)) removed++
  }
  if (removed > 0 && tools.items.length === 0) document.delete('tools')
  return removed
}
