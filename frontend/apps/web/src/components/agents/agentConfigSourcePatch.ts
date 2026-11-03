import { CST, Document, isMap, isNode, isScalar, type Node, parseDocument } from 'yaml'

export type ConfigPath = (string | number)[]
export type ConfigEdit =
  | { type: 'set'; path: ConfigPath; value: Node; raw?: string }
  | { type: 'delete'; path: ConfigPath }
  | { type: 'reorder'; path: ConfigPath; order: number[] }

type Collection = CST.BlockMap | CST.BlockSequence | CST.FlowCollection

export function patchConfigSource(source: string, original: Document, edits: ConfigEdit[]) {
  if (edits.length === 0) return source
  const root = original.contents
  if (!isMap(root) || !root.srcToken || !root.range)
    throw new Error('Missing document source tokens.')
  const json = isJsonSource(source)
  const newline = source.includes('\r\n') ? '\r\n' : '\n'
  let token = structuredClone(root.srcToken)
  if (!CST.isCollection(token)) throw new Error('The configuration must be a mapping.')
  for (const edit of edits) {
    if (!CST.isCollection(token)) throw new Error('The configuration must be a mapping.')
    token = applyEdit(token, edit.path, edit, json, newline)
  }
  return source.slice(0, root.range[0]) + CST.stringify(token) + source.slice(root.range[2])
}

function applyEdit(
  parent: Collection,
  path: ConfigPath,
  edit: ConfigEdit,
  json: boolean,
  newline: string,
): Collection {
  const key = path[0]
  if (key === undefined) throw new Error('An edit must identify a configuration field.')
  const sequence = isSequence(parent)
  const index = sequence
    ? Number(key)
    : parent.items.findIndex((item) => CST.resolveAsScalar(item.key)?.value === key)
  const previous = parent.items[index]
  if (path.length > 1) {
    if (!previous?.value || !CST.isCollection(previous.value))
      throw new Error(`Cannot edit missing collection ${String(key)}.`)
    previous.value = applyEdit(previous.value, path.slice(1), edit, json, newline)
  } else if (edit.type === 'reorder') {
    if (!previous?.value || !CST.isCollection(previous.value) || !isSequence(previous.value))
      throw new Error('Cannot reorder a missing sequence.')
    const collection = previous.value
    const originalItems = collection.items
    const reordered = edit.order.map((index, position) => {
      const item = originalItems[index]
      if (!item) throw new Error('Missing sequence item.')
      if (collection.type === 'flow-collection')
        return { ...item, start: originalItems[position]?.start ?? [] }
      const start = [...item.start]
      if (position === 0 && start[0]?.type === 'space') start.shift()
      else if (position > 0 && start[0]?.type === 'seq-item-ind')
        start.unshift(sourceToken('space', ' '.repeat(collection.indent)))
      return { ...item, start }
    })
    const items: CST.CollectionItem[] = collection.items
    items.splice(0, edit.order.length, ...reordered)
  } else if (edit.type === 'delete') {
    if (index >= 0 && previous) removeItem(parent, index)
  } else {
    const replacement = renderItem(parent, key, edit.value, edit.raw, json, newline)
    if (previous && (previous.key || previous.value)) {
      replacement.start = previous.start
      if (previous.key) replacement.key = previous.key
      parent.items[index] = replacement
    } else appendItem(parent, replacement, newline)
  }
  if (parent.type !== 'flow-collection' && parent.items.length === 0) {
    const empty = parseDocument((isSequence(parent) ? '[]' : '{}') + newline, {
      keepSourceTokens: true,
    }).contents?.srcToken
    if (!empty || !CST.isCollection(empty)) throw new Error('Cannot render empty collection.')
    return empty
  }
  return parent
}

function isSequence(token: Collection) {
  return (
    token.type === 'block-seq' ||
    (token.type === 'flow-collection' && token.start.type === 'flow-seq-start')
  )
}

