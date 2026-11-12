#!/usr/bin/env node
// Uploads your markdown files to the Omnara memory store the guide reads. Only needed
// when guide.config.json has knowledge source "files" (see lib-knowledge.mjs). Run it
// again whenever the files change: unchanged files are skipped, changed ones are
// replaced, and files removed locally are deleted from the store.
//
//   node --env-file=.env scripts/sync-knowledge.mjs [--config guide.config.json]
//
// Needs OMNARA_API_KEY, OMNARA_ORG_ID and OMNARA_PROJECT_ID.

import { createHash } from 'node:crypto'
import { readFile } from 'node:fs/promises'
import path from 'node:path'
import { listKnowledgeFiles } from './lib-knowledge.mjs'

const i = process.argv.indexOf('--config')
const configPath = path.resolve(i > 0 ? process.argv[i + 1] : 'guide.config.json')
const config = JSON.parse(await readFile(configPath, 'utf8'))
const k = config.knowledge
if (k?.source !== 'files') {
  console.log(`knowledge source is "${k?.source ?? 'none'}": nothing to upload`)
  process.exit(0)
}
const { OMNARA_API_KEY, OMNARA_ORG_ID, OMNARA_PROJECT_ID, OMNARA_API_URL = 'https://api.omnara.com/v1' } = process.env
if (!OMNARA_API_KEY || !OMNARA_ORG_ID || !OMNARA_PROJECT_ID) {
  console.error('set OMNARA_API_KEY, OMNARA_ORG_ID and OMNARA_PROJECT_ID (for example with --env-file=.env)')
  process.exit(1)
}

const base = `${OMNARA_API_URL.replace(/\/$/, '')}/orgs/${OMNARA_ORG_ID}/projects/${OMNARA_PROJECT_ID}`
async function api(method, url, body, headers = {}) {
  const res = await fetch(base + url, { method, body, headers: { Authorization: `Bearer ${OMNARA_API_KEY}`, ...headers } })
  if (!res.ok) throw new Error(`${method} ${url.split('?')[0]}: HTTP ${res.status} ${(await res.text()).slice(0, 200)}`)
  return res
}
const digestOf = (bytes) => `sha256:${createHash('sha256').update(bytes).digest('hex')}`

// Find the store, or create it read-only for agents.
let store = null
for (let cursor = ''; !store; ) {
  const page = await (await api('GET', `/memory-stores?limit=100${cursor && `&cursor=${encodeURIComponent(cursor)}`}`)).json()
  store = page.data.find((s) => s.name === k.store) ?? null
  if (!page.next_cursor) break
  cursor = page.next_cursor
}
if (!store) {
  const body = JSON.stringify({ name: k.store, description: `${config.product} product knowledge for the in-app guide`, agent_access: 'read' })
  store = await (await api('POST', '/memory-stores', body, { 'Content-Type': 'application/json' })).json()
  console.log(`created memory store ${k.store} (${store.id})`)
}
const files = `/memory-stores/${store.id}`

// What the store holds now.
const remote = new Set()
async function walk(dir) {
  for (let cursor = ''; ; ) {
    const q = new URLSearchParams({ limit: '100', ...(dir && { path: dir }), ...(cursor && { cursor }) })
    const page = await (await api('GET', `${files}/files?${q}`)).json()
    for (const f of page.data) f.type === 'directory' ? await walk(f.path) : remote.add(f.path)
    if (!page.next_cursor) break
    cursor = page.next_cursor
  }
}
await walk('')

const dir = path.resolve(path.dirname(configPath), k.dir)
const local = await listKnowledgeFiles(dir, k.exclude)
const remoteDigest = async (rel) => digestOf(Buffer.from(await (await api('GET', `${files}/file?path=${encodeURIComponent(rel)}`)).arrayBuffer()))
let uploaded = 0
let unchanged = 0
for (const rel of local) {
  const bytes = await readFile(path.join(dir, rel))
  const q = new URLSearchParams({ path: rel })
  if (remote.has(rel)) {
    // Replacing a file needs the digest of what is there now.
    const current = await remoteDigest(rel)
    if (current === digestOf(bytes)) {
      unchanged++
      continue
    }
    q.set('expected_digest', current)
  }
  await api('PUT', `${files}/file?${q}`, bytes, { 'Content-Type': 'application/octet-stream' })
  uploaded++
}
let deleted = 0
for (const rel of remote) {
  if (local.includes(rel)) continue
  await api('DELETE', `${files}/file?${new URLSearchParams({ path: rel, expected_digest: await remoteDigest(rel) })}`)
  deleted++
}
console.log(`${k.store}: ${uploaded} uploaded, ${unchanged} unchanged, ${deleted} deleted (${local.length} files)`)
