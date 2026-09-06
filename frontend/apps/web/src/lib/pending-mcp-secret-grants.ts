import { z } from 'zod'

const KEY_PREFIX = 'omnara:pending-mcp-secret-grants:'
const storedProjectIds = z.array(z.string())

export function savePendingMcpSecretGrants(orgId: string, projectIds: string[]) {
  if (projectIds.length === 0) {
    clearPendingMcpSecretGrants(orgId)
    return
  }
  window.sessionStorage.setItem(`${KEY_PREFIX}${orgId}`, JSON.stringify(projectIds))
}

export function takePendingMcpSecretGrants(orgId: string): string[] {
  const key = `${KEY_PREFIX}${orgId}`
  const stored = window.sessionStorage.getItem(key)
  window.sessionStorage.removeItem(key)
  if (!stored) return []
  try {
    const parsed = storedProjectIds.safeParse(JSON.parse(stored))
    return parsed.success ? parsed.data : []
  } catch {
    return []
  }
}

export function clearPendingMcpSecretGrants(orgId: string) {
  window.sessionStorage.removeItem(`${KEY_PREFIX}${orgId}`)
}
