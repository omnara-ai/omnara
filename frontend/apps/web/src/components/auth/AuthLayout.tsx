import type { ReactNode } from 'react'

import type { Blob } from '@/components/aurora/aurora'
import { AuroraPanel } from '@/components/aurora/AuroraPanel'
import { DitherBackdrop } from '@/components/aurora/DitherBackdrop'
import { BrandMark } from '@/components/brand/OmnaraMark'

function Wordmark() {
  return (
    <div className="type-card-title flex items-center gap-2 text-base">
      <BrandMark className="size-5" />
      Omnara
    </div>
  )
}

/** A glow either side of the centred panel, drifting like the overview's backdrop. */
const backdropBlobs: Blob[] = [
  { cx: 0.3, cy: 0.42, ax: 0.12, ay: 0.12, wx: 0.31, wy: 0.23, r: 0.34, peak: 0.55 },
  { cx: 0.72, cy: 0.56, ax: 0.1, ay: 0.12, wx: 0.19, wy: 0.29, r: 0.3, peak: 0.4 },
]

export function AuthLayout({ children }: { children: ReactNode }) {
  return (
    <div className="bg-background relative flex min-h-svh flex-col overflow-hidden p-6 sm:p-10">
      <DitherBackdrop
        className="absolute inset-0"
        blobs={backdropBlobs}
        fadeFrom={0.85}
        source="auth"
      />
      <header className="relative">
        <Wordmark />
      </header>
      <main className="relative flex flex-1 items-center justify-center py-10">
        <AuroraPanel source="auth" className="w-full max-w-md p-6 sm:p-8">
          {children}
        </AuroraPanel>
      </main>
      <footer className="relative hidden space-y-3 lg:block">
        <p className="text-foreground type-title">The API for production-grade agents.</p>
        <p className="text-muted-foreground text-base">Omnara console</p>
      </footer>
    </div>
  )
}

export function AuthHeading({ title, subtitle }: { title: string; subtitle: string }) {
  return (
    <div className="flex flex-col gap-2">
      <h1 className="type-title">{title}</h1>
      <p className="text-muted-foreground text-sm text-pretty">{subtitle}</p>
    </div>
  )
}
