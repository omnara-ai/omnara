import { type ChannelConnectorCapability, type JsonBody, schemas } from '@omnara/sdk'

import { isString } from '../diagnostics'
import { parseObjectFields } from '../json'

export const maxOperationEnvelopeBytes = 512 * 1024
export const maxOperationPayloadBytes = 256 * 1024
export const maxOperationArtifacts = 20
export const maxOperationResponseBytes = 1024 * 1024

export type OperationKind = 'send' | 'read' | 'interaction' | 'resolve_address'

// Wire names mirror internal/channelconnector/operations.go's transport envelope.
// Use generated operation types here once the shared schema owner publishes them.
export interface InstallationOperationScope {
  project_id: string
  integration_app_id: string
  integration_install_id: string
}

export interface OperationScope extends InstallationOperationScope {
  agent_id: string
  channel_id: string
}

export interface OperationArtifactMetadata {
  id: string
  filename: string
  content_type: string
}

/** Transport validation only; the executor owns the operation-specific schema. */
interface GatewayOperationBase {
  requestId: string
  capability: ChannelConnectorCapability
  deadlineMs: number
  /** Original object JSON, preserving integers that cannot fit a JS number. */
  payloadJSON: string
  artifacts: readonly OperationArtifactMetadata[]
}

export type GatewayOperation = GatewayOperationBase &
  (
    | { kind: 'send' | 'read' | 'interaction'; scope: OperationScope }
    | {
        kind: 'resolve_address'
        scope: InstallationOperationScope & { agent_id?: never; channel_id?: never }
      }
  )

export function isReadOnlyOperation(kind: OperationKind): boolean {
  return kind === 'read' || kind === 'resolve_address'
}

export class InvalidOperationError extends Error {
  constructor() {
    super('invalid channel operation')
  }
}

const registryName = /^[a-z0-9][a-z0-9_.-]{0,127}$/

export function capabilityKey(capability: ChannelConnectorCapability): string {
  if (!registryName.test(capability.connector_key) || !registryName.test(capability.provider)) {
    throw new InvalidOperationError()
  }
  return `${capability.connector_key}\u0000${capability.provider}`
}

export function parseOperation(
  raw: string,
  allowedCapabilities: ReadonlySet<string>,
  deadlineCeilingMs: number,
): GatewayOperation {
  const fields = parseObjectFields(raw, maxOperationEnvelopeBytes)
  exactFields(fields, [
    'request_id',
    'capability',
    'kind',
    'scope',
    'deadline',
    'payload',
    'artifacts',
  ])
  const capabilityFields = parseObjectFields(required(fields, 'capability'), 1024)
  exactFields(capabilityFields, ['connector_key', 'provider'])
  const capability = {
    connector_key: textField(capabilityFields, 'connector_key', 128),
    provider: textField(capabilityFields, 'provider', 128),
  }
  if (!allowedCapabilities.has(capabilityKey(capability))) throw new InvalidOperationError()
  const kind = textField(fields, 'kind', 32)
  if (kind !== 'send' && kind !== 'read' && kind !== 'interaction' && kind !== 'resolve_address')
    throw new InvalidOperationError()
  const scopeFields = parseObjectFields(required(fields, 'scope'), 8192)
  const scopeNames = ['project_id', 'integration_app_id', 'integration_install_id']
  exactFields(
    scopeFields,
    kind === 'resolve_address' ? scopeNames : [...scopeNames, 'agent_id', 'channel_id'],
  )
  const deadline = textField(fields, 'deadline', 64)
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?Z$/.test(deadline)) {
    throw new InvalidOperationError()
  }
  const parsedDeadline = Date.parse(deadline)
  if (!Number.isFinite(parsedDeadline)) throw new InvalidOperationError()
  // Reject Date.parse's normalization of impossible calendar dates.
  if (new Date(parsedDeadline).toISOString().slice(0, 19) !== deadline.slice(0, 19)) {
    throw new InvalidOperationError()
  }
  const deadlineMs = Math.min(parsedDeadline, deadlineCeilingMs)
  const payloadJSON = required(fields, 'payload')
  parseObjectFields(payloadJSON, maxOperationPayloadBytes)
  const artifactsRaw = fields.get('artifacts') ?? '[]'
  const parsedArtifacts: unknown = JSON.parse(artifactsRaw)
  if (!Array.isArray(parsedArtifacts) || parsedArtifacts.length > maxOperationArtifacts) {
    throw new InvalidOperationError()
  }
  const artifacts: OperationArtifactMetadata[] = []
  const ids = new Set<string>()
  for (const artifact of parsedArtifacts) {
    const parsed = schemas.zChannelOpaqueObject.safeParse(artifact)
    if (
      !parsed.success ||
      Object.keys(parsed.data).some((key) => !['id', 'filename', 'content_type'].includes(key))
    ) {
      throw new InvalidOperationError()
    }
    const { id, filename, content_type: contentType } = parsed.data
    if (
      !isBoundedText(id, 256) ||
      ids.has(id) ||
      !isBoundedText(filename, 255) ||
      filename.includes('/') ||
      filename.includes('\\') ||
      !isBoundedText(contentType, 255) ||
      !/^[a-zA-Z0-9!#$%&'*+.^_`|~-]+\/[a-zA-Z0-9!#$%&'*+.^_`|~-]+$/.test(contentType)
    ) {
      throw new InvalidOperationError()
    }
    ids.add(id)
    artifacts.push({ id, filename, content_type: contentType.toLowerCase() })
  }
  if (isReadOnlyOperation(kind) && artifacts.length !== 0) throw new InvalidOperationError()
  const common = {
    requestId: textField(fields, 'request_id', 256),
    capability,
    deadlineMs,
    payloadJSON,
    artifacts,
  }
  const scope = {
    project_id: textField(scopeFields, 'project_id', 256),
    integration_app_id: textField(scopeFields, 'integration_app_id', 256),
    integration_install_id: textField(scopeFields, 'integration_install_id', 256),
  }
  if (kind === 'resolve_address') return { ...common, kind, scope }
  return {
    ...common,
    kind,
    scope: {
      ...scope,
      agent_id: textField(scopeFields, 'agent_id', 256),
      channel_id: textField(scopeFields, 'channel_id', 256),
    },
  }
}

