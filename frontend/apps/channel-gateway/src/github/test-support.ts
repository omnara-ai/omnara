import { generateKeyPairSync } from 'node:crypto'
import { once } from 'node:events'
import { readFileSync } from 'node:fs'
import { createServer, type IncomingMessage, type ServerResponse } from 'node:http'
import { gunzipSync } from 'node:zlib'

import type { JsonBody } from '@omnara/sdk'
import {
  buildSchema,
  getOperationAST,
  getVariableValues,
  type GraphQLSchema,
  parse,
  validate,
} from 'graphql'
import { afterEach } from 'vitest'
import { z } from 'zod'

import { GitHubClient } from './client'
import type { GitHubConfiguration } from './configuration'

const keyPair = generateKeyPairSync('rsa', { modulusLength: 2048 })
export const publicKey = keyPair.publicKey
export const configuration: GitHubConfiguration = {
  appID: '42',
  privateKey: keyPair.privateKey.export({ type: 'pkcs8', format: 'pem' }).toString(),
  webhookSecret: 'local-webhook-secret',
  installationID: 123,
  repositoryID: 456,
  repositoryNodeID: 'R_selected',
  repositoryOwner: 'example',
  repositoryName: 'project',
  projectID: 'prj_example',
  integrationInstallID: 'inst_example',
}
export const oldCommit = 'a'.repeat(40)
export const newCommit = 'b'.repeat(40)
export const pr = { id: 'PR_selected', number: 7, repository: { id: 'R_selected' } }
export const comment = {
  id: 'IC_1',
  body: '**Original** `x` @example',
  author: { login: 'example[bot]' },
  createdAt: '2026-09-15T12:00:00Z',
}
export const review = {
  ...comment,
  id: 'PRR_1',
  state: 'PENDING',
  commit: { oid: oldCommit },
  submittedAt: null,
}
export const finding = {
  ...comment,
  id: 'PRRC_1',
  fullDatabaseId: '9007199254740993',
  state: 'PENDING',
  path: 'src/main.ts',
  line: 12,
  originalCommit: { oid: oldCommit },
  pullRequestReview: { id: 'PRR_1', state: 'PENDING' },
  replyTo: null,
}
export const thread = { id: 'PRRT_1', pullRequest: pr, comments: { nodes: [finding] } }
export const noPrevious = { hasPreviousPage: false, startCursor: 'cursor-1' }

export function attempt(signal = new AbortController().signal, milliseconds = 10_000) {
  return { attempt: 1, requestId: 'tc_fixture', deadlineMs: Date.now() + milliseconds, signal }
}

const servers: ReturnType<typeof createServer>[] = []
afterEach(async () => {
  await Promise.all(
    servers.splice(0).map(async (server) => {
      const closed = once(server, 'close')
      server.closeAllConnections()
      server.close()
      await closed
    }),
  )
})
export async function localServer(
  handler: (request: IncomingMessage, response: ServerResponse) => Promise<void> | void,
) {
  const server = createServer((request, response) => {
    void Promise.resolve(handler(request, response)).catch(() => {
      response.writeHead(500)
      response.end()
    })
  })
  servers.push(server)
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const address = z.object({ port: z.number() }).parse(server.address())
  return `http://127.0.0.1:${address.port}`
}
export function json(response: ServerResponse, value: JsonBody, status = 200) {
  response.writeHead(status, { 'content-type': 'application/json' })
  response.end(JSON.stringify(value))
}
export async function requestBody(request: IncomingMessage): Promise<JsonBody> {
  const chunks: Buffer[] = []
  for await (const chunk of request) chunks.push(Buffer.from(z.instanceof(Uint8Array).parse(chunk)))
  return z.json().parse(JSON.parse(Buffer.concat(chunks).toString('utf8')))
}
const graphRequest = z.object({ query: z.string(), variables: z.record(z.string(), z.json()) })
export type GraphRequest = z.infer<typeof graphRequest>
export interface APICall {
  path: string
  authorization?: string
  body: JsonBody | undefined
}

let nativeSchema: GraphQLSchema | undefined
function nativeRequestErrors(request: GraphRequest): string[] {
  nativeSchema ??= buildSchema(
    gunzipSync(
      readFileSync(new URL('./testdata/schema.docs.graphql.gz', import.meta.url)),
    ).toString('utf8'),
  )
  const document = parse(request.query)
  const errors = validate(nativeSchema, document)
  if (errors.length) return errors.map((error) => error.message)
  const operation = getOperationAST(document)
  if (!operation) return ['missing operation']
  return (
    getVariableValues(
      nativeSchema,
      operation.variableDefinitions ?? [],
      request.variables,
    ).errors?.map((error) => error.message) ?? []
  )
}

export async function githubFixture(
  handler: (request: GraphRequest, response: ServerResponse) => Promise<void> | void,
  rest?: (call: APICall, response: ServerResponse) => Promise<void> | void,
) {
  const calls: APICall[] = []
  const url = await localServer(async (request, response) => {
    const body = request.method === 'POST' ? await requestBody(request) : undefined
    const call: APICall = {
      path: request.url ?? '',
      authorization: request.headers.authorization,
      body,
    }
    calls.push(call)
    if (request.url === '/app/installations/123/access_tokens') {
      json(response, {
        token: 'local-installation-token',
        expires_at: new Date(Date.now() + 3_600_000).toISOString(),
      })
    } else if (request.url === '/repositories/456') {
      json(response, {
        id: 456,
        node_id: 'R_selected',
        name: 'renamed',
        owner: { login: 'new-owner' },
      })
    } else if (request.url === '/graphql') {
      const parsed = graphRequest.parse(body)
      const errors = nativeRequestErrors(parsed)
      if (errors.length) {
        json(
          response,
          { errors: errors.map((message) => ({ type: 'GRAPHQL_VALIDATION_FAILED', message })) },
          400,
        )
        return
      }
      if (parsed.query.startsWith('query GitHubPullRequest(')) {
        json(response, {
          data: {
            node: {
              id: 'R_selected',
              pullRequest: { ...pr, title: 'Example', state: 'CLOSED', headRefOid: newCommit },
            },
          },
        })
      } else await handler(parsed, response)
    } else if (rest) await rest(call, response)
    else json(response, {}, 404)
  })
  return { client: new GitHubClient(configuration, url), calls, url }
}

export function mutationInputs(calls: APICall[]) {
  return calls.flatMap((call) => {
    const parsed = graphRequest.safeParse(call.body)
    return parsed.success && parsed.data.query.startsWith('mutation ')
      ? [parsed.data.variables.input]
      : []
  })
}
