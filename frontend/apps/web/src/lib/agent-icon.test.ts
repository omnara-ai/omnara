import { describe, expect, it } from 'vitest'

import {
  agentIcon,
  type AgentIconSpec,
  type IconCell,
  type IconCorner,
  type MirrorAxis,
  profileIcon,
} from '@/lib/agent-icon'

const profileIds = Array.from({ length: 50 }, (_, index) => `aprf_${index.toString(36)}xk2p`)
const agentIds = ['agt_1', 'agt_2', 'agt_3', 'agt_4', 'agt_5']
const mirrors: Record<
  MirrorAxis,
  {
    corners: Record<IconCorner, IconCorner>
    cell: (row: number, column: number, last: number) => [number, number]
  }
> = {
  vertical: {
    corners: { 0: 1, 1: 0, 2: 3, 3: 2 },
    cell: (row, column, last) => [row, last - column],
  },
  horizontal: {
    corners: { 0: 3, 1: 2, 2: 1, 3: 0 },
    cell: (row, column, last) => [last - row, column],
  },
  diagonal: { corners: { 0: 0, 1: 3, 2: 2, 3: 1 }, cell: (row, column) => [column, row] },
  antidiagonal: {
    corners: { 0: 2, 1: 1, 2: 0, 3: 3 },
    cell: (row, column, last) => [last - column, last - row],
  },
}

function cellKey(cell: IconCell, row = cell.row, column = cell.column) {
  return `${row}:${column}:${cell.glyph}:${cell.corner}:${cell.textured}`
}

function cellKeys(icon: AgentIconSpec) {
  return icon.pattern.cells.map((cell) => cellKey(cell)).sort()
}

function centerKeys(icon: AgentIconSpec) {
  return icon.pattern.cells
    .filter((cell) => cell.row >= 1 && cell.row <= 2 && cell.column >= 1 && cell.column <= 2)
    .map((cell) => cellKey(cell, cell.row - 1, cell.column - 1))
    .sort()
}

function expectMirrored(icon: AgentIconSpec) {
  const { axis, columns } = icon.pattern
  const cells = new Set(cellKeys(icon))
  for (const cell of icon.pattern.cells) {
    const [row, column] = mirrors[axis].cell(cell.row, cell.column, columns - 1)
    const mirrored = { ...cell, corner: mirrors[axis].corners[cell.corner] }
    expect(cells.has(cellKey(mirrored, row, column))).toBe(true)
  }
}

function expectDistinctCellsInGrid(icon: AgentIconSpec) {
  const { cells, columns, rows } = icon.pattern
  expect(new Set(cells.map((cell) => `${cell.row}:${cell.column}`)).size).toBe(cells.length)
  for (const cell of cells) {
    expect(cell.row).toBeGreaterThanOrEqual(0)
    expect(cell.row).toBeLessThan(rows)
    expect(cell.column).toBeGreaterThanOrEqual(0)
    expect(cell.column).toBeLessThan(columns)
  }
}

describe('profileIcon', () => {
  it('is stable for the same profile', () => {
    expect(profileIcon('aprf_123')).toEqual(profileIcon('aprf_123'))
  })

  it('uses a 2x2 grid', () => {
    expect(profileIcon('aprf_123').pattern).toMatchObject({ columns: 2, rows: 2 })
  })

  it('mirrors cells across its axis', () => {
    for (const id of profileIds) {
      expectMirrored(profileIcon(id))
    }
  })

  it('varies the mirror axis across profiles', () => {
    expect(new Set(profileIds.map((id) => profileIcon(id).pattern.axis))).toEqual(
      new Set(['vertical', 'horizontal', 'diagonal', 'antidiagonal']),
    )
  })

  it('fills every cell', () => {
    for (const id of profileIds) {
      expect(profileIcon(id).pattern.cells).toHaveLength(4)
    }
  })

  it('draws each cell once inside the grid', () => {
    for (const id of profileIds) {
      expectDistinctCellsInGrid(profileIcon(id))
      expectDistinctCellsInGrid(agentIcon(id, 'agt_1'))
    }
  })

  it('draws only glyphs from the profile style', () => {
    for (const id of profileIds) {
      const { style, cells } = profileIcon(id).pattern
      for (const cell of cells) {
        expect(style.glyphs).toContain(cell.glyph)
      }
    }
  })

  it('spreads tints and glyphs across profiles', () => {
    expect(new Set(profileIds.map((id) => profileIcon(id).tint)).size).toBeGreaterThan(5)
    const glyphs = new Set(profileIds.flatMap((id) => profileIcon(id).pattern.style.glyphs))
    expect(glyphs).toEqual(new Set(['square', 'triangle', 'quarter']))
  })

  it('textures some cells but not all', () => {
    const textured = profileIds
      .flatMap((id) => profileIcon(id).pattern.cells)
      .map((c) => c.textured)
    expect(textured).toContain(true)
    expect(textured).toContain(false)
  })
})

describe('agentIcon', () => {
  it('uses a 4x4 grid', () => {
    expect(agentIcon('aprf_123', 'agt_1').pattern).toMatchObject({ columns: 4, rows: 4 })
  })

  it('frames its profile pattern with the same tint and glyphs', () => {
    for (const profileId of profileIds) {
      const profile = profileIcon(profileId)
      for (const agentId of agentIds) {
        const agent = agentIcon(profileId, agentId)
        expect(agent.tint).toBe(profile.tint)
        expect(agent.pattern.style.glyphs).toEqual(profile.pattern.style.glyphs)
        if (agent.pattern.axis === profile.pattern.axis) {
          expect(centerKeys(agent)).toEqual(cellKeys(profile))
        }
      }
    }
  })

  it('mirrors cells across its own axis', () => {
    for (const profileId of profileIds) {
      for (const agentId of agentIds) {
        expectMirrored(agentIcon(profileId, agentId))
      }
    }
  })

  it('sweeps agents but not profiles', () => {
    for (const profileId of profileIds) {
      expect(profileIcon(profileId).pattern.sweep).toBeUndefined()
      expect(['forward', 'backward']).toContain(agentIcon(profileId, 'agt_1').pattern.sweep)
    }
  })

  it('splits axes evenly between vertical, horizontal, and diagonal', () => {
    const axes = Array.from(
      { length: 600 },
      (_, index) => agentIcon(undefined, `agt_${index.toString(36)}`).pattern.axis,
    )
    const share = (matches: (axis: MirrorAxis) => boolean) =>
      axes.filter(matches).length / axes.length
    expect(share((axis) => axis === 'vertical')).toBeCloseTo(1 / 3, 1)
    expect(share((axis) => axis === 'horizontal')).toBeCloseTo(1 / 3, 1)
    expect(share((axis) => axis === 'diagonal' || axis === 'antidiagonal')).toBeCloseTo(1 / 3, 1)
  })

  it('varies the frame between agents of the same profile', () => {
    const patterns = new Set(
      agentIds.map((agentId) => cellKeys(agentIcon('aprf_123', agentId)).join(' ')),
    )
    expect(patterns.size).toBe(agentIds.length)
  })

  it('derives an agent without a profile from its own id', () => {
    const agent = agentIcon(undefined, 'agt_000')
    const profile = profileIcon('agt_000')
    expect(agent.tint).toBe(profile.tint)
    expect(agent.pattern.style.glyphs).toEqual(profile.pattern.style.glyphs)
  })
})
