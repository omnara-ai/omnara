/* Size of one dither cell in CSS px. */
export const CELL = 2

/* 8x8 Bayer matrix, flattened row-major and normalised to (0,1). */
const BAYER_8 = Float32Array.from(
  [
    [0, 32, 8, 40, 2, 34, 10, 42],
    [48, 16, 56, 24, 50, 18, 58, 26],
    [12, 44, 4, 36, 14, 46, 6, 38],
    [60, 28, 52, 20, 62, 30, 54, 22],
    [3, 35, 11, 43, 1, 33, 9, 41],
    [51, 19, 59, 27, 49, 17, 57, 25],
    [15, 47, 7, 39, 13, 45, 5, 37],
    [63, 31, 55, 23, 61, 29, 53, 21],
  ].flat(),
  (v) => (v + 0.5) / 64,
)

export interface Blob {
  /* Rest position as a fraction of the canvas size. */
  cx: number
  cy: number
  /* Drift amplitude (fraction of canvas size) and angular speeds (rad/s). */
  ax: number
  ay: number
  wx: number
  wy: number
  /* Radius as a fraction of the canvas width; peak intensity 0..1. */
  r: number
  peak: number
}

export const HERO_BLOBS: Blob[] = [
  {
    cx: 0.2,
    cy: 0.25,
    ax: 0.14,
    ay: 0.16,
    wx: 0.31,
    wy: 0.23,
    r: 0.32,
    peak: 0.6,
  },
  {
    cx: 0.84,
    cy: 0.3,
    ax: 0.12,
    ay: 0.14,
    wx: 0.19,
    wy: 0.29,
    r: 0.28,
    peak: 0.4,
  },
]

export const blobCentres = (blobs: readonly Blob[], width: number, height: number, t: number) =>
  blobs.map((b) => ({
    x: (b.cx + b.ax * Math.sin(t * b.wx)) * width,
    y: (b.cy + b.ay * Math.cos(t * b.wy)) * height,
  }))

export const drawAurora = (
  img: { width: number; height: number; data: Uint8ClampedArray },
  t: number,
  ink: number,
  blobDefs: readonly Blob[],
  fadeFrom: number,
) => {
  const { width, height, data } = img
  const px = new Uint32Array(data.buffer)
  const centres = blobCentres(blobDefs, width, height, t)
  const blobs = blobDefs.map((b, i) => {
    const r = b.r * width
    return {
      x: centres[i]?.x ?? 0,
      y: centres[i]?.y ?? 0,
      r2: r * r,
      invR2: 1 / (r * r),
      peak: b.peak,
      dy2: 0,
    }
  })

  /* `fadeFrom` is a height fraction; 1 disables fading. */
  const fadeStart = height * fadeFrom
  const invFade = fadeFrom < 1 ? 1 / (height - fadeStart) : 0

  px.fill(0)
  const active: (typeof blobs)[number][] = []
  for (let y = 0; y < height; y++) {
    let left = width
    let right = 0
    active.length = 0
    for (const b of blobs) {
      const dy = y - b.y
      b.dy2 = dy * dy
      if (b.dy2 >= b.r2) continue
      const extent = Math.sqrt(b.r2 - b.dy2)
      left = Math.min(left, Math.max(0, Math.floor(b.x - extent)))
      right = Math.max(right, Math.min(width, Math.ceil(b.x + extent) + 1))
      active.push(b)
    }
    const rowOffset = (y & 7) << 3
    const fade = y > fadeStart ? 1 - (y - fadeStart) * invFade : 1
    for (let x = left; x < right; x++) {
      let v = 0
      for (const b of active) {
        const dx = x - b.x
        const d2 = (dx * dx + b.dy2) * b.invR2
        if (d2 < 1) {
          const f = 1 - d2
          v += b.peak * f * f
        }
      }
      if (v * fade > (BAYER_8[rowOffset | (x & 7)] ?? 1)) px[y * width + x] = ink
    }
  }
}
