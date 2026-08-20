import { useState } from 'react'

import { CircleCheck, TriangleAlert } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'

interface OAuthOutcomeText {
  title: string
  description: string
}

type OAuthOutcome = ({ kind: 'success' } | { kind: 'error' }) & OAuthOutcomeText

export interface OAuthOutcomeDialogProps {
  successParam: string
  errorParam: string
  extraParams?: string[]
  successOutcome: (params: URLSearchParams) => OAuthOutcomeText
  errorOutcome: (code: string) => OAuthOutcomeText
}

function initialOutcome(props: OAuthOutcomeDialogProps): OAuthOutcome | null {
  if (typeof window === 'undefined') return null

  const search = new URLSearchParams(window.location.search)
  const success = search.get(props.successParam) === 'success'
  const errorCode = search.get(props.errorParam)
  if (!success && !errorCode) return null

  if (errorCode) {
    return { kind: 'error', ...props.errorOutcome(errorCode) }
  }
  return { kind: 'success', ...props.successOutcome(search) }
}

function clearParams(props: OAuthOutcomeDialogProps) {
  if (typeof window === 'undefined') return

  const url = new URL(window.location.href)
  url.searchParams.delete(props.successParam)
  url.searchParams.delete(props.errorParam)
  for (const param of props.extraParams ?? []) {
    url.searchParams.delete(param)
  }
  const nextUrl = `${url.pathname}${url.search}${url.hash}`
  window.history.replaceState(window.history.state, '', nextUrl)
}

export function OAuthOutcomeDialog(props: OAuthOutcomeDialogProps) {
  const [outcome, setOutcome] = useState<OAuthOutcome | null>(() => initialOutcome(props))
  const isError = outcome?.kind === 'error'

  function close() {
    setOutcome(null)
    clearParams(props)
  }

  return (
    <Dialog
      open={outcome !== null}
      onOpenChange={(open) => {
        if (!open) close()
      }}
    >
      <DialogContent>
        <div className="flex items-start gap-3">
          <div
            className={
              isError
                ? 'bg-destructive/10 text-destructive flex size-10 shrink-0 items-center justify-center rounded-full'
                : 'bg-primary/10 text-primary flex size-10 shrink-0 items-center justify-center rounded-full'
            }
          >
            {isError ? <TriangleAlert className="size-5" /> : <CircleCheck className="size-5" />}
          </div>
          <DialogHeader>
            <DialogTitle>{outcome?.title}</DialogTitle>
            <DialogDescription>{outcome?.description}</DialogDescription>
          </DialogHeader>
        </div>
        <DialogFooter>
          <Button onClick={close}>Got it</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
