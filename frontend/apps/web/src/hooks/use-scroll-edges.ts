import { useCallback, useEffect, useRef } from 'react'

const edgeThreshold = 8

function scrollableEdges(element: HTMLElement): string | null {
  const start = element.scrollTop > edgeThreshold
  const end = element.scrollHeight - element.scrollTop - element.clientHeight > edgeThreshold
  const edges = [start && 'start', end && 'end'].filter(Boolean).join(' ')
  return edges === '' ? null : edges
}

export function useScrollEdges() {
  const elementRef = useRef<HTMLElement | null>(null)

  const sync = useCallback(() => {
    const element = elementRef.current
    if (!element) return
    const edges = scrollableEdges(element)
    if (edges === null) element.removeAttribute('data-scrollable')
    else element.setAttribute('data-scrollable', edges)
  }, [])

  const ref = useCallback(
    (element: HTMLElement | null) => {
      elementRef.current = element
      sync()
    },
    [sync],
  )

  useEffect(() => {
    const element = elementRef.current
    if (!element) return
    const observer = new ResizeObserver(sync)
    observer.observe(element)
    for (const child of element.children) observer.observe(child)
    const contentObserver = new MutationObserver(() => {
      sync()
      for (const child of element.children) observer.observe(child)
    })
    contentObserver.observe(element, { childList: true, subtree: true })
    element.addEventListener('scroll', sync, { passive: true })
    return () => {
      observer.disconnect()
      contentObserver.disconnect()
      element.removeEventListener('scroll', sync)
    }
  }, [sync])

  return ref
}
