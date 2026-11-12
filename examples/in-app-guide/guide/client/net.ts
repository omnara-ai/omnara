// Counts the page's in-flight fetch requests, so the guide can tell "the page is quiet"
// from "the page is waiting on the server" (client-side navigations, form saves). The
// guide's own calls and long-lived streams are not counted.

let installed = false
let inflight = 0
const ignored: string[] = []

export function trackRequests(ignorePrefix: string) {
  if (!ignored.includes(ignorePrefix)) ignored.push(ignorePrefix)
  if (installed || typeof window === 'undefined') return
  installed = true
  const original = window.fetch
  window.fetch = function (input: RequestInfo | URL, init?: RequestInit) {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url
    const accept = new Headers(init?.headers ?? (input instanceof Request ? input.headers : undefined)).get('accept') ?? ''
    if (ignored.some((p) => url.includes(p)) || accept.includes('text/event-stream')) return original.call(this, input, init)
    inflight++
    let done = false
    const finish = () => {
      if (!done) {
        done = true
        inflight--
      }
    }
    // A request that never ends must not block the guide for ever.
    setTimeout(finish, 8000)
    return original.call(this, input, init).finally(finish)
  }
}

export const pendingRequests = () => inflight
