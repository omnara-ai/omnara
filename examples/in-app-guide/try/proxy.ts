// Try the guide on an app before changing the app.
//
// This is a local reverse proxy. You open http://localhost:3200, every request is passed
// to your app (APP_URL), and HTML pages get one extra <script> tag that loads the guide.
// The guide's own route (/__guide/api/*) is served here, so the Omnara API key stays in
// this process. Your app's code is untouched.
//
//   pnpm run try        # builds the widget, then starts this proxy
//
// For local trials only. It rewrites Origin and Host so your app accepts the proxied
// requests, which a real integration never needs: there the widget ships inside the app
// and the route is one of the app's own (see README.md).

import http from 'node:http'
import https from 'node:https'
import net from 'node:net'
import { existsSync, readFileSync } from 'node:fs'
import path from 'node:path'
import { Readable } from 'node:stream'
import { fileURLToPath } from 'node:url'
import { createGuideHandler, type GuideUser } from '../guide/server/index.ts'

const root = path.dirname(path.dirname(fileURLToPath(import.meta.url)))
const env = process.env
if (!env.APP_URL) throw new Error('set APP_URL to the app to try the guide on, e.g. http://localhost:3000')
const PORT = Number(env.PORT ?? 3200)
const APP = new URL(env.APP_URL)
const GUIDE_BASE = '/__guide/api'
const EMBED_PATH = '/__guide/embed.js'
const configPath = path.resolve(root, env.GUIDE_AGENT_CONFIG ?? 'guide.agent.yaml')
if (!existsSync(configPath)) throw new Error(`${configPath} is missing: run "pnpm run render" first`)

const attr = (name: string, value?: string) => (value ? ` data-${name}="${value.replace(/"/g, '&quot;')}"` : '')
const TAG =
  `<script src="${EMBED_PATH}" data-endpoint="${GUIDE_BASE}"` +
  attr('title', env.GUIDE_TITLE ?? 'Ask for help') +
  attr('accent', env.GUIDE_ACCENT) +
  attr('suggestions', env.GUIDE_SUGGESTIONS) +
  attr('show-only', env.GUIDE_SHOW_ONLY) +
  '></script>'

/** First string found at any of the given paths, e.g. "user.display_name". */
function pick(obj: unknown, paths: string[]): string | undefined {
  for (const p of paths) {
    const v = p.split('.').reduce<unknown>((o, k) => (o && typeof o === 'object' ? (o as Record<string, unknown>)[k] : undefined), obj)
    if (typeof v === 'string' && v) return v
    if (typeof v === 'number') return String(v)
  }
}

// Who is asking. With GUIDE_ME_PATH set, the proxy asks your app (with the browser's
// cookies) and uses the JSON it returns, so each signed-in user is their own guide user.
// Without it, everyone who opens the proxy is one trial user.
async function getUser(req: Request): Promise<GuideUser | null> {
  if (!env.GUIDE_ME_PATH) {
    return { id: 'trial-user', displayName: env.GUIDE_TRIAL_USER ?? 'Trial user', context: env.GUIDE_TRIAL_CONTEXT ?? 'A signed-in user. Their role is not known; check the page before assuming what they can do.' }
  }
  const cookie = req.headers.get('cookie')
  if (!cookie) return null
  const res = await fetch(new URL(env.GUIDE_ME_PATH, APP), { headers: { cookie, accept: 'application/json' } })
  if (!res.ok) return null
  const me = (await res.json()) as unknown
  const id = pick(me, ['id', 'user.id', 'user_id', 'email', 'user.email'])
  if (!id) return null
  const name = pick(me, ['name', 'display_name', 'user.name', 'user.display_name', 'email', 'user.email'])
  return { id, displayName: name, context: `${name ?? 'A signed-in user'}. What the app says about them: ${JSON.stringify(me).slice(0, 600)}` }
}

const guide = createGuideHandler({
  token: env.OMNARA_API_KEY ?? '',
  orgId: env.OMNARA_ORG_ID ?? '',
  projectId: env.OMNARA_PROJECT_ID ?? '',
  apiUrl: env.OMNARA_API_URL,
  config: readFileSync(configPath, 'utf8'),
  profilePrefix: env.GUIDE_PROFILE_PREFIX ?? 'In-app guide',
  basePath: GUIDE_BASE,
  sessionSecret: env.GUIDE_SESSION_SECRET,
  getUser,
})

