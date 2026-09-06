import type { ReactNode } from 'react'

import { Input } from '@/components/ui/input'

/**
 * Header for a table or list: title and count on top, then a toolbar row with
 * the search box on the left and an actions slot on the right. The caller
 * owns the query and filters its own data; omit the search props for lists
 * that don't search. Rich API lists pass a ResourceListToolbar instead.
 */
export function SearchHeader({
  title,
  toolbar,
  value,
  onChange,
  placeholder,
  children,
}: {
  title: string
  toolbar?: ReactNode
  value?: string
  onChange?: (value: string) => void
  placeholder?: string
  children?: ReactNode
}) {
  const showToolbar = toolbar !== undefined || onChange !== undefined || children !== undefined
  return (
    <div className="flex flex-col gap-3">
      <h2 className="text-2xl font-bold tracking-tight">{title}</h2>
      {showToolbar && (
        <div className="flex flex-col items-stretch gap-2 sm:flex-row sm:flex-wrap sm:items-center">
          {toolbar}
          {onChange && (
            <Input
              value={value}
              placeholder={placeholder}
              className="h-10 w-full max-w-sm sm:h-8"
              onChange={(event) => {
                onChange(event.target.value)
              }}
            />
          )}
          {children && (
            <div className="flex flex-wrap items-center gap-2 sm:ml-auto">{children}</div>
          )}
        </div>
      )}
    </div>
  )
}
