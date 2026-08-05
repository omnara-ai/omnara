export function authTokenFromURL(): string {
  return new URLSearchParams(window.location.search).get('token') ?? ''
}

export function clearAuthTokenFromURL(): void {
  const url = new URL(window.location.href)
  if (!url.searchParams.has('token')) return
  url.searchParams.delete('token')
  const search = url.searchParams.toString()
  window.history.replaceState(
    window.history.state,
    '',
    `${url.pathname}${search ? `?${search}` : ''}${url.hash}`,
  )
}
