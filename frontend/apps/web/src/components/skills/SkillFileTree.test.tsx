/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { SkillFileTree } from '@/components/skills/SkillFileTree'
import { enableReactActEnvironment } from '@/test/react-act'

let root: Root
let container: HTMLDivElement
let restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  restore()
})

it('shows the skill files as a folder tree', () => {
  act(() => {
    root.render(
      <SkillFileTree
        files={[
          { path: 'scripts/deploy.sh', size: 120 },
          { path: 'SKILL.md', size: 64 },
          { path: 'reference/flags.md', size: 300 },
        ]}
      />,
    )
  })

  expect(fileTreeRows()).toEqual(['reference', 'flags.md', 'scripts', 'deploy.sh', 'SKILL.md'])

  const scripts = [...container.querySelectorAll('button')].find(
    (folder) => folder.textContent === 'scripts',
  )
  expect(scripts?.getAttribute('aria-expanded')).toBe('true')
  act(() => {
    scripts?.click()
  })
  expect(scripts?.getAttribute('aria-expanded')).toBe('false')
  expect(fileTreeRows()).toEqual(['reference', 'flags.md', 'scripts', 'SKILL.md'])
})

function fileTreeRows() {
  return [...container.querySelectorAll('[aria-label="Skill files"] li')].map(
    (row) => row.querySelector(':scope > button, :scope > span')?.textContent,
  )
}
