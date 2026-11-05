import { type Document, isMap, isNode, isScalar, isSeq, type Node, parseDocument } from 'yaml'
import { z } from 'zod'

import { configPath, parseConfigDocument } from '@/components/agents/agentConfigCompatibility'
import {
  type ConfigSourceFormat,
  configSourceFormat,
  isJsonSource,
} from '@/components/agents/agentConfigSourceFormat'
import {
  type ConfigEdit,
  type ConfigPath,
  patchConfigSource,
} from '@/components/agents/agentConfigSourcePatch'
import { configYamlOptions } from '@/components/agents/agentConfigYaml'

export type ConfigValue =
  string | number | boolean | null | undefined | ConfigValue[] | ConfigObject
export interface ConfigObject {
  [key: string]: ConfigValue
}
export type ConfigNodeInput = ConfigValue | Node | ConfigNodeInput[]

export class ConfigEditError extends Error {
  constructor(
    readonly path: ConfigPath,
    message: string,
  ) {
    super(message)
  }
}

export function editConfigDocument(
  source: string,
  original: Document,
  preservedPaths: string[][],
  format: ConfigSourceFormat = configSourceFormat(source),
) {
  let document = original
  function mutableDocument() {
    if (document === original) document = original.clone()
    return document
  }
  const edits: ConfigEdit[] = []
  const protectedPaths: ConfigPath[] = preservedPaths.map((path) => [...path])

  function protectedPathAt(path: ConfigPath) {
    return protectedPaths.find(
      (protectedPath) =>
        protectedPath.every((part, index) => String(path[index]) === String(part)) ||
        path.every((part, index) => String(protectedPath[index]) === String(part)),
    )
  }

  function requireManaged(path: ConfigPath) {
    const protectedPath = protectedPathAt(path)
    if (protectedPath)
      throw new ConfigEditError(
        protectedPath,
        'This definition is preserved. Edit it explicitly in the source editor.',
      )
  }

  function orderSequence(path: ConfigPath, currentIds: string[], desiredIds: string[]) {
    if (deepEqual(currentIds, desiredIds)) return
    const sequence = document.getIn(path, true)
    if (!isSeq(sequence)) throw new ConfigEditError(path, 'Missing sequence.')
    const owned = sequence.items.flatMap((_, index) =>
      protectedPathAt([...path, index]) ? [] : [index],
    )
    const byId = new Map<string, number[]>()
    currentIds.forEach((id, index) => {
      const indices = byId.get(id) ?? []
      const sourceIndex = owned[index]
      if (sourceIndex !== undefined) indices.push(sourceIndex)
      byId.set(id, indices)
    })
    const order = sequence.items.map((_, index) => index)
    desiredIds.forEach((id, index) => {
      const sourceIndex = byId.get(id)?.shift()
      const targetIndex = owned[index]
      if (sourceIndex === undefined || targetIndex === undefined)
        throw new ConfigEditError(path, 'Cannot identify the reordered entry.')
      order[targetIndex] = sourceIndex
    })
    if (new Set(order).size !== order.length)
      throw new ConfigEditError(path, 'Cannot identify the reordered entries.')
    const target = mutableDocument().getIn(path, true)
    if (!isSeq(target)) throw new ConfigEditError(path, 'Missing sequence.')
    target.items = order.map((index) => target.items[index])
    edits.push({ type: 'reorder', path, order })
  }

  function write(path: ConfigPath, value: ConfigNodeInput, preserveComments: boolean) {
    const node = mutableDocument().createNode(value)
    const previous = document.getIn(path, true)
    if (preserveComments && isNode(previous)) {
      node.commentBefore = previous.commentBefore
      node.comment = previous.comment
      if (isScalar(node) && isScalar(previous)) {
        const text = z.string().safeParse(node.value)
        if (
          previous.type === 'BLOCK_LITERAL' ||
          previous.type === 'BLOCK_FOLDED' ||
          !text.success ||
          !text.data.includes('\n')
        )
          node.type = previous.type
      }
    }
    const snapshot = node.clone()
    if (!isNode(snapshot)) throw new ConfigEditError(path, 'Cannot copy the edited field.')
    document.setIn(path, node)
    edits.push({ type: 'set', path, value: snapshot })
  }

  function set(path: ConfigPath, value: ConfigNodeInput) {
    requireManaged(path)
    for (let length = 1; length < path.length; length += 1) {
      const parentPath = path.slice(0, length)
      const parent = document.getIn(parentPath, true)
      if (parent == null || (isScalar(parent) && parent.value === null)) {
        let nested = value
        for (const key of path.slice(length).reverse())
          nested = mutableDocument().createNode({ [key]: nested })
        write(parentPath, nested, true)
        return
      }
    }
    write(path, value, true)
  }

  function deleteAt(path: ConfigPath) {
    if (!document.hasIn(path)) return
    mutableDocument().deleteIn(path)
    edits.push({ type: 'delete', path })
    const parentPath = path.slice(0, -1)
    const parent = document.getIn(parentPath, true)
    if (isSeq(parent)) {
      for (const protectedPath of protectedPaths) {
        if (
          parentPath.every((part, index) => String(part) === String(protectedPath[index])) &&
          Number(protectedPath[parentPath.length]) > Number(path.at(-1))
        ) {
          protectedPath[parentPath.length] = Number(protectedPath[parentPath.length]) - 1
        }
      }
    }
    if (path.length === 2 && isMap(parent) && parent.items.length === 0) {
      document.deleteIn(parentPath)
      edits.push({ type: 'delete', path: parentPath })
    }
  }

  function remove(path: ConfigPath) {
    requireManaged(path)
    deleteAt(path)
  }

  function removeRawDefinition(path: string[]) {
    if (!protectedPaths.some((entry) => configPath(entry.map(String)) === configPath(path)))
      throw new ConfigEditError(path, 'This definition is not owned by the advanced editor.')
    deleteAt(path)
  }

  function setRawDefinition(path: string[], raw: string) {
    const preserved = protectedPaths.some(
      (entry) => configPath(entry.map(String)) === configPath(path),
    )
    if (!preserved && document.hasIn(path))
      throw new ConfigEditError(path, 'This definition is not owned by the advanced editor.')
    if (!preserved) requireManaged(path)
    const { doc: parsed, diagnostics } = parseConfigDocument(raw, format)
    const parentPath = path.slice(0, -1)
    const parent = document.getIn(parentPath, true)
    const blocker = diagnostics.find((issue) => issue.kind === 'fatal')
    if (blocker) throw new ConfigEditError(path, blocker.message)
    const needsConversion =
      [document.contents, parent].some((node) => isMap(node) && node.flow) &&
      !(isMap(parsed.contents) && parsed.contents.flow)
    if (needsConversion)
      throw new ConfigEditError(
        path,
        'This inline collection requires a flow definition using { ... }. Edit the collection in the source editor to use block YAML.',
      )
    if (parsed.errors.length > 0 || !parsed.contents)
      throw new ConfigEditError(path, 'Invalid advanced definition.')
    const name = path.at(-1)
    if (
      name !== undefined &&
      path.length > 1 &&
      (parent == null || (isScalar(parent) && parent.value === null))
    )
      set(parentPath, { [name]: null })
    write(path, parsed.contents, false)
    const edit = edits.at(-1)
    if (edit?.type === 'set') edit.raw = raw
  }

  function serialize() {
    if (edits.length === 0) return source
    let result: string
    try {
      const initial =
        source ||
        (format === 'json' ? JSON.stringify(document.toJS(), null, 2) : document.toString())
      result = patchConfigSource(
        initial,
        source === '' ? parseDocument(initial, configYamlOptions) : original,
        source === ''
          ? edits.filter((edit) => edit.type === 'set' && edit.raw !== undefined)
          : edits,
        format,
      )
    } catch (error) {
      if (error instanceof ConfigEditError) throw error
      throw new ConfigEditError(
        edits.at(-1)?.path ?? [],
        error instanceof Error ? error.message : 'Cannot patch this field.',
      )
    }
    const parsed = parseDocument(result)
    if (
      (format === 'json' && !isJsonSource(result)) ||
      parsed.errors.length > 0 ||
      !deepEqual(z.json().parse(parsed.toJS()), z.json().parse(document.toJS()))
    )
      throw new ConfigEditError(
        edits.at(-1)?.path ?? [],
        'The Builder cannot safely patch this edit. Use the source editor to edit this definition.',
      )
    return result
  }

  return {
    get document() {
      return document
    },
    set,
    remove,
    orderSequence,
    setRawDefinition,
    removeRawDefinition,
    serialize,
  }
}

export function deepEqual(a: ConfigValue, b: ConfigValue): boolean {
  if (Object.is(a, b)) return true
  if (Array.isArray(a) || Array.isArray(b)) {
    if (!Array.isArray(a) || !Array.isArray(b) || a.length !== b.length) return false
    return a.every((value, index) => deepEqual(value, b[index]))
  }
  if (!isConfigObject(a) || !isConfigObject(b)) return false
  const left = Object.entries(a)
  const right = new Map(Object.entries(b))
  return (
    left.length === right.size &&
    left.every(([key, value]) => right.has(key) && deepEqual(value, right.get(key)))
  )
}

function isConfigObject(value: ConfigValue): value is ConfigObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
