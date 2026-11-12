import { CST, Document, isMap, isNode, isScalar, type Node, parseDocument, visit } from 'yaml'

import { type ConfigSourceFormat, isJsonSource } from '@/components/agents/agentConfigSourceFormat'
import { configYamlOptions } from '@/components/agents/agentConfigYaml'

export type ConfigPath = (string | number)[]
export type ConfigEdit =
  | { type: 'set'; path: ConfigPath; value: Node; raw?: string }
  | { type: 'delete'; path: ConfigPath }
  | { type: 'reorder'; path: ConfigPath; order: number[] }
  | { type: 'rename'; path: ConfigPath; names: Map<string, string> }

type Collection = CST.BlockMap | CST.BlockSequence | CST.FlowCollection

export function patchConfigSource(
  source: string,
  original: Document,
  edits: ConfigEdit[],
  format: ConfigSourceFormat,
) {
  if (edits.length === 0) return source
  const root = original.contents
  if (!isMap(root) || !root.srcToken || !root.range)
    throw new Error('Missing document source tokens.')
  const json = format === 'json'
  const newline = source.includes('\r\n') ? '\r\n' : '\n'
  let token = structuredClone(root.srcToken)
  if (!CST.isCollection(token)) throw new Error('The configuration must be a mapping.')
  for (const edit of edits) {
    if (!CST.isCollection(token)) throw new Error('The configuration must be a mapping.')
    token = applyEdit(token, edit.path, edit, json, newline)
  }
  return (
    source.slice(0, root.range[0]) +
    CST.stringify(finalizeCollection(token, newline)) +
    source.slice(root.range[2])
  )
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
  } else if (edit.type === 'rename') {
    if (!previous?.value || !CST.isCollection(previous.value) || isSequence(previous.value))
      throw new Error('Cannot rename entries in a missing mapping.')
    for (const item of previous.value.items) {
      const name = CST.resolveAsScalar(item.key)?.value
      const renamed = edit.names.get(String(name))
      if (renamed !== undefined && item.key)
        CST.setScalarValue(item.key, renamed, {
          type:
            item.key.type === 'scalar' && yamlKey(renamed) !== renamed ? 'QUOTE_DOUBLE' : undefined,
        })
    }
  } else if (edit.type === 'reorder') {
    if (!previous?.value || !CST.isCollection(previous.value) || !isSequence(previous.value))
      throw new Error('Cannot reorder a missing sequence.')
    const collection = previous.value
    const originalItems = collection.items
    const flow = collection.type === 'flow-collection'
    const reordered = flow
      ? reorderFlowItems(collection, edit.order, newline)
      : edit.order.map((index, position) => {
          const item = originalItems[index]
          if (!item) throw new Error('Missing sequence item.')
          const start = [...item.start]
          if (position === 0 && start[0]?.type === 'space') start.shift()
          else if (position > 0 && start[0]?.type === 'seq-item-ind')
            start.unshift(sourceToken('space', ' '.repeat(collection.indent)))
          return { ...item, start }
        })
    if (collection.type === 'block-seq') {
      reordered.forEach((item, index) => {
        const previous = reordered[index - 1]
        if (previous && !CST.stringify(previous).endsWith('\n'))
          item.start.unshift(sourceToken('newline', newline))
      })
    }
    const items: CST.CollectionItem[] = collection.items
    items.splice(0, flow ? items.length : edit.order.length, ...reordered)
  } else if (edit.type === 'delete') {
    if (index >= 0 && previous) removeItem(parent, index)
  } else {
    const existing = previous && (previous.key || previous.value) ? previous : undefined
    const replacement = renderItem(
      parent,
      key,
      edit.value,
      edit.raw,
      json,
      newline,
      previous?.value?.type === 'block-map' ? previous.value.indent : undefined,
      followingSiblingTrivia(previous?.value),
      existing?.sep?.some((token) => token.type === 'newline') === false,
    )
    if (existing) {
      replacement.start = existing.start
      if (existing.key) replacement.key = existing.key
      if (parent.type === 'flow-collection') {
        if (existing.sep) {
          const previousComment = existing.sep.findIndex((token) => token.type === 'comment')
          const newComment = replacement.sep?.findIndex((token) => token.type === 'comment') ?? -1
          replacement.sep =
            edit.raw === undefined
              ? existing.sep
              : [
                  ...existing.sep.slice(
                    0,
                    previousComment < 0 ? existing.sep.length : previousComment,
                  ),
                  ...(newComment < 0 ? [] : (replacement.sep?.slice(newComment) ?? [])),
                ]
        }
        keepTrailing(replacement, takeTrailing(existing.value), edit.raw !== undefined)
      }
      parent.items[index] = replacement
    } else appendItem(parent, replacement, newline)
  }
  return parent
}

