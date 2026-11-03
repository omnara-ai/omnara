import { useCallback } from 'react'

interface AuroraPoint {
  x: number
  y: number
}

/* One channel per backdrop so a card lights up from the glow nearest it. */
const staticFrames = new Map<string, readonly AuroraPoint[]>()
const rings = new Map<string, Set<HTMLElement>>()

const updateRings = (
  elements: Iterable<HTMLElement>,
  points: readonly AuroraPoint[],
  animated: boolean,
) => {
  // Read every rectangle before changing styles to avoid layout thrashing.
  // https://developer.chrome.com/docs/performance/insights/forced-reflow
  const viewportWidth = window.innerWidth
  const viewportHeight = window.innerHeight
  const measurements = [...elements].map((el) => ({
    el,
    rect: el.getBoundingClientRect(),
  }))
  for (const { el, rect } of measurements) {
    if (rect.width === 0 || rect.height === 0) continue
    // A static frame must also initialize borders that will be scrolled into view.
    if (
      animated &&
      (rect.bottom <= 0 ||
        rect.top >= viewportHeight ||
        rect.right <= 0 ||
        rect.left >= viewportWidth)
    )
      continue
    const [a, b] = points
    if (a) {
      el.style.setProperty('--spec-ax', `${String(a.x - rect.left)}px`)
      el.style.setProperty('--spec-ay', `${String(a.y - rect.top)}px`)
    }
    if (b) {
      el.style.setProperty('--spec-bx', `${String(b.x - rect.left)}px`)
      el.style.setProperty('--spec-by', `${String(b.y - rect.top)}px`)
    }
  }
}

export const publishAurora = (points: readonly AuroraPoint[], source = 'hero', animated = true) => {
  if (animated) staticFrames.delete(source)
  else staticFrames.set(source, points)
  updateRings(rings.get(source) ?? [], points, animated)
}

export const resetAurora = (source = 'hero') => {
  staticFrames.delete(source)
  for (const el of rings.get(source) ?? []) {
    for (const property of ['--spec-ax', '--spec-ay', '--spec-bx', '--spec-by']) {
      el.style.removeProperty(property)
    }
  }
}

const registerAuroraRing = (el: HTMLElement, source: string) => {
  let set = rings.get(source)
  if (!set) {
    set = new Set()
    rings.set(source, set)
  }
  set.add(el)
  // Animated sources initialize new rings on their next frame; static sources
  // need a one-time update because another frame may never arrive.
  const points = staticFrames.get(source)
  if (points) updateRings([el], points, false)
  return () => {
    set.delete(el)
    if (set.size === 0) rings.delete(source)
  }
}

// Attach this ref to the empty border element so animated variables do not
// invalidate the styles of the card's contents through CSS inheritance.
export const useAuroraSpecular = (source = 'hero') =>
  useCallback(
    (el: HTMLElement | null) => {
      if (el) return registerAuroraRing(el, source)
      return undefined
    },
    [source],
  )
