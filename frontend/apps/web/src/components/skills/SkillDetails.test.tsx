/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type Skill } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { SkillDetails } from '@/components/skills/SkillDetails'
import { jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { waitForUI } from '@/test/secret-editor'

const orgId = fakeId('org')

const skill: Skill = {
  id: fakeId('skl'),
  org_id: orgId,
  owner: { kind: 'org' },
  name: 'deploy',
  revision_id: fakeId('skr'),
  revision: 1,
  description: 'Deploys things.',
  created_at: '2026-09-28T00:00:00Z',
  updated_at: '2026-09-28T00:00:00Z',
}

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

it('shows the skill files as a folder tree', async () => {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({
    fetch: () =>
      Promise.resolve(
        jsonResponse({
          ...skill,
          skill_md: '---\nname: deploy\ndescription: Deploys things.\n---\n',
          files: [
            { path: 'scripts/deploy.sh', size: 120 },
            { path: 'SKILL.md', size: 64 },
            { path: 'reference/flags.md', size: 300 },
          ],
        }),
      ),
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  act(() => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <SkillDetails orgId={orgId} skill={skill} />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
  })

  await waitForUI(() => {
    expect(fileTreeRows()).toEqual(['reference', 'flags.md', 'scripts', 'deploy.sh', 'SKILL.md'])
  })

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
