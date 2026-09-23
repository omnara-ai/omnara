import { randomUUID } from 'node:crypto'
import { readFile } from 'node:fs/promises'

import { expect, type Page, type Request, test } from '@playwright/test'
import { z } from 'zod'

import { installFailureTracking, requiredEnvironmentVariable, signIn } from './helpers'

const projectID = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROJECT_ID')
const adminEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_ADMIN_EMAIL')
const viewerEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_VIEWER_EMAIL')
const memoryPath = `/projects/${projectID}/memory`
const fileRoute = '**/memory-stores/*/file?*'

async function createMemoryStore(page: Page, expectedFileStatuses: number[] = []) {
  const ignore = [
    /^request: .*memory-stores\/mst_[a-z2-7]+(?:\/files?\?[^ ]+)? \(net::ERR_ABORTED\)$/,
    /^page: Canceled$/,
  ]
  const failures = installFailureTracking(page, ignore)
  const storeName = `memory-browser-${randomUUID()}`
  await signIn(page, adminEmail, memoryPath)
  await expect(page.getByRole('link', { name: 'Memory guide' })).toHaveAttribute(
    'href',
    'https://docs.omnara.com/agents/configuration#memory-stores',
  )
  await page.getByRole('button', { name: 'Create store', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill(storeName)
  await dialog.getByLabel('Description').fill('Browser verification notes')
  await dialog.getByRole('button', { name: 'Create store', exact: true }).click()
  await page.getByRole('link', { name: storeName, exact: true }).click()
  await expect(page.getByRole('heading', { name: storeName, exact: true })).toBeVisible()
  const storeID = new URL(page.url()).pathname.split('/').at(-1)
  for (const status of expectedFileStatuses) {
    ignore.push(
      new RegExp(
        `^response: ${status} /api/v1/orgs/[^/]+/projects/${projectID}/memory-stores/${storeID}/file$`,
      ),
    )
  }
  return { failures, storeName, ignore }
}

async function createMemoryFile(page: Page, content = 'Original notes') {
  const dialog = page.getByRole('dialog')
  await page.getByRole('button', { name: 'Add file', exact: true }).click()
  await dialog.getByLabel('Path', { exact: true }).fill('notes/plan.txt')
  await dialog.getByLabel('Content', { exact: true }).fill(content)
  const upload = page.waitForRequest(
    (request) => request.method() === 'PUT' && /\/memory-stores\/[^/]+\/file\?/.test(request.url()),
  )
  await dialog.getByRole('button', { name: 'Create file', exact: true }).click()
  const request = await upload
  const fileURL = new URL(request.url())
  const editor = page.getByRole('textbox', { name: 'notes/plan.txt', exact: true })
  await expect(editor).toBeVisible()
  return { editor, fileURL, request }
}

test('memory files preserve text bytes, guard unsaved edits, download, and delete', async ({
  page,
}) => {
  const { failures, ignore } = await createMemoryStore(page)
  const downloads: string[] = []
  page.on('request', (request) => {
    if (request.method() === 'GET' && /\/memory-stores\/[^/]+\/file\?/.test(request.url()))
      downloads.push(request.url())
  })
  const { editor, fileURL } = await createMemoryFile(page, '\ufeffOriginal notes')
  await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled()
  expect(downloads).toEqual([])
  await editor.focus()
  await page.keyboard.press('Control+A')
  await page.keyboard.insertText('Edited notes')
  await expect(page.getByText('Unsaved changes', { exact: false })).toBeVisible()
  await page.keyboard.press('Control+z')
  await expect(page.locator('.view-lines')).not.toContainText('Edited notes')
  await page.keyboard.press('Control+Shift+z')
  await expect(page.locator('.view-lines')).toContainText('Edited notes')
  page.once('dialog', (prompt) => prompt.dismiss())
  await page.getByRole('link', { name: '← All stores' }).click()
  await expect(editor).toBeVisible()
  const saveBanner = await page.evaluateHandle(() => {
    let seen = false
    const observer = new MutationObserver(() => {
      if (document.body.textContent.includes('This file changed since you opened it')) seen = true
    })
    observer.observe(document.body, { childList: true, subtree: true, characterData: true })
    return {
      finish: () => {
        observer.disconnect()
        return seen
      },
    }
  })
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(page.getByText('Unsaved changes', { exact: false })).toBeHidden()
  const savedFile = await page.request.get(fileURL.toString())
  expect(savedFile.ok()).toBe(true)
  expect(await savedFile.body()).toEqual(Buffer.from('\ufeffEdited notes'))
  expect(await saveBanner.evaluate((state) => state.finish())).toBe(false)
  await saveBanner.dispose()
  await page.screenshot({ path: test.info().outputPath('memory-store.png') })
  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('button', { name: 'Download', exact: true }).click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe('plan.txt')
  const downloadedPath = await download.path()
  expect(await readFile(downloadedPath, 'utf8')).toBe('\ufeffEdited notes')
  await editor.focus()
  await page.keyboard.insertText('Unsaved before deletion')
  await expect(page.getByText('Unsaved changes', { exact: false })).toBeVisible()
  page.once('dialog', (prompt) => prompt.accept())
  ignore.push(new RegExp(`^response: 404 ${fileURL.pathname}s$`))
  await page.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(page.getByText('This folder is empty.')).toBeVisible()
  expect(failures).toEqual([])
})

test('memory markdown preserves the editor across preview switches', async ({ page }) => {
  const { failures } = await createMemoryStore(page)
  await page.getByRole('button', { name: 'Add file', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Path', { exact: true }).fill('notes.md')
  await dialog.getByLabel('Content', { exact: true }).fill('Original notes')
  await dialog.getByRole('button', { name: 'Create file', exact: true }).click()
  await expect(dialog).toBeHidden()
  const editor = page.getByRole('textbox', { name: 'notes.md', exact: true })
  await expect(editor).toHaveCount(0)
  await page.getByRole('tab', { name: 'Edit', exact: true }).click()
  await editor.focus()
  await page.keyboard.press('End')
  await page.keyboard.insertText(' edited')
  await page.getByRole('tab', { name: 'Preview', exact: true }).click()
  await expect(editor).toBeHidden()
  await page.getByRole('tab', { name: 'Edit', exact: true }).click()
  await editor.focus()
  await page.keyboard.press('Control+z')
  await expect(page.locator('.view-lines')).toHaveText('Original notes')
  await page.keyboard.press('Control+Shift+z')
  await page.keyboard.insertText('!')
  await expect(page.locator('.view-lines')).toHaveText('Original notes edited!')
  expect(failures).toEqual([])
})

test('memory uploads replace existing files without downloading and preserve editor drafts', async ({
  page,
}) => {
  const { failures } = await createMemoryStore(page, [409])
  const { editor } = await createMemoryFile(page)
  const dialog = page.getByRole('dialog')
  await editor.focus()
  await page.keyboard.press('Control+A')
  await page.keyboard.insertText('Unsaved local edits')
  await expect(page.locator('.view-lines')).toContainText('Unsaved local edits')
  await page.getByRole('button', { name: 'Add file', exact: true }).click()
  await dialog.getByRole('tab', { name: 'Upload file', exact: true }).click()
  await dialog.getByLabel('File', { exact: true }).setInputFiles({
    name: 'plan.txt',
    mimeType: 'application/octet-stream',
    buffer: Buffer.from('Replacement notes'),
  })
  const conflictDownloads: string[] = []
  const trackConflictDownload = (request: Request) => {
    if (request.method() === 'GET' && /\/memory-stores\/[^/]+\/file\?/.test(request.url()))
      conflictDownloads.push(request.url())
  }
  page.on('request', trackConflictDownload)
  await dialog.getByRole('button', { name: 'Upload', exact: true }).click()
  await expect(
    dialog.getByText('A file already exists at this path.', { exact: false }),
  ).toBeVisible()
  expect(conflictDownloads).toEqual([])
  page.off('request', trackConflictDownload)
  await dialog.getByRole('button', { name: 'Replace file', exact: true }).click()
  await expect(dialog).toBeHidden()
  let releaseSave!: () => void
  const holdSave = new Promise<void>((resolve) => {
    releaseSave = resolve
  })
  await page.route(
    fileRoute,
    async (route) => {
      await holdSave
      await route.continue()
    },
    { times: 1 },
  )
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Load latest', exact: true })).toBeHidden()
  releaseSave()
  const replace = page.getByRole('button', { name: 'Replace current contents', exact: true })
  await expect(replace).toBeEnabled()
  await expect(page.getByRole('button', { name: 'Load latest', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Check latest', exact: true })).toBeHidden()
  await expect(page.locator('.view-lines')).toContainText('Unsaved local edits')
  await replace.click()
  await expect(page.getByText('Unsaved changes', { exact: false })).toBeHidden()
  await expect(page.getByRole('button', { name: 'Load latest', exact: true })).toBeHidden()
  expect(failures).toEqual([])
})

test('memory editor preserves drafts across a failed conflict refresh and retry', async ({
  page,
}) => {
  const { failures } = await createMemoryStore(page, [403, 409])
  const { editor } = await createMemoryFile(page)
  await editor.focus()
  await page.keyboard.press('Control+A')
  await page.keyboard.insertText('More unsaved local edits')
  await page.route(
    fileRoute,
    async (route) => {
      const remoteWrite = await route.fetch({ postData: 'Replacement notes' })
      expect(remoteWrite.ok()).toBe(true)
      await route.continue()
    },
    { times: 1 },
  )
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  const replace = page.getByRole('button', { name: 'Replace current contents', exact: true })
  const conflictAlert = page.getByRole('alert').filter({ hasText: 'This file changed.' })
  await expect(conflictAlert).toHaveText('This file changed. Check latest')
  await expect(replace).toBeEnabled()
  await page.route(
    fileRoute,
    (route) =>
      route.fulfill({
        status: 403,
        json: { error: 'File refresh denied', code: 'forbidden' },
      }),
    { times: 1 },
  )
  await page.getByRole('button', { name: 'Check latest', exact: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'File refresh denied' })).toBeVisible()
  await expect(page.locator('.view-lines')).toContainText('More unsaved local edits')
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(page.getByText('File refresh denied', { exact: true })).toBeHidden()
  await expect(conflictAlert).toBeHidden()
  page.once('dialog', (prompt) => prompt.accept())
  await page.getByRole('button', { name: 'Load latest', exact: true }).click()
  await expect(page.locator('.view-lines')).toContainText('Replacement notes')
  expect(failures).toEqual([])
})

for (const concurrentRecreation of [false, true]) {
  test(`memory editor recreates a deleted file${concurrentRecreation ? ' with a concurrent writer' : ''}`, async ({
    page,
  }) => {
    const { failures } = await createMemoryStore(page, [404, 409])
    const { editor, fileURL } = await createMemoryFile(page)
    await editor.focus()
    await page.keyboard.press('Control+A')
    await page.keyboard.insertText(`Recovered draft ${concurrentRecreation}`)
    await page.route(
      fileRoute,
      async (route) => {
        const deleted = await route.fetch({ method: 'DELETE', postData: '' })
        expect(deleted.status()).toBe(204)
        await route.continue()
      },
      { times: 1 },
    )
    await page.getByRole('button', { name: 'Save changes', exact: true }).click()
    const recreate = page.getByRole('button', { name: 'Recreate file', exact: true })
    await expect(recreate).toBeEnabled()
    await expect(page.getByRole('alert')).toHaveText('This file no longer exists. Check latest')
    const missing = page.waitForResponse(
      (response) => response.url() === fileURL.toString() && response.status() === 404,
    )
    await page.getByRole('button', { name: 'Check latest', exact: true }).click()
    await missing
    await page.route(
      fileRoute,
      async (route) => {
        expect(new URL(route.request().url()).searchParams.has('expected_digest')).toBe(false)
        if (concurrentRecreation) {
          const created = await route.fetch({ postData: 'Another writer' })
          expect(created.ok()).toBe(true)
        }
        await route.continue()
      },
      { times: 1 },
    )
    await recreate.click()
    if (concurrentRecreation) {
      const replace = page.getByRole('button', { name: 'Replace current contents', exact: true })
      await expect(replace).toBeEnabled()
      const current = await page.request.get(fileURL.toString())
      expect(await current.text()).toBe('Another writer')
      await expect(page.locator('.view-lines')).toContainText('Recovered draft true')
      await replace.click()
    }
    await expect(page.getByText('Unsaved changes', { exact: false })).toBeHidden()
    const restored = await page.request.get(fileURL.toString())
    expect(await restored.text()).toBe(`Recovered draft ${concurrentRecreation}`)
    expect(failures).toEqual([])
  })
}

test('memory uploads allow empty files and distinguish quota errors from conflicts', async ({
  page,
}) => {
  const { failures } = await createMemoryStore(page, [409])
  const dialog = page.getByRole('dialog')
  await page.getByRole('button', { name: 'Add file', exact: true }).click()
  await dialog.getByRole('tab', { name: 'Upload file', exact: true }).click()
  await dialog.getByLabel('Path', { exact: true }).fill('notes/empty.bin')
  await dialog.getByLabel('File', { exact: true }).setInputFiles({
    name: 'empty.bin',
    mimeType: 'application/octet-stream',
    buffer: Buffer.alloc(0),
  })
  await page.route(
    fileRoute,
    (route) =>
      route.fulfill({
        status: 409,
        json: { error: 'memory file limit reached', code: 'conflict' },
      }),
    { times: 1 },
  )
  await dialog.getByRole('button', { name: 'Upload', exact: true }).click()
  await expect(dialog.getByRole('alert')).toHaveText('memory file limit reached')
  await expect(dialog.getByRole('button', { name: 'Upload', exact: true })).toBeEnabled()
  await expect(dialog.getByRole('button', { name: 'Replace file', exact: true })).toBeHidden()
  await dialog.getByRole('button', { name: 'Upload', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'notes/empty.bin', exact: true })).toBeVisible()
  expect(failures).toEqual([])
})

test('memory store settings preserve previews and control default agent access', async ({
  page,
}) => {
  const { failures, storeName } = await createMemoryStore(page)
  await createMemoryFile(page)
  const dialog = page.getByRole('dialog')
  let fileDownloads = 0
  page.on('request', (request) => {
    if (request.method() === 'GET' && /\/memory-stores\/[^/]+\/file\?/.test(request.url()))
      fileDownloads++
  })
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  await dialog.getByLabel('Read-only for agents', { exact: true }).check()
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByText('Read-only for agents', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add file', exact: true })).toBeEnabled()
  expect(fileDownloads).toBe(0)
  const storeURL = new URL(page.url()).pathname + new URL(page.url()).search
  await signIn(page, adminEmail, `/projects/${projectID}/agents/new`)
  await page.getByRole('button', { name: 'Attach memory store', exact: true }).click()
  await page.getByRole('combobox', { name: 'Search memory stores…', exact: true }).click()
  await page.getByPlaceholder('Search memory stores…').fill(storeName)
  await page.getByRole('option', { name: new RegExp(storeName) }).click()
  await expect(page.getByRole('combobox', { name: `Access to ${storeName}` })).toContainText(
    'Read-only',
  )
  await page.getByRole('combobox', { name: `Access to ${storeName}` }).click()
  await page.getByRole('option', { name: 'Read & write', exact: true }).click()
  await expect(page.getByRole('combobox', { name: `Access to ${storeName}` })).toContainText(
    'Read & write',
  )
  await expect(page.getByText('This store is read-only for agents', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: `Detach ${storeName}` }).click()
  await page.goto(storeURL)
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  page.once('dialog', (prompt) => prompt.accept())
  await dialog.getByRole('button', { name: 'Delete store', exact: true }).click()
  await expect(page).toHaveURL(memoryPath)
  expect(failures).toEqual([])
})

test('memory viewers follow remote text and binary updates without an editable draft', async ({
  page,
}) => {
  const { failures } = await createMemoryStore(page)
  const { fileURL, request } = await createMemoryFile(page)
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Read-only for agents', { exact: true }).check()
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog).toBeHidden()
  const storeURL = new URL(page.url()).pathname + new URL(page.url()).search
  await page.context().clearCookies()
  await signIn(page, viewerEmail, storeURL)
  await expect(page.getByRole('heading', { name: 'notes/plan.txt', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Settings', exact: true })).toBeHidden()
  const uploadHeaders = await request.allHeaders()
  const headers = {
    'Content-Type': 'application/octet-stream',
    Origin: fileURL.origin,
    Cookie: z.string().parse(uploadHeaders.cookie),
    'X-Omnara-Csrf': z.string().parse(uploadHeaders['x-omnara-csrf']),
  }
  for (const { body, text } of [
    { body: Buffer.from('Remote text'), text: 'Remote text' },
    { body: Buffer.from([0, 255]), text: null },
    { body: Buffer.from('Updated text'), text: 'Updated text' },
  ]) {
    const latest = await page.request.get(fileURL.toString())
    const updateURL = new URL(fileURL)
    updateURL.searchParams.set(
      'expected_digest',
      z.string().parse(latest.headers()['x-omnara-file-digest']),
    )
    const updated = await page.request.put(updateURL.toString(), { headers, data: body })
    expect(updated.ok()).toBe(true)
    await page.evaluate(() => window.dispatchEvent(new Event('visibilitychange')))
    const expectedText = text ?? 'Preview isn’t available for this file.'
    await expect(page.getByText(expectedText, { exact: false })).toBeVisible()
    await expect(page.getByRole('button', { name: 'Load latest', exact: true })).toBeHidden()
  }
  expect(failures).toEqual([])
})