function finalizeCollection(parent: Collection, newline: string): Collection {
  for (const item of parent.items) {
    if (!item.value || !CST.isCollection(item.value)) continue
    const wasBlock = item.value.type !== 'flow-collection'
    item.value = finalizeCollection(item.value, newline)
    if (
      wasBlock &&
      item.key &&
      item.value.type === 'flow-collection' &&
      item.value.items.length === 0 &&
      item.sep?.every((token) => ['map-value-ind', 'space', 'newline'].includes(token.type))
    ) {
      item.sep = [sourceToken('map-value-ind', ':'), sourceToken('space', ' ')]
    }
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

function reorderFlowItems(collection: CST.FlowCollection, order: number[], newline: string) {
  const starts = collection.items.map((item) => item.start.filter((part) => part.type !== 'comma'))
  const tails = collection.items.map((item) => takeTrailing(item.value))
  const commentEnd = (parts: CST.SourceToken[]) =>
    parts.map((part) => part.type).lastIndexOf('comment') + 1
  const multiline = (parts: CST.SourceToken[]) =>
    parts.slice(0, commentEnd(parts)).some((part) => part.type === 'newline')
  starts.forEach((start, index) => {
    const end = start.findIndex((part) => part.type === 'newline')
    if (index > 0 && commentEnd(start.slice(0, end < 0 ? start.length : end)))
      tails[index - 1]?.push(...start.splice(0, end < 0 ? start.length : end))
  })
  const indices = [
    ...order,
    ...collection.items.slice(order.length).map((_, index) => order.length + index),
  ]
  return indices.map((index, position) => {
    const item = collection.items[index]
    if (!item) throw new Error('Missing sequence item.')
    const ownStart = starts[index] ?? []
    const layout = starts[position] ?? []
    const start = commentEnd(ownStart) ? ownStart : layout.slice(commentEnd(layout))
    const previous = tails[indices[position - 1] ?? -1] ?? []
    const comments = multiline(previous) ? [] : previous.slice(0, commentEnd(previous))
    const leading = start.find((part) => part.type === 'newline' || part.type === 'comment')?.type
    const separated =
      leading === 'comment' || (comments.length > 0 && leading !== 'newline')
        ? [
            sourceToken('newline', newline),
            ...(start[0]?.type === 'space' ? [] : [sourceToken('space', ' ')]),
            ...start,
          ]
        : start
    const moved = {
      ...item,
      start: position === 0 ? start : [sourceToken('comma', ','), ...comments, ...separated],
    }
    const ownTail = tails[index] ?? []
    const positionTail = tails[position] ?? []
    const last = position === indices.length - 1
    const tail = last || multiline(ownTail) ? ownTail.slice(0, commentEnd(ownTail)) : []
    let closing = positionTail.slice(commentEnd(positionTail))
    if (!last && multiline(positionTail)) closing = []
    if (!last && multiline(ownTail))
      closing = [
        sourceToken('newline', newline),
        sourceToken('space', ' '.repeat(flowLineIndent(collection).indent)),
      ]
    if (tail.length > 0 && !closing.some((part) => part.type === 'newline'))
      tail.push(sourceToken('newline', newline))
    keepTrailing(moved, [...tail, ...closing], false)
    return moved
  })
}

function renderItem(
  parent: Collection,
  key: string | number,
  value: Node,
  raw: string | undefined,
  json: boolean,
  newline: string,
  valueIndent = parent.indent + 2,
  preservedSuffix?: { tokens: CST.SourceToken[]; index: number },
  inline = false,
): CST.CollectionItem {
  const sequence = isSequence(parent)
  const flow = parent.type === 'flow-collection'
  const keyText = json ? JSON.stringify(key) : yamlKey(key)
  let rendered: string
  if (raw !== undefined && !flow && !sequence) {
    const indent = ' '.repeat(valueIndent)
    const single = raw.trim()
    const definition = raw
      .replace(/\r?\n/g, '\n')
      .split('\n')
      .map((line) => (line ? indent + line : ''))
      .join('\n')
    rendered =
      ' '.repeat(parent.indent) +
      keyText +
      (inline && !single.includes('\n') && rawStartsWithFlowMap(single)
        ? ': ' + single + '\n'
        : ':\n' + definition + (definition.endsWith('\n') ? '' : '\n')) +
      ' '.repeat(parent.indent) +
      '__end__: null\n'
  } else if (raw !== undefined) {
    if (!flow || sequence || !(json ? isJsonSource(raw) : rawStartsWithFlowMap(raw)))
      throw new Error('Cannot insert this definition without changing its syntax.')
    const body = raw.replace(/\r?\n/g, '\n').trimEnd()
    const lastLine = body.slice(body.lastIndexOf('\n') + 1)
    rendered =
      '{' +
      keyText +
      ': ' +
      body.replaceAll('\n', '\n' + ' '.repeat(flowLineIndent(parent).indent)) +
      (!json && lastLine.includes('#') ? '\n' : '') +
      '}'
  } else if (json) {
    const layout = flowLineIndent(parent)
    const unit = layout.multiline ? Math.max(layout.indent - parent.indent, 0) : 0
    const text = JSON.stringify(value.toJSON(), null, unit).replaceAll(
      '\n',
      '\n' + ' '.repeat(layout.indent),
    )
    rendered = sequence ? '[' + text + ']' : '{' + keyText + ':' + text + '}'
  } else {
    const doc = new Document(sequence ? [] : {}, configYamlOptions)
    doc.set(sequence ? 0 : key, value.clone())
    if (flow)
      visit(doc, {
        Scalar(_, scalar) {
          if (String(scalar.value).includes('\n')) scalar.type = 'QUOTE_DOUBLE'
        },
      })
    rendered = doc.toString({
      lineWidth: 0,
      collectionStyle: flow ? 'flow' : 'any',
      ...(flow && { doubleQuotedMinMultiLineLength: Infinity }),
    })
    rendered = rendered.replace(/^/gm, ' '.repeat(parent.indent))
  }
  rendered = rendered.replace(/\r?\n/g, newline)
  const token = parseDocument(rendered, { keepSourceTokens: true }).contents?.srcToken
  if (!token || !CST.isCollection(token) || !token.items[0])
    throw new Error('Cannot render the edited configuration field.')
  const item = token.items[0]
  const sibling = parent.items.find((item) => item.key)
  if (flow && sibling?.sep && !item.sep?.some((token) => token.type === 'comment')) {
    const end = sibling.sep.findIndex((token) => !['map-value-ind', 'space'].includes(token.type))
    item.sep = structuredClone(end < 0 ? sibling.sep : sibling.sep.slice(0, end))
  }
  if (raw !== undefined && !flow && !sequence && item.value && CST.isCollection(item.value)) {
    const tail = token.items[1]?.start ?? []
    if (tail.at(-1)?.type === 'space') tail.pop()
    const suffix = followingSiblingTrivia(item.value)
    if (suffix) suffix.tokens.splice(suffix.index)
    if (preservedSuffix) tail.push(...preservedSuffix.tokens.slice(preservedSuffix.index))
    if (item.value.type === 'flow-collection') item.value.end.push(...tail)
    else item.value.items.push({ start: tail })
  }
  return item
}

export function configNodeSource(source: string, doc: Document, path: string[]) {
  const node = doc.getIn(path, true)
  if (!isNode(node)) return ''
  if (!node.range) throw new Error('Missing definition source range.')
  const parent = doc.getIn(path.slice(0, -1), true)
  const pair = isMap(parent)
    ? parent.items.find((pair) => isScalar(pair.key) && pair.key.value === path.at(-1))
    : undefined
  const start =
    pair?.srcToken?.sep?.find((token) => token.type === 'comment')?.offset ?? node.range[0]
  const indent = node.srcToken && 'indent' in node.srcToken ? node.srcToken.indent : 0
  const suffix = followingSiblingTrivia(node.srcToken)
  const end = suffix?.tokens[suffix.index]?.offset ?? node.range[2]
  return source.slice(start, end).replace(new RegExp(`(^|\\n) {${indent}}`, 'g'), '$1')
}

function followingSiblingTrivia(token: CST.Token | undefined) {
  if (token?.type !== 'block-map') return undefined
  const indent = token.indent
  let current: CST.Token | undefined = token
  while (current && CST.isCollection(current) && current.type !== 'flow-collection') {
    const last: CST.CollectionItem | undefined = current.items.at(-1)
    if (!last) break
    if (!last.key && !last.value) {
      const tokens = last.start
      let index = tokens.findIndex((part) => part.type === 'comment' && part.indent < indent)
      if (index > 0 && tokens[index - 1]?.type === 'space') index -= 1
      if (index < 0 && tokens.at(-1)?.type === 'space') index = tokens.length - 1
      return index >= 0 ? { tokens, index } : undefined
    }
    current = last.value
  }
  return undefined
}

function yamlKey(key: string | number) {
  return new Document(String(key), configYamlOptions).toString().trimEnd()
}

function flowLineIndent(parent: Collection) {
  const items: CST.CollectionItem[] = parent.items
  const sample = [...items].reverse().find((item) => item.key != null || item.value != null)
  const start = sample?.start ?? []
  const newlineAt = start.map((token) => token.type).lastIndexOf('newline')
  const space = newlineAt < 0 ? undefined : start[newlineAt + 1]
  return newlineAt < 0
    ? {
        multiline: false,
        indent: parent.indent + 2,
        layout: start.filter((token) => token.type === 'space'),
      }
    : {
        multiline: true,
        indent: space?.type === 'space' ? space.source.length : 0,
        layout: start
          .slice(newlineAt)
          .filter((token) => token.type === 'newline' || token.type === 'space'),
      }
}

function takeTrailing(token: CST.Token | undefined): CST.SourceToken[] {
  const end = token && 'end' in token && Array.isArray(token.end) ? token.end : undefined
  if (!end) return []
  if (token?.type === 'flow-collection') {
    const close = end.findIndex(
      (part) => part.type === 'flow-map-end' || part.type === 'flow-seq-end',
    )
    return close < 0 ? [] : end.splice(close + 1)
  }
  return end.splice(0)
}

function keepTrailing(
  item: CST.CollectionItem,
  tail: CST.SourceToken[],
  preserveOwnComments = true,
) {
  const own = takeTrailing(item.value)
  const kept = preserveOwnComments && own.some((part) => part.type === 'comment') ? own : []
  const whitespace = preserveOwnComments
    ? tail.slice(tail.map((part) => part.type).lastIndexOf('comment') + 1)
    : tail
  const rest =
    kept.at(-1)?.type === 'newline' && whitespace[0]?.type === 'newline'
      ? whitespace.slice(1)
      : whitespace
  const target = item.value
  if (target?.type === 'flow-collection') target.end.push(...kept, ...rest)
  else if (target && 'end' in target && Array.isArray(target.end)) target.end.push(...kept, ...rest)
  else if (target) Object.assign(target, { end: [...kept, ...rest] })
}

function rawStartsWithFlowMap(source: string) {
  const node = parseDocument(source).contents
  return isMap(node) && node.flow === true
}

function sourceToken(type: CST.SourceToken['type'], source: string): CST.SourceToken {
  return { type, source, indent: 0, offset: -1 }
}

function appendItem(parent: Collection, item: CST.CollectionItem, lineEnding: string) {
  if (parent.type === 'flow-collection') {
    const layout = flowLineIndent(parent)
    const last = parent.items.at(-1)
    const trailing = last && !last.key && !last.value ? parent.items.pop() : undefined
    const sibling = parent.items.at(-1)
    if (sibling) {
      const tail = takeTrailing(sibling.value)
      const commentAt = tail.map((part) => part.type).lastIndexOf('comment')
      const moved = commentAt < 0 ? [] : tail.splice(0, commentAt + 1)
      item.start = [
        sourceToken('comma', ','),
        ...moved,
        ...(moved.length > 0 && !layout.multiline
          ? [sourceToken('newline', lineEnding), sourceToken('space', ' '.repeat(layout.indent))]
          : layout.layout),
        ...item.start.filter((token) => token.type !== 'space'),
      ]
      keepTrailing(item, tail)
    }
    parent.items.push(item)
    if (trailing) parent.items.push(trailing)
    return
  }
  const suffix = followingSiblingTrivia(parent)
  const trailing = suffix?.tokens.splice(suffix.index)
  if (parent.items.length > 0) {
    const newline = CST.stringify(parent).endsWith('\n') ? [] : [sourceToken('newline', lineEnding)]
    item.start = [...newline, sourceToken('space', ' '.repeat(parent.indent)), ...item.start]
  }
  const items: CST.CollectionItem[] = parent.items
  items.push(item)
  if (trailing?.length) items.push({ start: trailing })
}

function removeItem(parent: Collection, index: number) {
  const [removed] = parent.items.splice(index, 1)
  if (!removed) return
  const next = parent.items[index]
  if (parent.type === 'flow-collection') {
    const gap = removed.start.filter((token) => token.type !== 'comma')
    const closing = next
      ? !next.key && !next.value && !next.start.some((token) => token.type === 'comma')
      : CST.stringify(removed).endsWith('\n')
    if (closing && gap.at(-1)?.type === 'space') gap.pop()
    if (next && index === 0) {
      const comment = next.start.findIndex((token) => token.type === 'comment')
      const retained = comment < 0 ? [] : next.start.slice(gap.length === 0 ? 0 : comment)
      next.start = [...gap, ...retained.filter((token) => token.type !== 'comma')]
    } else if (next?.start.some((token) => token.type === 'comma')) {
      const comment = removed.start.map((token) => token.type).lastIndexOf('comment')
      if (comment >= 0) {
        const newline = next.start.findIndex((token) => token.type === 'newline')
        const tail = newline < 0 ? removed.start.slice(comment + 1) : next.start.slice(newline)
        next.start = [...removed.start.slice(0, comment + 1), ...tail]
      }
    } else if (next) next.start = [...gap, ...next.start]
    else parent.end.unshift(...gap)
    if (parent.items[0])
      parent.items[0].start = parent.items[0].start.filter((token) => token.type !== 'comma')
    return
  }
  const suffix = followingSiblingTrivia(removed.value)
  if (suffix) {
    const trivia = suffix.tokens.slice(suffix.index)
    if (next) next.start.unshift(...trivia)
    else parent.items.push({ start: trivia })
  }
  if (index === 0 && next?.start[0]?.type === 'space') next.start.shift()
}
