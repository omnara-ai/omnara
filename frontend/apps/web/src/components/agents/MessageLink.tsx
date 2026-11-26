import { useRouter } from '@tanstack/react-router'
import type { MouseEvent, ReactNode } from 'react'

import { isDashboardPage, type PageRouter } from '@/lib/dashboard-page'

/**
 * A link in a message. One that names a real dashboard page opens it in place, keeping
 * the conversation; every other link opens in a new tab.
 */
export function MessageLink({
  href,
  title,
  children,
}: {
  href?: string
  title?: string
  children: ReactNode
}) {
  // SAFETY: without a RouterProvider (tests, embeds) the hook returns undefined at runtime,
  // which its declared type omits.
  const router = useRouter({ warn: false }) as PageRouter | undefined
  const page = isDashboardPage(router, href)

  if (!page || href == null) {
    return (
      <a href={href} title={title} target="_blank" rel="noreferrer">
        {children}
      </a>
    )
  }
  function open(event: MouseEvent<HTMLAnchorElement>) {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) {
      return
    }
    event.preventDefault()
    if (href != null) router?.history.push(href)
  }
  return (
    <a href={href} title={title} onClick={open}>
      {children}
    </a>
  )
}
