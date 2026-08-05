import type { CSSProperties, ReactNode } from 'react'

import { BrandMark } from '@/components/brand/OmnaraMark'

function Wordmark() {
  return (
    <div className="flex items-center gap-2 text-base font-semibold tracking-tight">
      <BrandMark className="size-5" />
      Omnara
    </div>
  )
}

const dotGrid: CSSProperties = {
  backgroundImage: 'radial-gradient(circle at 1px 1px, var(--border) 1px, transparent 0)',
  backgroundSize: '22px 22px',
  maskImage: 'radial-gradient(ellipse at top, black, transparent 75%)',
  WebkitMaskImage: 'radial-gradient(ellipse at top, black, transparent 75%)',
}

const glow: CSSProperties = {
  background: 'radial-gradient(circle, var(--primary), transparent 60%)',
}

export function AuthLayout({ children }: { children: ReactNode }) {
  return (
    <div className="grid min-h-svh lg:grid-cols-2">
      <aside className="relative hidden flex-col justify-between overflow-hidden p-10 lg:flex">
        <div className="from-primary/10 via-card to-card absolute inset-0 bg-gradient-to-br" />
        <div aria-hidden className="absolute inset-0 opacity-40" style={dotGrid} />
        <div
          aria-hidden
          className="absolute -top-24 left-1/2 size-[28rem] -translate-x-1/2 rounded-full opacity-[0.12] blur-3xl"
          style={glow}
        />
        <div className="relative z-10">
          <Wordmark />
        </div>
        <blockquote className="relative z-10 space-y-3">
          <p className="text-foreground text-balance text-2xl font-medium leading-snug">
            Mission control for your AI agents — launch them, steer them, and review their work from
            anywhere.
          </p>
          <footer className="text-muted-foreground text-sm">The Omnara console</footer>
        </blockquote>
      </aside>

      <main className="flex flex-col p-6 sm:p-10">
        <div className="lg:hidden">
          <Wordmark />
        </div>
        <div className="flex flex-1 items-center justify-center py-10">
          <div className="w-full max-w-sm">{children}</div>
        </div>
      </main>
    </div>
  )
}

export function AuthHeading({ title, subtitle }: { title: string; subtitle: string }) {
  return (
    <div className="flex flex-col gap-2">
      <h1 className="text-2xl font-semibold tracking-tight">{title}</h1>
      <p className="text-muted-foreground text-pretty text-sm">{subtitle}</p>
    </div>
  )
}
