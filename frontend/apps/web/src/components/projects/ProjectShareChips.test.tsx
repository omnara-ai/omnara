/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, expect, it } from 'vitest'

import { fakeApi, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { addProject, openProjectMenu, projectOptions } from '@/test/project-share-chips'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, waitForUI } from '@/test/secret-editor'

import { ProjectShareChips } from './ProjectShareChips'

const orgId = fakeId('org')
const timestamp = '2026-01-01T00:00:00Z'
const projects = [
  { name: 'Alpha', manage: true },
  { name: 'Beta', manage: true },
  { name: 'Bravo', manage: true },
  { name: 'Read-only', manage: false },
  { name: 'Owner', manage: true },
].map(({ name, manage }, index) => ({
  id: `proj_${'abcde'.charAt(index).repeat(26)}`,
  org_id: orgId,
  name,
  access: { can_read: true, can_manage: manage, can_manage_access: manage, can_operate: true },
  created_at: timestamp,
  updated_at: timestamp,
}))
const owner = projects[4]?.id ?? ''

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

beforeAll(() => {
  restoreActEnvironment = enableReactActEnvironment()
})
afterAll(() => {
  restoreActEnvironment()
})
beforeEach(() => {
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

/** Every selection the picker reported, latest last. */
const selections: string[][] = []

function Picker({ failed }: { failed?: string[] }) {
  const [value, setValue] = useState<string[]>([])
  return (
    <ProjectShareChips
      orgId={orgId}
      value={value}
      onChange={(projectIds) => {
        selections.push(projectIds)
        setValue(projectIds)
      }}
      excludedProjectIds={[owner]}
      failedProjectIds={failed}
      isProjectEligible={(project) => project.access.can_manage_access}
    />
  )
}

async function render(failed?: string[]) {
  const api = fakeApi([
    {
      method: 'GET',
      path: `/api/v1/orgs/${orgId}/projects`,
      respond: () => jsonResponse({ data: projects, next_cursor: null }),
    },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1', fetch: api.fetch })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <Picker failed={failed} />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  await waitForUI(() => {
    expect(button('Add project').getAttribute('data-disabled')).toBeNull()
  })
}

function projectId(name: string) {
  return projects.find((project) => project.name === name)?.id ?? ''
}

it('offers eligible projects, filtered by search, and adds the picked one as a chip', async () => {
  await render()
  await openProjectMenu()
  expect(projectOptions()).toEqual(['Alpha', 'Beta', 'Bravo'])

  await openProjectMenu('br')
  expect(projectOptions()).toEqual(['Bravo'])
  for (const key of ['ArrowDown', 'Enter']) {
    await act(async () => {
      document
        .querySelector('input[aria-label="Search projects…"]')
        ?.dispatchEvent(new KeyboardEvent('keydown', { key, bubbles: true }))
      await new Promise((resolve) => setTimeout(resolve, 0))
    })
  }
  expect(selections.at(-1)).toEqual([projectId('Bravo')])
  await openProjectMenu('')
  expect(projectOptions()).toEqual(['Alpha', 'Beta', 'Bravo'])

  await addProject('Alpha')
  expect(selections.at(-1)).toEqual([projectId('Bravo'), projectId('Alpha')])
  expect(button('Remove Bravo')).toBeDefined()

  await act(async () => {
    button('Remove Bravo').click()
    await Promise.resolve()
  })
  expect(selections.at(-1)).toEqual([projectId('Alpha')])
})

it('highlights the chips whose share failed', async () => {
  await render([projectId('Beta')])
  await addProject('Alpha')
  await addProject('Beta')

  const failedChips = [...document.querySelectorAll('[data-failed]')].map(
    (chip) => chip.textContent,
  )
  expect(failedChips).toEqual(['Beta(sharing failed)'])
})