function required(fields: ReadonlyMap<string, string>, key: string): string {
  const raw = fields.get(key)
  if (raw === undefined) throw new InvalidOperationError()
  return raw
}

function textField(fields: ReadonlyMap<string, string>, key: string, maxBytes: number): string {
  const value: unknown = JSON.parse(required(fields, key))
  if (!isBoundedText(value, maxBytes)) throw new InvalidOperationError()
  return value
}

function isBoundedText(value: unknown, maxBytes: number): value is string {
  return (
    typeof value === 'string' &&
    value.length !== 0 &&
    Buffer.byteLength(value) <= maxBytes &&
    value.trim() === value &&
    !value.includes('\u0000') &&
    !value.includes('\r') &&
    !value.includes('\n')
  )
}

function exactFields(fields: ReadonlyMap<string, string>, allowed: readonly string[]): void {
  for (const key of fields.keys()) if (!allowed.includes(key)) throw new InvalidOperationError()
}

/** Serialize plain JSON while enforcing the response budget before allocating
 * a whole response. Provider result objects must contain data, not accessors or
 * custom toJSON hooks. The same depth/node limits apply to received envelopes.
 */
export function serializeOperationResult(value: JsonBody): string {
  const pieces: string[] = []
  let bytes = 0
  let nodes = 0
  const append = (text: string): void => {
    bytes += Buffer.byteLength(text)
    if (bytes > maxOperationResponseBytes) throw new InvalidOperationError()
    pieces.push(text)
  }
  const string = (text: string): void => {
    // Even before escaping, each UTF-16 code unit requires at least one byte.
    if (text.length > maxOperationResponseBytes - bytes) throw new InvalidOperationError()
    append(JSON.stringify(text))
  }
  const visit = (entry: JsonBody, depth: number): void => {
    if (++nodes > 16_384 || depth > 128) throw new InvalidOperationError()
    if (isString(entry)) {
      string(entry)
    } else if (entry === null || entry === true || entry === false) {
      append(String(entry))
    } else if (isJSONNumber(entry) && Number.isFinite(entry)) {
      append(JSON.stringify(entry))
    } else if (Array.isArray(entry)) {
      if (entry.length > 16_384 - nodes) throw new InvalidOperationError()
      append('[')
      for (let i = 0; i < entry.length; i += 1) {
        if (i !== 0) append(',')
        const descriptor = Object.getOwnPropertyDescriptor(entry, String(i))
        if (!descriptor || !('value' in descriptor)) throw new InvalidOperationError()
        const child = entry[i]
        if (child === undefined) throw new InvalidOperationError()
        visit(child, depth + 1)
      }
      append(']')
    } else if (isPlainJSONObject(entry)) {
      append('{')
      let first = true
      for (const key in entry) {
        if (!Object.hasOwn(entry, key)) continue
        if (!first) append(',')
        first = false
        string(key)
        append(':')
        const descriptor = Object.getOwnPropertyDescriptor(entry, key)
        if (!descriptor || !('value' in descriptor)) throw new InvalidOperationError()
        const child = entry[key]
        if (child === undefined) throw new InvalidOperationError()
        visit(child, depth + 1)
      }
      append('}')
    } else {
      throw new InvalidOperationError()
    }
  }
  visit(value, 0)
  return pieces.join('')
}

function isJSONNumber(value: JsonBody): value is number {
  return typeof value === 'number'
}

function isPlainJSONObject(value: JsonBody): value is Record<string, JsonBody> {
  if (typeof value !== 'object' || value === null || Array.isArray(value)) return false
  const prototype: unknown = Object.getPrototypeOf(value)
  return prototype === null || prototype === Object.prototype
}
