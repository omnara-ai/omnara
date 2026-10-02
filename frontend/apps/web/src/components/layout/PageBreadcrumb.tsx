import { Link, type LinkProps } from '@tanstack/react-router'
import { Fragment, type ReactNode, use } from 'react'
import { createPortal } from 'react-dom'

import { BreadcrumbSlotContext } from '@/components/layout/breadcrumb-slot-context'
import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb'

const crumbClass = 'inline-flex min-h-10 min-w-0 max-w-56 items-center gap-1.5 md:min-h-0'

export interface Crumb {
  id: string
  label: string
  to?: LinkProps['to']
  params?: LinkProps['params']
  icon?: ReactNode
}

/** The page title of every screen: a breadcrumb trail ending at the current page. */
export function PageBreadcrumb({ items }: { items: Crumb[] }) {
  const slot = use(BreadcrumbSlotContext)
  const trail = (
    <Breadcrumb className="min-w-0">
      <BreadcrumbList className="flex-nowrap gap-2.5 overflow-hidden whitespace-nowrap sm:gap-3">
        {items.map((item, index) => {
          const isLast = index === items.length - 1
          const content = (
            <>
              {item.icon}
              <span className="truncate">{item.label}</span>
            </>
          )
          return (
            <Fragment key={item.id}>
              {index > 0 && (
                <BreadcrumbSeparator className="text-muted-foreground/40 hidden sm:block">
                  /
                </BreadcrumbSeparator>
              )}
              <BreadcrumbItem className={isLast ? 'min-w-0' : 'hidden min-w-0 sm:inline-flex'}>
                {isLast ? (
                  <BreadcrumbLink asChild className="text-foreground hover:text-foreground">
                    <Link to="." aria-current="page" className={crumbClass}>
                      {content}
                    </Link>
                  </BreadcrumbLink>
                ) : item.to ? (
                  <BreadcrumbLink asChild>
                    {/* Exact, so an ancestor crumb never also claims aria-current="page". */}
                    <Link
                      to={item.to}
                      params={item.params}
                      activeOptions={{ exact: true }}
                      className={crumbClass}
                    >
                      {content}
                    </Link>
                  </BreadcrumbLink>
                ) : (
                  <span className={crumbClass}>{content}</span>
                )}
              </BreadcrumbItem>
            </Fragment>
          )
        })}
      </BreadcrumbList>
    </Breadcrumb>
  )
  return slot ? createPortal(trail, slot) : trail
}
