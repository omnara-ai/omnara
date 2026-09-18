import { type ReactNode, useLayoutEffect, useRef, useState } from 'react'

import { cn } from '@/lib/utils'

export function CollapseBody({
  open,
  className,
  children,
}: {
  open: boolean
  className?: string
  children: ReactNode
}) {
  const contentRef = useRef<HTMLDivElement>(null)
  const [height, setHeight] = useState<number>()
  useLayoutEffect(() => {
    const element = contentRef.current
    if (!element) return
    const update = () => {
      setHeight(element.offsetHeight)
    }
    update()
    const observer = new ResizeObserver(update)
    observer.observe(element)
    return () => {
      observer.disconnect()
    }
  }, [])
  return (
    <div
      data-state={open ? 'open' : 'closed'}
      aria-hidden={!open}
      className={cn('collapse-body overflow-hidden', className)}
      style={{ height: open ? (height ?? 'auto') : 0, visibility: open ? 'visible' : 'hidden' }}
    >
      <div ref={contentRef}>{children}</div>
    </div>
  )
}