function upstreamHeaders(req: http.IncomingMessage) {
  const headers = { ...req.headers, host: APP.host }
  // Ask for uncompressed responses so HTML can be edited.
  delete headers['accept-encoding']
  const self = `http://${req.headers.host}`
  for (const name of ['origin', 'referer'] as const) {
    const v = headers[name]
    if (typeof v === 'string' && v.startsWith(self)) headers[name] = APP.origin + v.slice(self.length)
  }
  return headers
}

async function serveGuide(req: http.IncomingMessage, res: http.ServerResponse) {
  const hasBody = req.method !== 'GET' && req.method !== 'HEAD'
  const abort = new AbortController()
  res.on('close', () => abort.abort())
  const request = new Request(`http://${req.headers.host}${req.url}`, {
    method: req.method,
    headers: req.headers as Record<string, string>,
    body: hasBody ? (Readable.toWeb(req) as ReadableStream) : undefined,
    signal: abort.signal,
    // @ts-expect-error Node needs this for a streamed request body.
    duplex: 'half',
  })
  const response = await guide.GET(request)
  res.writeHead(response.status, Object.fromEntries(response.headers))
  if (!response.body) return void res.end()
  Readable.fromWeb(response.body as never).on('error', () => res.end()).pipe(res)
}

function proxy(req: http.IncomingMessage, res: http.ServerResponse) {
  const out = (APP.protocol === 'https:' ? https : http).request(
    { hostname: APP.hostname, port: APP.port, path: req.url, method: req.method, headers: upstreamHeaders(req) },
    (up) => {
      const headers = { ...up.headers }
      const isHtml = String(headers['content-type'] ?? '').includes('text/html')
      if (!isHtml) {
        res.writeHead(up.statusCode ?? 502, headers)
        return void up.pipe(res)
      }
      const chunks: Buffer[] = []
      up.on('data', (c) => chunks.push(c))
      up.on('end', () => {
        let html = Buffer.concat(chunks).toString('utf8')
        html = html.includes('</body>') ? html.replace('</body>', `${TAG}</body>`) : html + TAG
        delete headers['content-length']
        // The cached copy must not outlive this proxy.
        delete headers.etag
        delete headers['last-modified']
        headers['cache-control'] = 'no-store'
        res.writeHead(up.statusCode ?? 502, headers)
        res.end(html)
      })
    },
  )
  out.on('error', (err) => {
    res.writeHead(502, { 'content-type': 'text/plain' })
    res.end(`The app at ${APP.origin} did not answer: ${err.message}`)
  })
  req.pipe(out)
}

const server = http.createServer((req, res) => {
  const url = req.url ?? '/'
  if (url === EMBED_PATH) {
    const file = path.join(root, 'embed.js')
    if (!existsSync(file)) {
      res.writeHead(404)
      return void res.end('embed.js is missing: run "pnpm run build:embed"')
    }
    res.writeHead(200, { 'content-type': 'text/javascript; charset=utf-8', 'cache-control': 'no-store' })
    return void res.end(readFileSync(file))
  }
  if (url.startsWith(GUIDE_BASE)) {
    return void serveGuide(req, res).catch((err) => {
      console.error('[guide]', err)
      if (!res.headersSent) res.writeHead(500)
      res.end()
    })
  }
  proxy(req, res)
})

// WebSockets pass straight through (plain-HTTP apps only).
server.on('upgrade', (req, socket, head) => {
  const up = net.connect(Number(APP.port) || 80, APP.hostname, () => {
    const lines = [`${req.method} ${req.url} HTTP/1.1`]
    for (const [k, v] of Object.entries(upstreamHeaders(req))) {
      for (const one of Array.isArray(v) ? v : [v]) if (one !== undefined) lines.push(`${k}: ${one}`)
    }
    up.write(lines.join('\r\n') + '\r\n\r\n')
    if (head.length) up.write(head)
    up.pipe(socket)
    socket.pipe(up)
  })
  up.on('error', () => socket.destroy())
  socket.on('error', () => up.destroy())
})

server.listen(PORT, () => console.log(`your app with the guide: http://localhost:${PORT}  (proxying ${APP.origin})`))
