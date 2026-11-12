// Where the guide's product knowledge comes from. guide.config.json names one source:
//
//   "knowledge": { "source": "mcp",   "mcp_url": "https://docs.acme.com/mcp", "index_url": "https://docs.acme.com/llms.txt", "base_url": "https://docs.acme.com" }
//   "knowledge": { "source": "web",   "index_url": "https://docs.acme.com/llms.txt", "base_url": "https://docs.acme.com" }
//   "knowledge": { "source": "files", "dir": "../acme/docs", "store": "acme-guide-docs", "base_url": "https://docs.acme.com" }
//   "knowledge": { "source": "none" }
//
// mcp    a docs site with its own MCP server (Mintlify and similar).
// web    a public docs site: the agent fetches pages with Omnara's web_fetch tool.
// files  markdown in a folder (a repo's docs/, an exported wiki, or notes written for the
//        guide). scripts/sync-knowledge.mjs uploads them to an Omnara memory store that
//        the agent searches and reads. base_url is optional: set it only when the files
//        are also published, at base_url + the file's path without its extension.
// none   nothing written down: the agent works from the page alone.

import { readFile, readdir } from 'node:fs/promises'
import path from 'node:path'

const TEXT_FILE = /\.(md|mdx|markdown|txt)$/i

/** Every markdown file under dir, as paths relative to it, skipping exclude prefixes. */
export async function listKnowledgeFiles(dir, exclude = []) {
  const out = []
  async function walk(rel) {
    for (const entry of await readdir(path.join(dir, rel), { withFileTypes: true })) {
      const child = rel ? `${rel}/${entry.name}` : entry.name
      if (entry.name.startsWith('.') || entry.name === 'node_modules') continue
      if (exclude.some((x) => child === x || child.startsWith(`${x}/`))) continue
      if (entry.isDirectory()) await walk(child)
      else if (TEXT_FILE.test(entry.name)) out.push(child)
    }
  }
  await walk('')
  return out.sort()
}

/** Title and one-line summary from front matter, or from the first heading. */
function describeFile(text, rel) {
  const front = text.startsWith('---') ? text.slice(3, text.indexOf('\n---', 3)) : ''
  const field = (name) => front.match(new RegExp(`^${name}:\\s*["']?(.+?)["']?\\s*$`, 'm'))?.[1]
  const title = field('title') ?? text.match(/^#\s+(.+)$/m)?.[1] ?? rel
  return { title, description: field('description') ?? '' }
}

async function fetchIndex(url) {
  const res = await fetch(url)
  if (!res.ok) throw new Error(`fetching ${url}: HTTP ${res.status}`)
  // Keep the page list only; drop API reference endpoints and footer noise.
  return (await res.text())
    .split('\n')
    .filter((line) => line.startsWith('- [') && !line.includes('/api-reference/'))
    .join('\n')
}

/** The pieces of the agent config that depend on the knowledge source. */
export async function renderKnowledge(config, configDir) {
  const k = config.knowledge ?? { source: 'none' }
  const product = config.product
  const linkRule = k.base_url
    ? `When a doc page backs up what you say, link it as a markdown link with the full URL (it must start with ${k.base_url}, without a ".md" or ".mdx" suffix); never link to a path inside the app.`
    : `There is no public documentation to link to, so don't add links; name the place in the app instead.`

  if (k.source === 'mcp') {
    const disabled = (k.disable_tools ?? []).map((t) => `      ${t}:\n        enabled: false`).join('\n')
    return {
      knowledge_how: `Search the ${product} docs with the docs tools unless the page itself makes the path obvious.${k.index_url ? ' The docs index below tells you which page to read.' : ''}`,
      knowledge_links: linkRule,
      knowledge_index: k.index_url ? `Docs index:\n${await fetchIndex(k.index_url)}` : '',
      knowledge_tools: '# Product knowledge comes from the docs MCP server below.',
      knowledge_config: `mcp:\n  docs:\n    url: ${k.mcp_url}\n    permission:\n      mode: always_allow${disabled ? `\n    tools:\n${disabled}` : ''}`,
    }
  }

  if (k.source === 'web') {
    return {
      knowledge_how: `Read the ${product} docs with web_fetch unless the page itself makes the path obvious. The docs index below lists every page: pick the one that fits and fetch its URL. Only fetch URLs from that index.`,
      knowledge_links: linkRule,
      knowledge_index: `Docs index:\n${await fetchIndex(k.index_url)}`,
      knowledge_tools: 'web_fetch:\n  permission:\n    mode: always_allow',
      knowledge_config: '# Product knowledge comes from the public docs site, read with web_fetch.',
    }
  }

  if (k.source === 'files') {
    const dir = path.resolve(configDir, k.dir)
    const files = await listKnowledgeFiles(dir, k.exclude)
    if (!files.length) throw new Error(`no markdown files under ${dir}`)
    const lines = []
    for (const rel of files) {
      const { title, description } = describeFile(await readFile(path.join(dir, rel), 'utf8'), rel)
      lines.push(`- /memory/${k.store}/${rel}: ${title}${description ? `. ${description}` : ''}`)
    }
    const urlRule = k.base_url ? ` A file's public page is ${k.base_url}/ plus its path after /memory/${k.store}/, without the extension.` : ''
    return {
      knowledge_how: `What is written down about ${product} is in files under /memory/${k.store}/. Unless the page itself makes the path obvious, pick the file from the index below and read it with storage_read, or search all of them with storage_search (path "/memory/${k.store}/**", args like ["-i", "-e", "invite"]). Read only the files you need.`,
      knowledge_links: linkRule + urlRule,
      knowledge_index: `Files:\n${lines.join('\n')}`,
      knowledge_tools: '# Product knowledge comes from the memory store below (storage_search, storage_read).',
      knowledge_config: `memory_stores:\n  - name: ${k.store}\n    access: read`,
    }
  }

  if (k.source === 'none') {
    return {
      knowledge_how: `Nothing is written down about ${product} for you to read. Work from what the page shows: its navigation, headings, labels and controls. Explore one step at a time, and when you can't tell how something is done, say so instead of guessing.`,
      knowledge_links: linkRule,
      knowledge_index: '',
      knowledge_tools: '# No knowledge source: the agent works from the page.',
      knowledge_config: '# No knowledge source configured.',
    }
  }

  throw new Error(`unknown knowledge source "${k.source}" (use mcp, web, files or none)`)
}
