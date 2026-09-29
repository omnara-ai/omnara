import { Link, type LinkProps } from '@tanstack/react-router'
import { Fragment, type ReactNode } from 'react'

import {
  Breadcrumb,
  BreadcrumbItem,
  BreadcrumbLink,
  BreadcrumbList,
  BreadcrumbPage,
  BreadcrumbSeparator,
} from '@/components/ui/breadcrumb'
import { cn } from '@/lib/utils'

const iconCrumbClass = 'inline-flex items-center gap-1.5'

export interface Crumb {
  id: string
  label: string
  to?: LinkProps['to']
  params?: LinkProps['params']
  icon?: ReactNode
}

/** The page title of every screen: a breadcrumb trail ending at the current page. */
export function PageBreadcrumb({ items }: { items: Crumb[] }) {
  return (
    <Breadcrumb>
      <BreadcrumbList>
        {items.map((item, index) => {
          const isLast = index === items.length - 1
          return (
            <Fragment key={item.id}>
              {index > 0 && <BreadcrumbSeparator />}
              <BreadcrumbItem>
                {isLast ? (
                  <BreadcrumbPage className={cn(item.icon && iconCrumbClass)}>
                    {item.icon}
                    {item.label}
                  </BreadcrumbPage>
                ) : item.to ? (
                  <BreadcrumbLink asChild>
                    <Link
                      to={item.to}
                      params={item.params}
                      className={cn(item.icon && iconCrumbClass)}
                    >
                      {item.icon}
                      {item.label}
                    </Link>
                  </BreadcrumbLink>
                ) : (
                  item.label
                )}
              </BreadcrumbItem>
            </Fragment>
          )
        })}
      </BreadcrumbList>
    </Breadcrumb>
  )
}
