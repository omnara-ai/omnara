export type IconGlyph = 'square' | 'triangle' | 'quarter'

export type IconTexture = 'dots' | 'lines' | 'columns'

export type IconCorner = 0 | 1 | 2 | 3

export interface IconStyle {
  glyphs: readonly IconGlyph[]
  texture: IconTexture
}

export interface IconCell {
  row: number
  column: number
  glyph: IconGlyph
  corner: IconCorner
  textured: boolean
}

export interface IconPattern {
  style: IconStyle
  columns: number
  rows: number
  cells: IconCell[]
}

export interface AgentIconSpec {
  tint: number
  pattern: IconPattern
}

interface IconSlot {
  row: number
  column: number
}

interface ProfileLayout {
  tint: number
  style: IconStyle
  marks: IconCell[]
}

const iconGlyphSets: readonly (readonly IconGlyph[])[] = [
  ['square', 'triangle'],
  ['triangle'],
  ['quarter'],
  ['square', 'quarter'],
  ['triangle', 'quarter'],
]
const iconTextures: readonly IconTexture[] = ['dots', 'lines', 'columns']
const iconHues = [25, 55, 85, 135, 165, 195, 225, 255, 285, 315, 345] as const
const corners: readonly IconCorner[] = [0, 1, 2, 3]

const profileSize = 2
const agentSize = 4
const centerOffset = (agentSize - profileSize) / 2
const minimumProfileMarks = 2
const minimumRingMarks = 3
const texturedChance = 0.3

const profileSlots = halfSlots(profileSize)
const ringSlots = halfSlots(agentSize).filter(
  (slot) =>
    slot.column < centerOffset || slot.row < centerOffset || slot.row >= centerOffset + profileSize,
)

export function profileIcon(profileId: string): AgentIconSpec {
  const { tint, style, marks } = profileLayout(seededRandom(profileId))
  return iconSpec(tint, style, profileSize, marks)
}

export function agentIcon(profileId: string | undefined, agentId: string): AgentIconSpec {
  const agentRandom = seededRandom(agentId)
  const { tint, style, marks } = profileLayout(
    profileId === undefined ? agentRandom : seededRandom(profileId),
  )
  const center = marks.map((mark) => ({
    ...mark,
    row: mark.row + centerOffset,
    column: mark.column + centerOffset,
  }))
  const ring = filledMarks(agentRandom, style, ringSlots, minimumRingMarks)
  return iconSpec(tint, style, agentSize, [...center, ...ring])
}

function profileLayout(random: () => number): ProfileLayout {
  const tint = pick(random, iconHues)
  const style = { glyphs: pick(random, iconGlyphSets), texture: pick(random, iconTextures) }
  return { tint, style, marks: filledMarks(random, style, profileSlots, minimumProfileMarks) }
}

function halfSlots(size: number): IconSlot[] {
  return Array.from({ length: size }, (_, row) =>
    Array.from({ length: size / 2 }, (_, column) => ({ row, column })),
  ).flat()
}

function filledMarks(
  random: () => number,
  style: IconStyle,
  slots: readonly IconSlot[],
  minimum: number,
): IconCell[] {
  const candidates = slots.map((slot) => ({
    ...slot,
    filled: random() < 0.5,
    glyph: pick(random, style.glyphs),
    corner: pick(random, corners),
    textured: random() < texturedChance,
  }))
  const empty = candidates.filter((candidate) => !candidate.filled)
  const missing = minimum - (candidates.length - empty.length)
  for (const candidate of shuffled(random, empty).slice(0, missing)) {
    candidate.filled = true
  }
  return candidates
    .filter((candidate) => candidate.filled)
    .map(({ row, column, glyph, corner, textured }) => ({ row, column, glyph, corner, textured }))
}

function iconSpec(
  tint: number,
  style: IconStyle,
  size: number,
  marks: readonly IconCell[],
): AgentIconSpec {
  const cells = marks.flatMap((mark) => [
    mark,
    { ...mark, column: size - 1 - mark.column, corner: mirroredCorner(mark.corner) },
  ])
  return { tint, pattern: { style, columns: size, rows: size, cells } }
}

function mirroredCorner(corner: IconCorner): IconCorner {
  switch (corner) {
    case 0:
      return 1
    case 1:
      return 0
    case 2:
      return 3
    case 3:
      return 2
  }
}

function shuffled<T>(random: () => number, items: readonly T[]) {
  return items
    .map((item) => ({ item, key: random() }))
    .sort((left, right) => left.key - right.key)
    .map(({ item }) => item)
}

function pick<T>(random: () => number, values: readonly T[]): T {
  const value = values[Math.floor(random() * values.length)]
  if (value === undefined) throw new Error('Cannot pick from an empty list')
  return value
}

function seededRandom(id: string) {
  let state = hashString(id)
  return () => {
    state = (state + 0x6d2b79f5) | 0
    let mixed = Math.imul(state ^ (state >>> 15), 1 | state)
    mixed = (mixed + Math.imul(mixed ^ (mixed >>> 7), 61 | mixed)) ^ mixed
    return ((mixed ^ (mixed >>> 14)) >>> 0) / 4294967296
  }
}

function hashString(value: string) {
  let hash = 0x811c9dc5
  for (let index = 0; index < value.length; index++) {
    hash ^= value.charCodeAt(index)
    hash = Math.imul(hash, 0x01000193)
  }
  return hash >>> 0
}

export function tintColor(tint: number, alpha = 1) {
  return `oklch(0.62 0.15 ${tint} / ${alpha})`
}
