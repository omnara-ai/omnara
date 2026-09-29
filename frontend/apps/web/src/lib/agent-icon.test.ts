import { describe, expect, it } from 'vitest'

import { agentIcon, type AgentIconSpec, type IconCell, profileIcon } from '@/lib/agent-icon'

const profileIds = Array.from({ length: 50 }, (_, index) => `aprf_${index.toString(36)}xk2p`)
const agentIds = ['agt_1', 'agt_2', 'agt_3', 'agt_4', 'agt_5']
const mirroredCorners = { 0: 1, 1: 0, 2: 3, 3: 2 } as const

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
  const cells = new Set(cellKeys(icon))
  for (const cell of icon.pattern.cells) {
    const mirrored = { ...cell, corner: mirroredCorners[cell.corner] }
    expect(cells.has(cellKey(mirrored, cell.row, icon.pattern.columns - 1 - cell.column))).toBe(
      true,
    )
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

  it('mirrors cells across the vertical axis', () => {
    for (const id of profileIds) {
      expectMirrored(profileIcon(id))
    }
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

  it('frames its profile pattern with the same tint and style', () => {
    for (const profileId of profileIds) {
      const profile = profileIcon(profileId)
      for (const agentId of agentIds) {
        const agent = agentIcon(profileId, agentId)
        expect(agent.tint).toBe(profile.tint)
        expect(agent.pattern.style).toEqual(profile.pattern.style)
        expect(centerKeys(agent)).toEqual(cellKeys(profile))
      }
    }
  })

  it('mirrors cells across the vertical axis', () => {
    for (const profileId of profileIds) {
      expectMirrored(agentIcon(profileId, 'agt_1'))
    }
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
    expect(centerKeys(agent)).toEqual(cellKeys(profile))
  })
})
