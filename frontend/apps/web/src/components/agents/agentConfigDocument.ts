import {
  type ConfigSourceFormat,
  parseConfigDocument,
  serializeConfigDocument,
} from '@omnara/sdk/config-source'
import { type Document, isMap, isNode, isScalar, isSeq, type Node } from 'yaml'

import { configPath } from '@/components/agents/agentConfigCompatibility'
import { configJson } from '@/components/agents/agentConfigValidation'

type ConfigPath = (string | number)[]

export type ConfigValue =
  string | number | boolean | null | undefined | ConfigValue[] | ConfigObject
interface ConfigObject {
  [key: string]: ConfigValue
}
type ConfigNodeInput = ConfigValue | Node | ConfigNodeInput[]

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
  format: ConfigSourceFormat,
) {
  let document = original
  function mutableDocument() {
    if (document === original) document = original.clone()
    return document
  }
  const emptiedParents: ConfigPath[] = []
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
  }

  function write(path: ConfigPath, value: ConfigNodeInput, preserveComments: boolean) {
    const node = mutableDocument().createNode(value)
    const previous = document.getIn(path, true)
    if (preserveComments && isNode(previous)) {
      node.commentBefore = previous.commentBefore
      node.comment = previous.comment
    }
    document.setIn(path, node)
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
      emptiedParents.push(parentPath)
    }
  }

  function remove(path: ConfigPath) {
    requireManaged(path)
    deleteAt(path)
  }

  function removeRawDefinition(path: string[]) {
    const index = protectedPaths.findIndex(
      (entry) => configPath(entry.map(String)) === configPath(path),
    )
    if (index < 0)
      throw new ConfigEditError(path, 'This definition is not owned by the advanced editor.')
    deleteAt(path)
    protectedPaths.splice(index, 1)
  }

  function renameRawDefinitions(path: string[], names: Map<string, string>) {
    if (names.size === 0) return
    const parent = mutableDocument().getIn(path, true)
    if (!isMap(parent)) throw new ConfigEditError(path, 'Missing definitions to rename.')
    const destinations = new Set<string>()
    for (const [name, destination] of names) {
      const from = [...path, name]
      if (!parent.has(name) || !protectedPaths.some((item) => deepEqual(item.map(String), from)))
        throw new ConfigEditError(from, 'This definition is not owned by the advanced editor.')
      if (destinations.has(destination) || (parent.has(destination) && !names.has(destination)))
        throw new ConfigEditError([...path, destination], 'This name is already in use.')
      destinations.add(destination)
    }
    for (const pair of parent.items) {
      if (!isScalar(pair.key)) continue
      const renamed = names.get(String(pair.key.value))
      if (renamed !== undefined) pair.key.value = renamed
    }
    for (const protectedPath of protectedPaths) {
      if (!deepEqual(protectedPath.slice(0, -1).map(String), path)) continue
      const renamed = names.get(String(protectedPath.at(-1)))
      if (renamed !== undefined) protectedPath[protectedPath.length - 1] = renamed
    }
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
    if (parsed.errors.length > 0 || !parsed.contents)
      throw new ConfigEditError(path, 'Invalid advanced definition.')
    parsed.contents.commentBefore =
      [parsed.commentBefore, parsed.contents.commentBefore].filter(Boolean).join('\n') || undefined
    parsed.contents.comment =
      [parsed.contents.comment, parsed.comment].filter(Boolean).join('\n') || undefined
    const name = path.at(-1)
    if (
      name !== undefined &&
      path.length > 1 &&
      (parent == null || (isScalar(parent) && parent.value === null))
    )
      set(parentPath, { [name]: null })
    write(path, parsed.contents, false)
  }

  function serialize() {
    if (document === original) return source
    for (const path of emptiedParents) {
      const parent = document.getIn(path, true)
      if (isMap(parent) && parent.items.length === 0) deleteAt(path)
    }
    let result: string
    try {
      result = serializeConfigDocument(document, format)
    } catch (error) {
      if (error instanceof ConfigEditError) throw error
      throw new ConfigEditError(
        [],
        'The Builder cannot safely apply this edit. Your source has been preserved.',
      )
    }
    const written = parseConfigDocument(result, format)
    const blocker = written.diagnostics.find((issue) => issue.kind === 'fatal')
    if (blocker) throw new ConfigEditError(blocker.path, blocker.message)
    const actual = configJson.safeParse(written.doc.toJS())
    const expected = configJson.safeParse(document.toJS())
    if (!actual.success || !expected.success || !deepEqual(actual.data, expected.data))
      throw new ConfigEditError(
        [],
        'The Builder cannot safely apply this edit. Use the source editor to edit this definition.',
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
    renameRawDefinitions,
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

export function isConfigObject(value: ConfigValue): value is ConfigObject {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
}
