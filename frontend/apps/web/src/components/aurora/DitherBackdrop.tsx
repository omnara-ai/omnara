import { useEffect, useRef } from 'react'

import { type Blob, blobCentres, CELL, drawAurora, HERO_BLOBS } from '@/components/aurora/aurora'
import { publishAurora, resetAurora } from '@/components/aurora/auroraStore'

const FPS = 24

const readInk = () => {
  const cs = getComputedStyle(document.documentElement)
  const [r = 95, g = 138, b = 220] = cs
    .getPropertyValue('--aurora-ink')
    .trim()
    .split(/\s+/)
    .map(Number)
  const a = Number(cs.getPropertyValue('--aurora-alpha')) || 44
  return (a << 24) | (b << 16) | (g << 8) | r // little-endian RGBA
}

interface DitherBackdropProps {
  className: string
  blobs?: readonly Blob[]
  /** Fraction of the height below which the glow fades to nothing. */
  fadeFrom?: number
  /** Aurora channel this backdrop publishes on (see auroraStore.ts). */
  source?: string
}

export const DitherBackdrop = ({
  className,
  blobs = HERO_BLOBS,
  fadeFrom = 0.66,
  source = 'hero',
}: DitherBackdropProps) => {
  const canvasRef = useRef<HTMLCanvasElement>(null)

  useEffect(() => {
    const canvas = canvasRef.current
    if (!canvas) return

    const ctx = canvas.getContext('2d')
    if (!ctx) return

    const motion = window.matchMedia('(prefers-reduced-motion: reduce)')
    let img: ImageData | null = null
    let width = 0
    let height = 0
    let ink = 0
    let inkDirty = true
    let dirty = true
    let inView = false
    let disposed = false
    let raf: number | null = null
    let last = 0

    const canRender = () => !disposed && inView && !document.hidden && width > 0 && height > 0

    const publishPositions = (now: number) => {
      const rect = canvas.getBoundingClientRect()
      const t = motion.matches ? 0 : now / 1000
      publishAurora(
        blobCentres(blobs, width, height, t).map((c) => ({
          x: rect.left + c.x * CELL,
          y: rect.top + c.y * CELL,
        })),
        source,
        !motion.matches,
      )
      return t
    }

    const render = (now: number) => {
      if (inkDirty) {
        ink = readInk()
        inkDirty = false
      }
      const t = publishPositions(now)
      if (img?.width !== width || img.height !== height) {
        canvas.width = width
        canvas.height = height
        img = ctx.createImageData(width, height)
      }
      drawAurora(img, t, ink, blobs, fadeFrom)
      ctx.putImageData(img, 0, 0)
    }

    const schedule = () => {
      if (canRender() && (dirty || !motion.matches)) {
        raf ??= requestAnimationFrame(frame)
      } else if (raf !== null) {
        cancelAnimationFrame(raf)
        raf = null
      }
    }

    const frame = (now: number) => {
      raf = null
      if (!canRender()) return
      if (dirty || now - last >= 1000 / FPS) {
        render(now)
        dirty = false
        last = now
      }
      schedule()
    }

    const resize = new ResizeObserver(([entry]) => {
      if (disposed || !entry) return
      width = Math.ceil(entry.contentRect.width / CELL)
      height = Math.ceil(entry.contentRect.height / CELL)
      if (width === 0 || height === 0) resetAurora(source)
      else if (!inView) publishPositions(performance.now())
      dirty = true
      schedule()
    })
    const intersection = new IntersectionObserver(([entry]) => {
      inView = entry?.isIntersecting ?? false
      dirty = true
      schedule()
    })
    const onVisibility = () => {
      dirty = true
      schedule()
    }
    const onMotion = () => {
      dirty = true
      schedule()
    }
    // The app switches themes with the `dark` class on <html>, not the media query.
    const scheme = new MutationObserver(() => {
      inkDirty = true
      dirty = true
      schedule()
    })

    resize.observe(canvas)
    intersection.observe(canvas)
    document.addEventListener('visibilitychange', onVisibility)
    motion.addEventListener('change', onMotion)
    scheme.observe(document.documentElement, { attributeFilter: ['class'] })

    return () => {
      if (disposed) return
      disposed = true
      if (raf !== null) cancelAnimationFrame(raf)
      resize.disconnect()
      intersection.disconnect()
      document.removeEventListener('visibilitychange', onVisibility)
      motion.removeEventListener('change', onMotion)
      scheme.disconnect()
      resetAurora(source)
    }
  }, [blobs, fadeFrom, source])

  return (
    <div aria-hidden="true" className={`pointer-events-none overflow-hidden ${className}`}>
      <canvas
        ref={canvasRef}
        className="absolute inset-0 h-full w-full [image-rendering:pixelated]"
      />
    </div>
  )
}
