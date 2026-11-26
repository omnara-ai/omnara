export interface PageRouter {
  getMatchedRoutes: (pathname: string) => readonly [unknown, unknown, unknown]
  history: { push: (href: string) => void }
  state: { isLoading: boolean }
}

/** The path of a link that stays inside the dashboard, or undefined for anything else. */
export function dashboardPath(href: string | undefined): string | undefined {
  if (href == null || !href.startsWith('/') || href.startsWith('//')) return undefined
  return href.split(/[?#]/, 1)[0]
}

/** Whether the link names a real dashboard page rather than, say, an API or file path. */
export function isDashboardPage(router: PageRouter | undefined, href: string | undefined): boolean {
  const path = dashboardPath(href)
  return router != null && path != null && router.getMatchedRoutes(path)[2] != null
}
