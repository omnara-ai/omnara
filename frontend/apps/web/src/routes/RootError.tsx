import { ApiError } from '@omnara/sdk'
import type { ErrorComponentProps } from '@tanstack/react-router'

import { Button } from '@/components/ui/button'

export function RootError({ error }: ErrorComponentProps) {
  const message =
    error instanceof ApiError
      ? `${error.status}: ${error.message}`
      : error instanceof Error
        ? error.message
        : 'Something went wrong.'

  return (
    <div className="flex h-full flex-col items-center justify-center gap-4 p-6 text-center">
      <div>
        <h1 className="text-lg font-semibold">Something went wrong</h1>
        <p className="text-muted-foreground mt-1 max-w-md text-sm">{message}</p>
      </div>
      <Button
        variant="outline"
        onClick={() => {
          window.location.reload()
        }}
      >
        Reload
      </Button>
    </div>
  )
}
