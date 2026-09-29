/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type Skill } from '@omnara/sdk'
import { listProjectAvailableSkillsInfiniteQueryKey } from '@omnara/sdk/tanstack'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { AgentConfigSkillSearchbox } from '@/components/agents/AgentConfigSkillSearchbox'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'

const orgId = fakeId('org')
const projectId = fakeId('proj')

function skill(name: string, id: string): Skill {
  return {
    id,
    org_id: orgId,
    owner: { kind: 'project', project_id: projectId },
    name,
    revision_id: fakeId('skr'),
    revision: 1,
    description: `Does ${name} things.`,
    created_at: '2026-09-28T00:00:00Z',
    updated_at: '2026-09-28T00:00:00Z',
  }
}

const skills = [
  skill('attached', `skl_${'a'.repeat(26)}`),
  skill('pdf-tools', `skl_${'b'.repeat(26)}`),
  skill('slides', `skl_${'c'.repeat(26)}`),
]

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

async function render(canCreate = true, expanded = true) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false, staleTime: Infinity } },
  })
  const path = { orgID: orgId, projectID: projectId }
  queryClient.setQueryData(
    listProjectAvailableSkillsInfiniteQueryKey({
      path,
      query: { sort: 'name', limit: 25 },
      client,
    }),
    {
      pages: [
        {
          data: skills.map((item) => ({
            skill: item,
            project_id: projectId,
            availability: { source: 'direct' },
          })),
          next_cursor: null,
        },
      ],
      pageParams: [{ path }],
    },
  )
  const onSelect = vi.fn()
  const onCreateSkill = vi.fn()
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <AgentConfigSkillSearchbox
            orgId={orgId}
            projectId={projectId}
            excludedIds={new Set([`skl_${'a'.repeat(26)}`])}
            excludedNames={new Set()}
            active
            expanded={expanded}
            onSelect={onSelect}
            onCreateSkill={canCreate ? onCreateSkill : undefined}
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  return { onSelect, onCreateSkill }
}

function options() {
  return [...document.querySelectorAll('[role="option"]')]
}

function optionNames() {
  return options().map((option) => option.querySelector('.font-medium')?.textContent)
}

async function type(value: string) {
  await act(async () => {
    const input = document.querySelector('input')
    const descriptor = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')
    descriptor?.set?.call(input, value)
    input?.dispatchEvent(new InputEvent('input', { bubbles: true, inputType: 'insertText' }))
    await Promise.resolve()
  })
}

it('opens focused with Create skill ahead of the unattached skills', async () => {
  await render()
  expect(document.activeElement).toBe(document.querySelector('input[aria-label="Search skills…"]'))
  expect(optionNames()).toEqual(['Create skill', 'pdf-tools', 'slides'])
})

it('moves Create skill below matching skills so Enter attaches the match', async () => {
  const ctx = await render()
  await type('sli')
  expect(optionNames()).toEqual(['slides', 'Create skill'])
  expect(options()[0]?.hasAttribute('data-highlighted')).toBe(true)
  await act(async () => {
    document
      .querySelector('input')
      ?.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
    await Promise.resolve()
  })
  expect(ctx.onSelect).toHaveBeenCalledWith(skills[2])
  expect(ctx.onCreateSkill).not.toHaveBeenCalled()
})

it('keeps Create skill first when nothing matches and opens the create flow', async () => {
  const ctx = await render()
  await type('zzz')
  expect(optionNames()).toEqual(['Create skill'])
  act(() => {
    options()[0]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  expect(ctx.onCreateSkill).toHaveBeenCalledOnce()
  expect(ctx.onSelect).not.toHaveBeenCalled()
})

it('selects a skill from the list', async () => {
  const ctx = await render()
  act(() => {
    options()[2]?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  })
  expect(ctx.onSelect).toHaveBeenCalledWith(skills[2])
})

it('keeps Enter inside the search box when nothing is highlighted', async () => {
  const ctx = await render(false)
  await type('zzz')
  expect(options()).toEqual([])
  const enter = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
  act(() => {
    document.querySelector('input')?.dispatchEvent(enter)
  })
  expect(enter.defaultPrevented).toBe(true)
  expect(ctx.onSelect).not.toHaveBeenCalled()
})

it('omits Create skill without create permission', async () => {
  await render(false)
  expect(optionNames()).toEqual(['pdf-tools', 'slides'])
})

it('keeps the list closed until the search box has expanded', async () => {
  await render(true, false)
  expect(document.activeElement).toBe(document.querySelector('input[aria-label="Search skills…"]'))
  expect(options()).toEqual([])
})
