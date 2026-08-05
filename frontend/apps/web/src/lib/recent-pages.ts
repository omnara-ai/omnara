import { readLocal, writeLocal } from '@/lib/storage'

const MAX_STORED_PAGES = 20

export interface RecentPage {
  path: string
  visitedAt: string
}

function storageKey(orgId: string, userId: string) {
  return `omnara-recent-pages:${userId}:${orgId}`
}

export function recordRecentPage(orgId: string, userId: string, path: string) {
  if (path === '/') return
  const pages = readRecentPages(orgId, userId).filter((page) => page.path !== path)
  pages.unshift({ path, visitedAt: new Date().toISOString() })
  writeLocal(storageKey(orgId, userId), JSON.stringify(pages.slice(0, MAX_STORED_PAGES)))
}

export function readRecentPages(orgId: string, userId: string): RecentPage[] {
  const stored = readLocal(storageKey(orgId, userId))
  if (!stored) return []
  try {
    const parsed: unknown = JSON.parse(stored)
    if (!Array.isArray(parsed)) return []
    return parsed.filter(
      (page): page is RecentPage =>
        typeof page === 'object' &&
        page !== null &&
        typeof (page as RecentPage).path === 'string' &&
        typeof (page as RecentPage).visitedAt === 'string',
    )
  } catch {
    return []
  }
}
