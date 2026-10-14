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
  axis: MirrorAxis
  sweep?: IconSweep
  columns: number
  rows: number
  cells: IconCell[]
}

export interface AgentIconSpec {
  tint: number
  pattern: IconPattern
}

export type MirrorAxis = 'vertical' | 'horizontal' | 'diagonal' | 'antidiagonal'

export type IconSweep = 'forward' | 'backward'

type CornerMap = readonly [IconCorner, IconCorner, IconCorner, IconCorner]

interface IconSlot {
  row: number
  column: number
}

interface IconMarkSlot extends IconSlot {
  corners: readonly IconCorner[]
}

interface ProfileLayout {
  tint: number
  style: IconStyle
  axis: MirrorAxis
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
const mirrorAxes: readonly MirrorAxis[] = [
  'vertical',
  'vertical',
  'horizontal',
  'horizontal',
  'diagonal',
  'antidiagonal',
]
const iconSweeps: readonly IconSweep[] = ['forward', 'backward']
const mirroredCorners: Record<MirrorAxis, CornerMap> = {
  vertical: [1, 0, 3, 2],
  horizontal: [3, 2, 1, 0],
  diagonal: [0, 3, 2, 1],
  antidiagonal: [2, 1, 0, 3],
}

const profileSize = 2
const agentSize = 4
const centerOffset = (agentSize - profileSize) / 2
const minimumRingMarks = 3
const texturedChance = 0.3

export function profileIcon(profileId: string): AgentIconSpec {
  const { tint, style, axis, marks } = profileLayout(seededRandom(profileId))
  return iconSpec(tint, style, profileSize, axis, marks)
}

export function agentIcon(profileId: string | undefined, agentId: string): AgentIconSpec {
  const agentRandom = seededRandom(agentId)
  const { tint, style, axis, marks } = profileLayout(
    profileId === undefined ? agentRandom : seededRandom(profileId),
    pick(seededRandom(`${agentId}:axis`), mirrorAxes),
  )
  const center = marks.map((mark) => ({
    ...mark,
    row: mark.row + centerOffset,
    column: mark.column + centerOffset,
  }))
  const ringSlots = halfSlots(agentSize, axis).filter(
    (slot) => !inCenter(slot.row) || !inCenter(slot.column),
  )
  const ring = filledMarks(agentRandom, style, ringSlots, minimumRingMarks)
  const sweep = pick(agentRandom, iconSweeps)
  const spec = iconSpec(tint, style, agentSize, axis, [...center, ...ring])
  return { ...spec, pattern: { ...spec.pattern, sweep } }
}

function profileLayout(random: () => number, iconAxis?: MirrorAxis): ProfileLayout {
  const tint = pick(random, iconHues)
  const profileAxis = pick(random, mirrorAxes)
  const glyphs = pick(random, iconGlyphSets)
  const texture = pick(random, iconTextures)
  const axis = iconAxis ?? profileAxis
  const style = { glyphs, texture: axis === 'vertical' || axis === 'horizontal' ? texture : 'dots' }
  const slots = halfSlots(profileSize, axis)
  return { tint, style, axis, marks: filledMarks(random, style, slots, slots.length) }
}

function inCenter(index: number) {
  return index >= centerOffset && index < centerOffset + profileSize
}

function halfSlots(size: number, axis: MirrorAxis): IconMarkSlot[] {
  const last = size - 1
  const inHalf = ({ row, column }: IconSlot) => {
    switch (axis) {
      case 'vertical':
        return column < size / 2
      case 'horizontal':
        return row < size / 2
      case 'diagonal':
        return column >= row
      case 'antidiagonal':
        return row + column <= last
    }
  }
  const symmetricCorners = corners.filter((corner) => mirroredCorners[axis][corner] === corner)
  return Array.from({ length: size }, (_, row) =>
    Array.from({ length: size }, (_, column) => ({ row, column })),
  )
    .flat()
    .filter(inHalf)
    .map((slot) => {
      const mirror = mirroredSlot(slot, axis, size)
      const onAxis = mirror.row === slot.row && mirror.column === slot.column
      return { ...slot, corners: onAxis ? symmetricCorners : corners }
    })
}

function mirroredSlot({ row, column }: IconSlot, axis: MirrorAxis, size: number): IconSlot {
  const last = size - 1
  switch (axis) {
    case 'vertical':
      return { row, column: last - column }
    case 'horizontal':
      return { row: last - row, column }
    case 'diagonal':
      return { row: column, column: row }
    case 'antidiagonal':
      return { row: last - column, column: last - row }
  }
}

function filledMarks(
  random: () => number,
  style: IconStyle,
  slots: readonly IconMarkSlot[],
  minimum: number,
): IconCell[] {
  const candidates = slots.map((slot) => ({
    row: slot.row,
    column: slot.column,
    filled: random() < 0.5,
    glyph: pick(random, style.glyphs),
    corner: pick(random, slot.corners),
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
  axis: MirrorAxis,
  marks: readonly IconCell[],
): AgentIconSpec {
  const cells = marks.flatMap((mark) => {
    const mirror = mirroredSlot(mark, axis, size)
    if (mirror.row === mark.row && mirror.column === mark.column) return [mark]
    return [mark, { ...mark, ...mirror, corner: mirroredCorners[axis][mark.corner] }]
  })
  return { tint, pattern: { style, axis, columns: size, rows: size, cells } }
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
