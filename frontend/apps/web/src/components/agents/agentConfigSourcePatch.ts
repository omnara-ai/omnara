import {
  type Document,
  isMap,
  isNode,
  isScalar,
  isSeq,
  type Node,
  type Pair,
  parseDocument,
} from 'yaml'

interface SourceEdit {
  start: number
  end: number
  text: string
}

// Serialize only changed subtrees. Untouched nodes retain their original bytes, including
// comments, flow spacing, quotes, and long schemas that the YAML serializer would reflow.
export function patchConfigSource(
  source: string,
  original: Document,
  updated: Document,
  rawEdits: string[][] = [],
) {
  function retainCollectionStyles(before: Node | null, after: Node | null) {
    if (isMap(before) && isMap(after)) {
      after.flow = before.flow
      for (const pair of before.items) {
        if (isScalar(pair.key)) {
          const nextValue = after.get(pair.key.value, true)
          retainCollectionStyles(
            isNode(pair.value) ? pair.value : null,
            isNode(nextValue) ? nextValue : null,
          )
        }
      }
    } else if (isSeq(before) && isSeq(after)) {
      after.flow = before.flow
      before.items.forEach((item, index) => {
        const nextItem = after.items[index]
        retainCollectionStyles(isNode(item) ? item : null, isNode(nextItem) ? nextItem : null)
      })
    }
  }
  retainCollectionStyles(original.contents, updated.contents)
  const rendered = updated.toString({ lineWidth: 0 })
  const next = parseDocument(rendered)
  const edits: SourceEdit[] = []
  const lineStart = (text: string, offset: number) => text.lastIndexOf('\n', offset - 1) + 1
  const column = (text: string, offset: number) => offset - lineStart(text, offset)
  const fragment = (node: Node, indent: number) => {
    if (!node.range) throw new Error('Missing YAML source range')
    const text = rendered.slice(node.range[0], node.range[1])
    const delta = indent - column(rendered, node.range[0])
    return text.replace(
      /\n( +)/g,
      (_, spaces: string) => '\n' + ' '.repeat(Math.max(0, spaces.length + delta)),
    )
  }
  const explicitlyEdited = (path: string[]) =>
    rawEdits.some((edited) => edited.join('\0') === path.join('\0'))
  function pairFragment(pair: Pair, indent: number) {
    if (!isNode(pair.key) || !pair.key.range || !isNode(pair.value) || !pair.value.range)
      throw new Error('Missing YAML mapping range')
    const delta = indent - column(rendered, pair.key.range[0])
    return rendered
      .slice(pair.key.range[0], pair.value.range[2])
      .replace(
        /\n( +)/g,
        (_, spaces: string) => '\n' + ' '.repeat(Math.max(0, spaces.length + delta)),
      )
  }
  function diff(before: Node | null, after: Node | null, path: string[] = []) {
    if (!before?.range || !after?.range) return
    if (
      before.toString() === after.toString() &&
      !rawEdits.some((edited) => path.every((part, index) => edited[index] === part))
    )
      return
    const [start, end] = before.range
    if (isMap(before) && isMap(after) && before.flow === after.flow) {
      const keyOf = (pair: Pair) => (isScalar(pair.key) ? String(pair.key.value) : '')
      const nextByKey = new Map(after.items.map((pair) => [keyOf(pair), pair]))
      const oldByKey = new Map(before.items.map((pair) => [keyOf(pair), pair]))
      const sameKeys =
        before.items.length === after.items.length &&
        before.items.every((pair) => nextByKey.has(keyOf(pair)))
      if (before.flow && !sameKeys) {
        // Preserve each retained flow entry verbatim; only separators and explicit edits change.
        const entries: string[] = []
        let previousEnd = start + 1
        let pendingGap = ''
        for (const previous of before.items) {
          if (
            !isNode(previous.key) ||
            !previous.key.range ||
            !isNode(previous.value) ||
            !previous.value.range
          )
            throw new Error('Missing YAML mapping range')
          // Gaps contain separators, whitespace and comments. Remove only the separator;
          // comments around an opaque sibling must survive additions and deletions too.
          const gap = source
            .slice(previousEnd, previous.key.range[0])
            .replace(/(^|\n)([^#\n]*),/, '$1$2')
          pendingGap += gap
          previousEnd = previous.value.range[1]
          const pair = nextByKey.get(keyOf(previous))
          if (!pair) continue
          if (explicitlyEdited([...path, keyOf(previous)])) {
            entries.push(
              pendingGap + pairFragment(pair, column(source, previous.key.range[0])).trimEnd(),
            )
            pendingGap = ''
            continue
          }
          const offset = edits.length
          diff(previous.value, isNode(pair.value) ? pair.value : null, [...path, keyOf(previous)])
          const local = edits.splice(offset)
          entries.push(
            pendingGap +
              spliceSource(source, local, previous.key.range[0], previous.value.range[1]),
          )
          pendingGap = ''
        }
        for (const pair of after.items) {
          if (oldByKey.has(keyOf(pair))) continue
          if (!isNode(pair.key) || !pair.key.range || !isNode(pair.value) || !pair.value.range)
            throw new Error('Missing YAML mapping range')
          entries.push(pendingGap + ' ' + rendered.slice(pair.key.range[0], pair.value.range[1]))
          pendingGap = ''
        }
        const suffix = source.slice(previousEnd, end - 1).replace(/(^|\n)([^#\n]*),/, '$1$2')
        edits.push({ start, end, text: '{' + entries.join(',') + pendingGap + suffix + '}' })
        return
      }
      for (const pair of before.items) {
        const nextPair = nextByKey.get(keyOf(pair))
        if (nextPair) {
          if (
            explicitlyEdited([...path, keyOf(pair)]) &&
            isNode(pair.key) &&
            pair.key.range &&
            isNode(pair.value) &&
            pair.value.range
          ) {
            edits.push({
              start: pair.key.range[0],
              end: pair.value.range[2],
              text: pairFragment(nextPair, column(source, pair.key.range[0])),
            })
            continue
          }
          const oldValue = isNode(pair.value) ? pair.value : null
          const newValue = isNode(nextPair.value) ? nextPair.value : null
          if (
            isScalar(oldValue) &&
            (isMap(newValue) || isSeq(newValue)) &&
            !newValue.flow &&
            oldValue.range &&
            isNode(pair.key) &&
            pair.key.range
          ) {
            const indent = column(source, pair.key.range[0]) + 2
            edits.push({
              start: oldValue.range[0],
              end: oldValue.range[1],
              text: '\n' + ' '.repeat(indent) + fragment(newValue, indent).trimEnd(),
            })
          } else diff(oldValue, newValue, [...path, keyOf(pair)])
        } else if (isNode(pair.key) && pair.key.range && isNode(pair.value) && pair.value.range) {
          edits.push({
            start: lineStart(source, pair.key.range[0]),
            end: pair.value.range[2],
            text: '',
          })
        }
      }
      const firstKey = before.items[0]?.key
      const indent =
        isNode(firstKey) && firstKey.range
          ? column(source, firstKey.range[0])
          : column(source, start)
      const additions = after.items
        .filter((pair) => !oldByKey.has(keyOf(pair)))
        .map((pair) => {
          if (!isNode(pair.key) || !pair.key.range || !isNode(pair.value) || !pair.value.range)
            throw new Error('Missing YAML mapping range')
          const text = rendered.slice(pair.key.range[0], pair.value.range[2])
          const delta = indent - column(rendered, pair.key.range[0])
          return (
            ' '.repeat(indent) +
            text.replace(
              /\n( +)/g,
              (_, spaces: string) => '\n' + ' '.repeat(Math.max(0, spaces.length + delta)),
            ) +
            (text.endsWith('\n') ? '' : '\n')
          )
        })
        .join('')
      if (additions)
        edits.push({ start: end, end, text: (source[end - 1] === '\n' ? '' : '\n') + additions })
      return
    }
    if (
      isSeq(before) &&
      isSeq(after) &&
      before.items.length === after.items.length &&
      before.flow === after.flow
    ) {
      before.items.forEach((item, index) => {
        const nextItem = after.items[index]
        diff(isNode(item) ? item : null, isNode(nextItem) ? nextItem : null, [
          ...path,
          String(index),
        ])
      })
      return
    }
    let text = fragment(after, column(source, start))
    if (source.slice(start, end).endsWith('\n') && !text.endsWith('\n')) text += '\n'
    if (!source.slice(start, end).endsWith('\n') && text.endsWith('\n')) text = text.slice(0, -1)
    edits.push({ start, end, text })
  }
  diff(original.contents, next.contents)
  return spliceSource(source, edits, 0, source.length)
}

function spliceSource(source: string, edits: SourceEdit[], start: number, end: number) {
  let text = source.slice(start, end)
  for (const edit of [...edits].reverse().sort((a, b) => b.start - a.start || b.end - a.end)) {
    text = text.slice(0, edit.start - start) + edit.text + text.slice(edit.end - start)
  }
  return text
}
