import { describe, expect, it } from 'vitest'

import { currentSection } from './scoped-sections'

describe('currentSection', () => {
  it('finds the section in org and project paths', () => {
    expect(currentSection('/usage')).toBe('usage')
    expect(currentSection('/projects/proj_x/machines')).toBe('machines')
    expect(currentSection('/projects/proj_x/agents/agt_y/events')).toBe('agents')
  })

  it('keeps agent profile pages in the Agents section', () => {
    expect(currentSection('/projects/proj_x/agent-profiles/aprf_y')).toBe('agents')
  })

  it('keeps memory pages in the Memory section', () => {
    expect(currentSection('/projects/proj_x/memory')).toBe('memory')
    expect(currentSection('/projects/proj_x/memory/mem_y')).toBe('memory')
  })

  it('has no section for overviews', () => {
    expect(currentSection('/')).toBeUndefined()
    expect(currentSection('/projects/proj_x')).toBeUndefined()
  })
})
