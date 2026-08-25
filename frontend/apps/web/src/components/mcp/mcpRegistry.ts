import type { McpRegistryRemote, McpRegistryServer } from '@omnara/sdk'

import { mcpServerNameMaxLength } from '@/components/agents/useAgentBuilderForm'

export function registryServerSearchFilters(name: string): { q?: string } {
  const q = name.trim()
  return q === '' ? {} : { q }
}

export function registryServerDomain(server: Pick<McpRegistryServer, 'name'>) {
  const namespace = server.name.split('/')[0] ?? ''
  const labels = namespace.split('.').filter((label) => label !== '')
  if (labels.length < 2 || !labels.every((label) => /^[a-z0-9-]+$/i.test(label))) return null
  return labels.reverse().join('.').toLowerCase()
}

export function githubNamespaceUser(server: Pick<McpRegistryServer, 'name'>) {
  const match = /^io\.github\.([a-z0-9-]+)\//i.exec(server.name)
  return match?.[1] ?? null
}

export function remoteOriginFaviconSrc(url: string) {
  try {
    const parsed = new URL(url.trim())
    if (parsed.protocol !== 'https:' && parsed.protocol !== 'http:') return null
    return `${parsed.origin}/favicon.ico`
  } catch {
    return null
  }
}

export function registryServerIconCandidates(
  server: Pick<McpRegistryServer, 'name' | 'icons'> | null | undefined,
  url = '',
) {
  const candidates: (string | null)[] = [registryServerIconSrc(server)]
  if (server) {
    const user = githubNamespaceUser(server)
    if (user) {
      candidates.push(`https://github.com/${user}.png?size=64`)
    } else {
      const domain = registryServerDomain(server)
      candidates.push(domain ? `https://${domain}/favicon.ico` : null)
    }
  }
  candidates.push(remoteOriginFaviconSrc(url))
  return candidates.filter((candidate): candidate is string => candidate !== null)
}

export function registryServerShortName(server: Pick<McpRegistryServer, 'name'>) {
  const segment = server.name.split('/').at(-1) ?? server.name
  const slug = segment.replace(/[^A-Za-z0-9_.-]+/g, '-').replace(/^-+|-+$/g, '')
  return slug === '' ? server.name : slug
}

export function registryServerSuggestedName(server: Pick<McpRegistryServer, 'name'>) {
  const segment = server.name.split('/').at(-1) ?? server.name
  return segment
    .replace(/[^A-Za-z0-9]+/g, '-')
    .replace(/^[^A-Za-z]+/, '')
    .slice(0, mcpServerNameMaxLength)
    .replace(/-+$/, '')
}

export function registryServerLabel(server: Pick<McpRegistryServer, 'name' | 'title'>) {
  const title = server.title?.trim() ?? ''
  return title === '' ? registryServerShortName(server) : title
}

export interface RegistryServerEntry {
  key: string
  server: McpRegistryServer
  remote: McpRegistryRemote
}

export function streamableRemotes(server: Pick<McpRegistryServer, 'remotes'>) {
  return server.remotes.filter((remote) => remote.type === 'streamable-http')
}

export function registryServerRemoteUrl(server: Pick<McpRegistryServer, 'remotes'>) {
  return streamableRemotes(server)[0]?.url ?? ''
}

export function registryServerEntries(servers: McpRegistryServer[]): RegistryServerEntry[] {
  return servers.flatMap((server) =>
    streamableRemotes(server).map((remote) => ({
      key: `${server.name}\n${remote.url}`,
      server,
      remote,
    })),
  )
}

export function registryServerIconSrc(
  server: Pick<McpRegistryServer, 'icons'> | null | undefined,
  theme: 'light' | 'dark' = 'light',
) {
  if (!server) return null
  const icons = server.icons
  const themed = icons.find((icon) => icon.theme === theme)
  const untitled = icons.find((icon) => !icon.theme)
  return (themed ?? untitled ?? icons[0])?.src ?? null
}
