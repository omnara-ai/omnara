/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { createOmnaraClient, type Skill, type VisibleProject } from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BlobWriter, TextReader, ZipWriter } from '@zip.js/zip.js'
import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { CreateSkillDialog } from '@/components/org/CreateSkillDialog'
import { bundleSource, type SkillSource } from '@/lib/skill-bundles'
import { jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { addProject } from '@/test/project-share-chips'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, field, waitForUI } from '@/test/secret-editor'

const orgId = fakeId('org')

function skill(name: string, revision = 1): Skill {
  return {
    id: fakeId('skl'),
    org_id: orgId,
    owner: { kind: 'org' },
    name,
    revision_id: fakeId('skr'),
    revision,
    description: `Does ${name} things.`,
    created_at: '2026-09-28T00:00:00Z',
    updated_at: '2026-09-28T00:00:00Z',
  }
}

function skillMd(name: string) {
  return `---\nname: ${name}\ndescription: Does ${name} things.\n---\n`
}

interface Upload {
  archiveName: string
  respond: (response: Response) => void
}

function arrivals<T>() {
  const items: T[] = []
  let notify: (() => void) | undefined
  return {
    push(item: T) {
      items.push(item)
      notify?.()
    },
    async at(index: number) {
      await act(async () => {
        if (items.length <= index) {
          await new Promise<void>((resolve) => {
            notify = resolve
          })
        }
      })
      const item = items[index]
      if (item === undefined) throw new Error(`Missing arrival ${index}`)
      return item
    },
    get count() {
      return items.length
    },
  }
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

async function render(
  existing: Skill[] = [],
  attachedSkills: Skill[] = [],
  projects: VisibleProject[] = [],
  failingGrants = 0,
) {
  const uploads = arrivals<Upload>()
  const created = arrivals<string[]>()
  const lookups: string[] = []
  const grants: unknown[] = []
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({
    fetch: async (input, init) => {
      const request = new Request(input, init)
      const path = new URL(request.url).pathname
      if (request.method === 'GET' && path.endsWith('/projects')) {
        return jsonResponse({ data: projects, next_cursor: null })
      }
      if (request.method === 'POST' && path.endsWith('/grants')) {
        const body: unknown = await request.json()
        grants.push(body)
        if (grants.length <= failingGrants) {
          return jsonResponse({ code: 'internal', error: 'grant failed' }, 500)
        }
        return jsonResponse(
          {
            id: fakeId('skg'),
            org_id: orgId,
            skill_id: path.split('/').at(-2) ?? '',
            target_project_id: projects[0]?.id ?? '',
            created_at: '2026-09-28T00:00:00Z',
          },
          201,
        )
      }
      if (request.method === 'GET') {
        const name = new URL(request.url).searchParams.get('name') ?? ''
        lookups.push(name)
        return jsonResponse({
          data: existing.filter((item) => item.name === name),
          next_cursor: null,
        })
      }
      const archive = (await request.formData()).get('archive')
      return new Promise<Response>((respond) => {
        uploads.push({ archiveName: archive instanceof File ? archive.name : '', respond })
      })
    },
  })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  const onOpenChange = vi.fn()
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <CreateSkillDialog
            open
            onOpenChange={onOpenChange}
            orgId={orgId}
            owner={{ kind: 'org' }}
            readSource={readSource}
            attachedSkills={attachedSkills}
            onCreated={(skills) => {
              created.push(skills.map((item) => item.name))
            }}
          />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  return { uploads, created, lookups, grants, existing, onOpenChange }
}

async function settle(action: () => void) {
  await act(async () => {
    action()
    await Promise.all(bundling.splice(0))
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
}

async function chooseArchive(file: File) {
  const input = field('Skill files')
  Object.defineProperty(input, 'files', { value: [file], configurable: true })
  await settle(() => {
    input.dispatchEvent(new Event('change', { bubbles: true }))
  })
}

function submit() {
  const form = document.querySelector('form')
  if (!form) throw new Error('Missing skill form')
  act(() => {
    form.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
}

function reviewRows() {
  return [...document.querySelectorAll('ul[aria-label="Skills to upload"] li')].map(
    (row) => row.textContent,
  )
}

async function skillZip(
  files: Record<string, string>,
  name: string,
  symlinks: Record<string, string> = {},
) {
  const writer = new ZipWriter(new BlobWriter('application/zip'), { useWebWorkers: false })
  for (const [path, content] of Object.entries(files)) {
    await writer.add(path, new TextReader(content))
  }
  for (const [path, target] of Object.entries(symlinks)) {
    await writer.add(path, new TextReader(target), { unixMode: 0o120777 })
  }
  return new File([await writer.close()], name)
}

it('reviews a bulk upload, then uploads one at a time and retries only the failures', async () => {
  const ctx = await render([skill('gamma', 2)])
  await chooseArchive(
    await skillZip(
      {
        'collection/alpha/SKILL.md': skillMd('alpha'),
        'collection/alpha-copy/SKILL.md': skillMd('alpha'),
        'collection/beta/SKILL.md': skillMd('beta'),
        'collection/gamma/SKILL.md': skillMd('gamma'),
      },
      'collection.zip',
    ),
  )
  expect(ctx.lookups.sort()).toEqual(['alpha', 'beta', 'gamma'])
  expect(reviewRows()).toEqual([])
  expect(document.body.textContent).toContain('collection.zip4 skills')

  submit()
  expect(reviewRows()).toEqual([
    'alphaNew',
    'alphaDuplicate of collection.zip/collection/alphaSkipped',
    'betaNew',
    'gammav2 → v3 · replaces all files',
  ])
  expect(ctx.uploads.count).toBe(0)

  submit()
  const alpha = await ctx.uploads.at(0)
  expect(alpha.archiveName).toBe('alpha.zip')
  expect(ctx.uploads.count).toBe(1)
  alpha.respond(jsonResponse(skill('alpha'), 201))

  const beta = await ctx.uploads.at(1)
  expect(beta.archiveName).toBe('beta.zip')
  expect(ctx.uploads.count).toBe(2)
  beta.respond(jsonResponse({ code: 'invalid_request', error: 'bad frontmatter' }, 422))

  const gamma = await ctx.uploads.at(2)
  expect(gamma.archiveName).toBe('gamma.zip')
  gamma.respond(jsonResponse(skill('gamma', 3), 201))

  expect(await ctx.created.at(0)).toEqual(['alpha', 'gamma'])
  expect(ctx.onOpenChange).not.toHaveBeenCalled()
  expect(reviewRows()[2]).toBe('betabad frontmatterNew')

  submit()
  const retry = await ctx.uploads.at(3)
  expect(retry.archiveName).toBe('beta.zip')
  retry.respond(jsonResponse(skill('beta'), 201))

  expect(await ctx.created.at(1)).toEqual(['beta'])
  expect(ctx.onOpenChange).toHaveBeenCalledWith(false)
  expect(ctx.uploads.count).toBe(4)
})

it('skips skills with broken frontmatter and returns to the picker on Back', async () => {
  const ctx = await render()
  await chooseArchive(
    await skillZip(
      { 'set/good/SKILL.md': skillMd('good'), 'set/broken/SKILL.md': '# Broken\n' },
      'set.zip',
    ),
  )
  expect(ctx.lookups).toEqual(['good'])
  submit()
  expect(reviewRows()).toEqual([
    "brokenSKILL.md is missing YAML frontmatter delimited by '---'.Skipped",
    'goodNew',
  ])
  expect(button('Upload skill').disabled).toBe(false)

  act(() => {
    button('Back').click()
  })
  expect(reviewRows()).toEqual([])
  expect(document.body.textContent).toContain('set.zip2 skills')
})

it('uploads a single new skill straight from the picker', async () => {
  const ctx = await render()
  await chooseArchive(await skillZip({ 'solo/SKILL.md': skillMd('solo') }, 'solo.zip'))
  expect(document.body.textContent).toContain('solo.zipsolo')

  submit()
  expect(reviewRows()).toEqual([])
  const solo = await ctx.uploads.at(0)
  expect(solo.archiveName).toBe('solo.zip')
  solo.respond(jsonResponse(skill('solo'), 201))
  expect(await ctx.created.at(0)).toEqual(['solo'])
  expect(ctx.onOpenChange).toHaveBeenCalledWith(false)
})

const project = {
  id: fakeId('proj'),
  org_id: orgId,
  name: 'cli-agent',
  access: { can_read: true, can_manage: true, can_manage_access: true, can_operate: true },
  created_at: '2026-09-28T00:00:00Z',
  updated_at: '2026-09-28T00:00:00Z',
}

async function pickProject(name: string) {
  await waitForUI(() => {
    expect(button('Add project').getAttribute('data-disabled')).toBeNull()
  })
  await addProject(name)
  expect(button(`Remove ${name}`)).toBeDefined()
}

it('shares a new skill with the projects picked in the footer', async () => {
  const ctx = await render([], [], [project])
  await chooseArchive(await skillZip({ 'solo/SKILL.md': skillMd('solo') }, 'solo.zip'))
  await pickProject('cli-agent')

  submit()
  const solo = await ctx.uploads.at(0)
  solo.respond(jsonResponse(skill('solo'), 201))
  expect(await ctx.created.at(0)).toEqual(['solo'])
  await waitForUI(() => {
    expect(ctx.grants).toEqual([{ target_project_id: project.id }])
  })
  expect(ctx.onOpenChange).toHaveBeenCalledWith(false)
})

it('says why sharing failed and finishes once the failing project is removed', async () => {
  const ctx = await render([], [], [project], 1)
  await chooseArchive(await skillZip({ 'solo/SKILL.md': skillMd('solo') }, 'solo.zip'))
  await pickProject('cli-agent')

  submit()
  ;(await ctx.uploads.at(0)).respond(jsonResponse(skill('solo'), 201))
  await waitForUI(() => {
    expect(document.body.textContent).toContain(
      'The skill was created. Sharing with 1 project failed: grant failed. The failed projects are still selected — retry or remove them.',
    )
  })
  expect(document.querySelector('[data-failed]')?.textContent).toContain('cli-agent')
  expect(ctx.onOpenChange).not.toHaveBeenCalled()

  await settle(() => {
    button('Remove cli-agent').click()
  })
  submit()
  await waitForUI(() => {
    expect(ctx.onOpenChange).toHaveBeenCalledWith(false)
  })
  expect(ctx.grants).toHaveLength(1)
})

it('stays open after retrying shares while failed uploads still need retrying', async () => {
  const ctx = await render([], [], [project], 1)
  await chooseArchive(
    await skillZip(
      { 'pair/alpha/SKILL.md': skillMd('alpha'), 'pair/beta/SKILL.md': skillMd('beta') },
      'pair.zip',
    ),
  )
  await pickProject('cli-agent')
  submit()
  submit()
  ;(await ctx.uploads.at(0)).respond(jsonResponse(skill('alpha'), 201))
  ;(await ctx.uploads.at(1)).respond(
    jsonResponse({ code: 'invalid_request', error: 'bad frontmatter' }, 422),
  )
  await waitForUI(() => {
    expect(ctx.grants).toHaveLength(1)
    expect(document.body.textContent).toContain('Retry sharing')
  })

  submit()
  await waitForUI(() => {
    expect(ctx.grants).toHaveLength(2)
    expect(document.body.textContent).toContain('Retry upload')
  })
  expect(ctx.onOpenChange).not.toHaveBeenCalled()
  expect(reviewRows()[1]).toBe('betabad frontmatterNew')

  submit()
  ;(await ctx.uploads.at(2)).respond(jsonResponse(skill('beta'), 201))
  await waitForUI(() => {
    expect(ctx.grants).toHaveLength(3)
  })
  expect(ctx.onOpenChange).toHaveBeenCalledWith(false)
})

it('warns before a new skill replaces an attached skill with the same name', async () => {
  const ctx = await render([], [skill('solo')])
  await chooseArchive(await skillZip({ 'solo/SKILL.md': skillMd('solo') }, 'solo.zip'))

  submit()
  expect(reviewRows()).toEqual(['soloReplaces the attached organization skill.New'])
  expect(ctx.uploads.count).toBe(0)

  submit()
  const solo = await ctx.uploads.at(0)
  solo.respond(jsonResponse(skill('solo'), 201))
  expect(await ctx.created.at(0)).toEqual(['solo'])
})

it('reports a picked archive without any SKILL.md', async () => {
  const ctx = await render()
  await chooseArchive(await skillZip({ 'notes/README.md': 'hi' }, 'notes.zip'))
  expect(document.querySelector('[role="alert"]')?.textContent).toBe(
    'No SKILL.md found in notes.zip.',
  )
  expect(ctx.lookups).toEqual([])
  expect(button('Create skill').disabled).toBe(true)
})

it('reports broken frontmatter in a single picked skill without opening the review', async () => {
  const ctx = await render()
  await chooseArchive(await skillZip({ 'broken/SKILL.md': '# Broken\n' }, 'broken.zip'))
  expect(document.querySelector('[role="alert"]')?.textContent).toBe(
    "SKILL.md is missing YAML frontmatter delimited by '---'.",
  )
  expect(ctx.lookups).toEqual([])
  expect(button('Create skill').disabled).toBe(true)
})

it('reports every broken skill up front when none in the selection are valid', async () => {
  const ctx = await render()
  await chooseArchive(
    await skillZip(
      { 'set/first/SKILL.md': '# First\n', 'set/second/SKILL.md': '# Second\n' },
      'set.zip',
    ),
  )
  expect(document.querySelector('[role="alert"]')?.textContent).toBe(
    [
      "first: SKILL.md is missing YAML frontmatter delimited by '---'.",
      "second: SKILL.md is missing YAML frontmatter delimited by '---'.",
    ].join('\n'),
  )
  expect(ctx.lookups).toEqual([])
  expect(button('Create skill').disabled).toBe(true)
})

it('names the symlink when a picked zip contains one', async () => {
  const ctx = await render()
  await chooseArchive(
    await skillZip({ 'linked/SKILL.md': skillMd('linked') }, 'linked.zip', {
      'linked/data': '/etc/passwd',
    }),
  )
  expect(document.querySelector('[role="alert"]')?.textContent).toBe(
    'Skill archive contains a symlink at linked/data.',
  )
  expect(ctx.lookups).toEqual([])
})

it('confirms before a pasted SKILL.md creates a new revision of an existing skill', async () => {
  const ctx = await render([skill('my-skill', 4)])
  const trigger = [...document.querySelectorAll('[role="tab"]')].find(
    (tab) => tab.textContent === 'SKILL.md',
  )
  act(() => {
    trigger?.dispatchEvent(new MouseEvent('mousedown', { bubbles: true, button: 0 }))
  })
  expect(button('Create skill').disabled).toBe(false)

  await settle(() => {
    const form = document.querySelector('form')
    form?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
  expect(ctx.lookups).toEqual(['my-skill'])
  expect(reviewRows()).toEqual(['my-skillv4 → v5 · replaces all files'])
  expect(ctx.uploads.count).toBe(0)

  submit()
  const upload = await ctx.uploads.at(0)
  expect(upload.archiveName).toBe('my-skill.zip')
  upload.respond(jsonResponse(skill('my-skill', 5), 201))
  expect(await ctx.created.at(0)).toEqual(['my-skill'])
  expect(ctx.onOpenChange).toHaveBeenCalledWith(false)
})

it('drops already uploaded skills from the selection on Back', async () => {
  const ctx = await render()
  await chooseArchive(
    await skillZip(
      { 'pair/one/SKILL.md': skillMd('one'), 'pair/two/SKILL.md': skillMd('two') },
      'pair.zip',
    ),
  )
  submit()
  submit()
  const one = await ctx.uploads.at(0)
  one.respond(jsonResponse(skill('one'), 201))
  const two = await ctx.uploads.at(1)
  two.respond(jsonResponse({ code: 'invalid_request', error: 'rejected' }, 422))
  expect(await ctx.created.at(0)).toEqual(['one'])

  await settle(() => {
    button('Back').click()
  })
  expect(document.body.textContent).toContain('pair.ziptwo')

  submit()
  const retry = await ctx.uploads.at(2)
  expect(retry.archiveName).toBe('two.zip')
  retry.respond(jsonResponse(skill('two'), 201))
  expect(await ctx.created.at(1)).toEqual(['two'])
  expect(ctx.uploads.count).toBe(3)
})

it('rechecks the remaining skills against the server after Back', async () => {
  const ctx = await render()
  await chooseArchive(
    await skillZip(
      {
        'set/foo/SKILL.md': skillMd('foo'),
        'set/foo-copy/SKILL.md': skillMd('foo'),
        'set/bar/SKILL.md': skillMd('bar'),
      },
      'set.zip',
    ),
  )
  submit()
  expect(reviewRows()).toEqual(['barNew', 'fooNew', 'fooDuplicate of set.zip/set/fooSkipped'])

  submit()
  const bar = await ctx.uploads.at(0)
  bar.respond(jsonResponse({ code: 'invalid_request', error: 'rejected' }, 422))
  const foo = await ctx.uploads.at(1)
  ctx.existing.push(skill('foo'))
  foo.respond(jsonResponse(skill('foo'), 201))
  expect(await ctx.created.at(0)).toEqual(['foo'])

  ctx.lookups.length = 0
  await settle(() => {
    button('Back').click()
  })
  expect(ctx.lookups.sort()).toEqual(['bar', 'foo'])
  expect(document.body.textContent).toContain('set.zip2 skills')

  submit()
  expect(reviewRows()).toEqual(['barNew', 'foov1 → v2 · replaces all files'])
})
