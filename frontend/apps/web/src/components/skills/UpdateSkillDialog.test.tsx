/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type Skill } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BlobWriter, TextReader, ZipWriter } from '@zip.js/zip.js'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { UpdateSkillDialog } from '@/components/skills/UpdateSkillDialog'
import { bundleSource, type SkillSource } from '@/lib/skill-bundles'
import { jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, field } from '@/test/secret-editor'

const orgId = fakeId('org')

const current: Skill = {
  id: fakeId('skl'),
  org_id: orgId,
  owner: { kind: 'org' },
  name: 'deploy',
  revision_id: fakeId('skr'),
  revision: 2,
  description: 'Deploys things.',
  created_at: '2026-09-28T00:00:00Z',
  updated_at: '2026-09-28T00:00:00Z',
}

function skillMd(name: string) {
  return `---\nname: ${name}\ndescription: Does ${name} things.\n---\n`
}

let root: Root
let container: HTMLDivElement
const bundling: Promise<unknown>[] = []

function readSource(source: SkillSource) {
  const bundles = bundleSource(source)
  bundling.push(bundles.catch(() => undefined))
  return bundles
}
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

async function render() {
  const archives: string[] = []
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({
    fetch: async (input, init) => {
      const request = new Request(input, init)
      if (request.method === 'GET') {
        return jsonResponse({ ...current, skill_md: skillMd(current.name) })
      }
      const archive = (await request.formData()).get('archive')
      archives.push(archive instanceof File ? archive.name : '')
      return jsonResponse({ ...current, revision: current.revision + 1 })
    },
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const onOpenChange = vi.fn()
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <UpdateSkillDialog
            open
            onOpenChange={onOpenChange}
            orgId={orgId}
            skill={current}
            readSource={readSource}
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  const trigger = [...document.querySelectorAll('[role="tab"]')].find(
    (tab) => tab.textContent === 'Upload',
  )
  act(() => {
    trigger?.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }))
  })
  return { archives, onOpenChange }
}

async function chooseFolder(files: Record<string, string>) {
  const input = field('Skill folder')
  const picked = Object.entries(files).map(([path, content]) => {
    const file = new File([content], path.slice(path.lastIndexOf('/') + 1))
    Object.defineProperty(file, 'webkitRelativePath', { value: path })
    return file
  })
  Object.defineProperty(input, 'files', { value: picked, configurable: true })
  await act(async () => {
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await Promise.all(bundling.splice(0))
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function chooseZip(files: Record<string, string>, name: string) {
  const writer = new ZipWriter(new BlobWriter('application/zip'), { useWebWorkers: false })
  for (const [path, content] of Object.entries(files)) {
    await writer.add(path, new TextReader(content))
  }
  const input = field('Skill files')
  Object.defineProperty(input, 'files', {
    value: [new File([await writer.close()], name)],
    configurable: true,
  })
  await act(async () => {
    input.dispatchEvent(new Event('change', { bubbles: true }))
    await Promise.all(bundling.splice(0))
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

function alert() {
  return document.querySelector('[role="alert"]')?.textContent
}

it('uploads a picked folder as a new revision, whatever the folder is called', async () => {
  const ctx = await render()
  await chooseFolder({
    'my-checkout/SKILL.md': skillMd('deploy'),
    'my-checkout/scripts/run.sh': 'echo hi',
  })
  expect(alert()).toBeUndefined()

  await act(async () => {
    button('Save').click()
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  expect(ctx.archives).toEqual(['deploy.zip'])
  expect(ctx.onOpenChange).toHaveBeenCalledWith(false)
})

it('rejects an upload whose SKILL.md names a different skill', async () => {
  const ctx = await render()
  await chooseZip({ 'other/SKILL.md': skillMd('other') }, 'other.zip')
  expect(alert()).toBe('SKILL.md frontmatter `name` must stay `deploy`.')
  expect(button('Save').disabled).toBe(true)
  expect(ctx.archives).toEqual([])
})

it('rejects an upload containing more than one skill', async () => {
  await render()
  await chooseZip(
    { 'set/deploy/SKILL.md': skillMd('deploy'), 'set/lint/SKILL.md': skillMd('lint') },
    'set.zip',
  )
  expect(alert()).toBe('set.zip contains 2 skills. Choose a single skill.')
  expect(button('Save').disabled).toBe(true)
})
