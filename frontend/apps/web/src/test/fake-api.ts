import { zJsonText } from '@omnara/sdk'
import { z } from 'zod'

const jsonValue = z.json()

export type JsonValue = z.infer<typeof jsonValue>

export interface RecordedRequest {
  method: string
  url: URL
  body: JsonValue | undefined
}

export interface FakeRoute {
  method: string
  path: string
  respond: (request: RecordedRequest) => Response | Promise<Response>
}

export interface FakeApi {
  fetch: typeof globalThis.fetch
  requests: RecordedRequest[]
  requestsTo: (method: string, path: string) => RecordedRequest[]
}

export function jsonResponse(body: JsonValue, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json' },
  })
}

export function emptyResponse(status = 204): Response {
  return new Response(null, { status })
}

export function neverResponds(): Promise<Response> {
  return new Promise(() => undefined)
}

const jsonBody = zJsonText.pipe(jsonValue)

async function requestBody(request: Request): Promise<JsonValue | undefined> {
  const text = await request.text()
  return text === '' ? undefined : jsonBody.parse(text)
}

export function fakeApi(routes: FakeRoute[]): FakeApi {
  const requests: RecordedRequest[] = []
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const incoming = new Request(input, init)
    const request = {
      method: incoming.method.toUpperCase(),
      url: new URL(incoming.url, window.location.href),
      body: await requestBody(incoming),
    }
    requests.push(request)
    const route = routes.find(
      (candidate) =>
        candidate.method.toUpperCase() === request.method &&
        candidate.path === request.url.pathname,
    )
    if (!route) {
      return jsonResponse(
        {
          code: 'not_found',
          message: `No fake route for ${request.method} ${request.url.pathname}`,
        },
        404,
      )
    }
    return route.respond(request)
  }
  return {
    fetch,
    requests,
    requestsTo: (method, path) =>
      requests.filter(
        (request) => request.method === method.toUpperCase() && request.url.pathname === path,
      ),
  }
}
