import type { ReactNode } from 'react'

import type { Blob } from '@/components/aurora/aurora'
import { DitherBackdrop } from '@/components/aurora/DitherBackdrop'

/** The landing hero's blobs, larger and lower so they sit behind the page's top content. */
const backdropBlobs: Blob[] = [
  { cx: 0.2, cy: 0.4, ax: 0.14, ay: 0.14, wx: 0.31, wy: 0.23, r: 0.4, peak: 0.6 },
  { cx: 0.84, cy: 0.44, ax: 0.12, ay: 0.12, wx: 0.19, wy: 0.29, r: 0.35, peak: 0.4 },
]

/**
 * An overview page's glow, drawn under the floating header, with the page's content over it.
 * The panels on the page light their borders from it.
 */
export function OverviewBackdrop({ children }: { children: ReactNode }) {
  return (
    <>
      <DitherBackdrop
        className="absolute inset-x-0 top-0 h-[760px] sm:h-[900px]"
        blobs={backdropBlobs}
      />
      <div
        data-transparent-header
        className="relative flex min-h-0 flex-1 justify-center pt-20 pb-8 sm:pt-24 sm:pb-12"
      >
        {children}
      </div>
    </>
  )
}
