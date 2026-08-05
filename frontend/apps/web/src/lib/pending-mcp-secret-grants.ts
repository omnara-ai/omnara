const KEY_PREFIX = 'omnara:pending-mcp-secret-grants:'

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
    const parsed: unknown = JSON.parse(stored)
    return Array.isArray(parsed)
      ? parsed.filter((value): value is string => typeof value === 'string')
      : []
  } catch {
    return []
  }
}

export function clearPendingMcpSecretGrants(orgId: string) {
  window.sessionStorage.removeItem(`${KEY_PREFIX}${orgId}`)
}
