import { z } from 'zod'

const jsonValue = z.json()
const textBody = z.string()

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

function requestUrl(input: RequestInfo | URL): URL {
  const href = input instanceof Request ? input.url : String(input)
  return new URL(href, window.location.href)
}

function requestBody(init: RequestInit | undefined): JsonValue | undefined {
  const text = textBody.safeParse(init?.body)
  return text.success && text.data !== '' ? jsonValue.parse(JSON.parse(text.data)) : undefined
}

export function fakeApi(routes: FakeRoute[]): FakeApi {
  const requests: RecordedRequest[] = []
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const method = init?.method ?? (input instanceof Request ? input.method : 'GET')
    const request = {
      method: method.toUpperCase(),
      url: requestUrl(input),
      body: requestBody(init),
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
