import { type Document, isMap } from 'yaml'

export const legacyBindingManagedToolNames = new Set(['list_channels', 'send_channel_message'])

export function withoutLegacyBindingManagedTools(document: Document): Document {
  const draft = document.clone()
  removeLegacyBindingManagedTools(draft)
  return draft
}

export function removeLegacyBindingManagedTools(document: Document): number {
  const tools = document.getIn(['tools'], true)
  if (!isMap(tools)) return 0
  let removed = 0
  for (const name of legacyBindingManagedToolNames) {
    if (tools.delete(name)) removed++
  }
  if (removed > 0 && tools.items.length === 0) document.delete('tools')
  return removed
}