function renderItem(
  parent: Collection,
  key: string | number,
  value: Node,
  raw: string | undefined,
  json: boolean,
  newline: string,
): CST.CollectionItem {
  const sequence = isSequence(parent)
  const flow = parent.type === 'flow-collection'
  let rendered: string
  if (raw !== undefined && !flow && !sequence) {
    const indent = ' '.repeat(parent.indent + 2)
    const definition = raw
      .replace(/\r?\n/g, '\n')
      .split('\n')
      .map((line) => (line ? indent + line : ''))
      .join('\n')
    rendered =
      ' '.repeat(parent.indent) +
      JSON.stringify(key) +
      ':\n' +
      definition +
      '\n' +
      ' '.repeat(parent.indent) +
      '__end__: null\n'
  } else if (
    raw !== undefined &&
    flow &&
    !sequence &&
    (json ? isJsonSource(raw) : rawStartsWithFlowMap(raw))
  ) {
    rendered = '{' + JSON.stringify(key) + ': ' + raw + '\n}'
    rendered = rendered.replace(/^/gm, ' '.repeat(parent.indent))
  } else {
    const doc = new Document(sequence ? [] : {})
    doc.set(sequence ? 0 : key, value.clone())
    rendered = json
      ? JSON.stringify(doc.toJS())
      : doc.toString({ lineWidth: 0, collectionStyle: flow ? 'flow' : 'any' })
    rendered = rendered.replace(/^/gm, ' '.repeat(parent.indent))
  }
  rendered = rendered.replace(/\r?\n/g, newline)
  const token = parseDocument(rendered, { keepSourceTokens: true }).contents?.srcToken
  if (!token || !CST.isCollection(token) || !token.items[0])
    throw new Error('Cannot render the edited configuration field.')
  const item = token.items[0]
  if (raw !== undefined && !flow && !sequence && item.value && CST.isCollection(item.value)) {
    const tail = token.items[1]?.start ?? []
    if (tail.at(-1)?.type === 'space') tail.pop()
    if (item.value.type === 'flow-collection') item.value.end.push(...tail)
    else {
      const items: CST.CollectionItem[] = item.value.items
      items.push({ start: tail })
    }
  }
  return item
}

export function configNodeSource(source: string, doc: Document, path: string[]) {
  const node = doc.getIn(path, true)
  if (!isNode(node) || !node.range) throw new Error('Missing definition source range.')
  const parent = doc.getIn(path.slice(0, -1), true)
  const pair = isMap(parent)
    ? parent.items.find((pair) => isScalar(pair.key) && pair.key.value === path.at(-1))
    : undefined
  const start =
    pair?.srcToken?.sep?.find((token) => token.type === 'comment')?.offset ?? node.range[0]
  const indent = node.srcToken && 'indent' in node.srcToken ? node.srcToken.indent : 0
  return source.slice(start, node.range[2]).replace(new RegExp(`(^|\\n) {${indent}}`, 'g'), '$1')
}

function rawStartsWithFlowMap(source: string) {
  const node = parseDocument(source).contents
  return isMap(node) && node.flow === true
}

export function isJsonSource(source: string) {
  try {
    JSON.parse(source)
    return true
  } catch {
    return false
  }
}

function sourceToken(type: CST.SourceToken['type'], source: string): CST.SourceToken {
  return { type, source, indent: 0, offset: -1 }
}

function appendItem(parent: Collection, item: CST.CollectionItem, lineEnding: string) {
  if (parent.type === 'flow-collection') {
    const last = parent.items.at(-1)
    const trailing = last && !last.key && !last.value ? parent.items.pop() : undefined
    item.start =
      parent.items.length > 0
        ? [sourceToken('comma', ','), sourceToken('space', ' '), ...item.start]
        : item.start
    parent.items.push(item)
    if (trailing) parent.items.push(trailing)
    return
  }
  if (parent.items.length > 0) {
    const newline = CST.stringify(parent).endsWith('\n') ? [] : [sourceToken('newline', lineEnding)]
    item.start = [...newline, sourceToken('space', ' '.repeat(parent.indent)), ...item.start]
  }
  const items: CST.CollectionItem[] = parent.items
  items.push(item)
}

function removeItem(parent: Collection, index: number) {
  const [removed] = parent.items.splice(index, 1)
  if (!removed) return
  const next = parent.items[index]
  if (parent.type === 'flow-collection') {
    const gap = removed.start.filter((token) => token.type !== 'comma')
    if (next) next.start = [...gap, ...next.start]
    else parent.end.unshift(...gap)
    if (parent.items[0])
      parent.items[0].start = parent.items[0].start.filter((token) => token.type !== 'comma')
    return
  }
  if (index === 0 && next?.start[0]?.type === 'space') next.start.shift()
}
