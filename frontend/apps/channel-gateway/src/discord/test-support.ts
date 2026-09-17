import { once } from 'node:events'
import { createReadStream } from 'node:fs'
import { mkdtemp, rm, writeFile } from 'node:fs/promises'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import type { JsonBody } from '@omnara/sdk'
import { expect, onTestFinished, vi } from 'vitest'
import { z } from 'zod'

import type { CoreClient } from '../core/client'
import type { DiscordUpload } from './client'

export const config = {
  applicationID: '111111111111111111',
  botUserID: '222222222222222222',
  guildID: '333333333333333333',
  botToken: 'discord-local-test-token',
}
export const room = {
  id: '444444444444444444',
  guild_id: config.guildID,
  type: 0,
  name: 'engineering',
}
export const thread = {
  id: '555555555555555555',
  guild_id: config.guildID,
  type: 11,
  name: 'Discussion',
  parent_id: room.id,
}
export const user = { id: config.botUserID, username: 'Omnara', bot: true }
export const message = {
  id: thread.id,
  channel_id: room.id,
  guild_id: config.guildID,
  author: user,
  content: '*hello* <@123456789012345678>',
  timestamp: '2026-09-15T12:00:00.000000+00:00',
  type: 0,
  attachments: [],
  embeds: [],
}
export const suffix = 'aaaaaaaaaaaaaaaaaaaaaaaaae'
export const app = {
  app: {
    id: `iapp_${suffix}`,
    provider: 'discord',
    connector_key: 'omnara',
    provider_app_ref: config.applicationID,
    display_name: 'Omnara',
    provider_config: {},
    provider_metadata: {},
    configuration_revision: 1,
    updated_at: '2026-09-15T12:00:00Z',
  },
  credential: { kind: 'integration_credentials', payload: { bot_token: config.botToken } },
}
export const installation = {
  integration_app_id: app.app.id,
  app_configuration_revision: 1,
  install: {
    id: `iin_${suffix}`,
    project_id: `proj_${suffix}`,
    provider_account_ref: config.botUserID,
    provider_tenant_id: config.guildID,
    display_name: 'Test guild',
    provider_config: {},
    provider_identity: {},
    metadata: {},
    configuration_revision: 1,
    updated_at: '2026-09-15T12:00:00Z',
  },
}
export const destination = {
  implementation_key: 'discord_channel',
  provider_ref_kind: 'channel',
  provider_ref: room.id,
  provider_metadata: {},
}
export const threadDestination = {
  ...destination,
  implementation_key: 'discord_thread',
  provider_ref_kind: 'thread',
  provider_ref: thread.id,
}
export function operation(milliseconds = 5000) {
  return { requestId: 'discord-test-operation', deadlineMs: Date.now() + milliseconds }
}
export function attempt(milliseconds = 5000) {
  return { ...operation(milliseconds), attempt: 1, signal: new AbortController().signal }
}
export function registration() {
  return {
    installation: {
      integration_app_id: app.app.id,
      integration_install_id: installation.install.id,
    },
    publishDefinition: vi
      .fn<CoreClient['publishDefinition']>()
      .mockImplementation((_scope, body) => Promise.resolve({ ...body, id: `cdef_${suffix}` })),
  }
}
export function json(
  response: ServerResponse,
  value: JsonBody,
  status = 200,
  headers: Record<string, string> = {},
) {
  response.writeHead(status, { 'content-type': 'application/json', ...headers })
  response.end(JSON.stringify(value))
}
export function identity(request: IncomingMessage, response: ServerResponse): boolean {
  if (request.url === '/applications/@me') json(response, { id: config.applicationID })
  else if (request.url === '/users/@me') json(response, user)
  else return false
  return true
}
export async function server(
  handler: (request: IncomingMessage, response: ServerResponse) => void,
  botToken = config.botToken,
) {
  const api = createServer((request, response) => {
    expect(request.headers.authorization).toBe(`Bot ${botToken}`)
    handler(request, response)
  })
  api.listen(0, '127.0.0.1')
  await once(api, 'listening')
  onTestFinished(async () => {
    api.closeAllConnections()
    await new Promise<void>((resolve, reject) =>
      api.close((error) => {
        if (error) reject(error)
        else resolve()
      }),
    )
  })
  return `http://127.0.0.1:${z.object({ port: z.number() }).parse(api.address()).port}/`
}
export async function body(request: IncomingMessage) {
  const chunks: Buffer[] = []
  for await (const chunk of request) {
    if (!(chunk instanceof Uint8Array)) throw new Error('expected request bytes')
    chunks.push(Buffer.from(chunk))
  }
  return Buffer.concat(chunks)
}

export function deferred() {
  let resolve!: () => void
  const promise = new Promise<void>((done) => {
    resolve = done
  })
  return { promise, resolve }
}
export async function artifact(bytes: string | Buffer = 'notes'): Promise<DiscordUpload> {
  const directory = await mkdtemp(join(tmpdir(), 'discord-test-'))
  onTestFinished(() => rm(directory, { recursive: true, force: true }))
  const path = join(directory, 'file')
  await writeFile(path, bytes)
  return {
    id: `art_${suffix}`,
    filename: 'notes.txt',
    content_type: 'text/plain',
    sizeBytes: Buffer.byteLength(bytes),
    open: () => createReadStream(path, { highWaterMark: 32 * 1024 }),
  }
}
