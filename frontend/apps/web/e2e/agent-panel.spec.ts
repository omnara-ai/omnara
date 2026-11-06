import { schemas, zJsonText } from '@omnara/sdk'
import { expect, type Locator, type Page, type Request, test } from '@playwright/test'

import {
  expectClearOfAssistantLauncher,
  installFailureTracking,
  requiredEnvironmentVariable,
  signIn,
} from './helpers'

const desktop = { width: 1280, height: 720 }
const mobile = { width: 390, height: 844 }

async function box(locator: Locator) {
  const rect = await locator.boundingBox()
  if (!rect) throw new Error('Element is not rendered')
  return rect
}

/** The launcher floats in the corner without shrinking the app or covering the page's action. */
async function expectFloatingLauncher(page: Page, action: Locator) {
  for (const viewport of [desktop, mobile]) {
    await page.setViewportSize(viewport)
    await expectClearOfAssistantLauncher(page, action)
    const shell = await box(page.locator('[data-slot="sidebar-inset"]'))
    const launcher = await box(page.getByRole('button', { name: 'Assistant', exact: true }))
    expect(shell.height).toBe(viewport.height)
    expect(launcher.width).toBeLessThanOrEqual(48)
    expect(viewport.width - launcher.x - launcher.width).toBeLessThanOrEqual(24)
  }
  await page.setViewportSize(desktop)
}

test('creates a personal assistant and keeps its conversation across organizations and account pages', async ({
  page,
}, testInfo) => {
  const failures = installFailureTracking(page)
  const streams = new Set<Request>()
  page.on('request', (request) => {
    if (request.url().includes('/events/stream')) streams.add(request)
  })
  page.on('requestfailed', (request) => streams.delete(request))
  page.on('requestfinished', (request) => streams.delete(request))
  const email = requiredEnvironmentVariable('OMNARA_WEB_E2E_PANEL_EMAIL')
  const orgName = requiredEnvironmentVariable('OMNARA_WEB_E2E_ORG_NAME')
  const otherOrg = requiredEnvironmentVariable('OMNARA_WEB_E2E_SWITCH_ORG_NAME')
  const projectID = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROJECT_ID')
  await signIn(page, email, `/projects/${projectID}/agents`)
  await page.getByRole('button', { name: 'Assistant', exact: true }).click()
  const panel = page.getByRole('dialog', { name: 'Assistant', exact: true })
  await panel.getByRole('button', { name: 'Create new', exact: true, pressed: false }).click()
  await panel.getByRole('combobox', { name: 'Project', exact: true }).click()
  await page.getByRole('option', { name: 'Default', exact: true }).click()
  const creation = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' && response.url().endsWith('/assistant-agents'),
  )
  await panel.getByRole('button', { name: 'Create assistant', exact: true }).click()
  const created = schemas.zCreateAssistantAgentResponse.parse(await (await creation).json())
  await expect(panel.getByText('Omnara assistant', { exact: true })).toBeVisible()
  const composer = panel.getByRole('textbox')
  await expect(composer).toBeEnabled()
  await composer.fill('Keep this draft')
  await panel.getByRole('button', { name: 'Close assistant' }).click()
  await page.getByRole('button', { name: orgName }).click()
  await page.getByRole('menuitem', { name: otherOrg, exact: true }).click()
  await page.getByRole('button', { name: 'Assistant', exact: true }).click()
  await expect(composer).toHaveValue('Keep this draft')
  await expect(panel.getByText(`${orgName} / Default`, { exact: true })).toBeVisible()
  const sent = page.waitForRequest(
    (request) =>
      request.method() === 'POST' && request.url().endsWith(`/agents/${created.agent_id}/inputs`),
  )
  await composer.press('Enter')
  const input = zJsonText.pipe(schemas.zCreateAgentInputRequest).parse((await sent).postData())
  const context = input.content_blocks.find(
    (block) => block.type === 'text' && block.metadata?.omnara_hidden === 'true',
  )
  expect(context?.type).toBe('text')
  if (context?.type !== 'text') throw new Error('Missing page context')
  expect(context.text).toContain(otherOrg)
  expect(context.text).toContain(created.agent_id)
  await expect(panel).not.toContainText('Dashboard page context')
  await composer.fill('Account page draft')
  await page.getByRole('button', { name: new RegExp(email) }).click()
  await page.getByRole('menuitem', { name: 'Account', exact: true }).click()
  await expect(page).toHaveURL('/user/account')
  await expect(composer).toHaveValue('Account page draft')
  await expect.poll(() => streams.size).toBe(1)
  await page.getByRole('button', { name: 'Turn off assistant panel', exact: true }).click()
  await expect.poll(() => streams.size).toBe(0)
  await expect(panel).toBeHidden()
  await expect(page.getByRole('button', { name: 'Assistant', exact: true })).toHaveCount(0)
  await page.getByRole('button', { name: 'Turn on assistant panel', exact: true }).click()
  await page.reload()
  await expect(
    page.getByRole('button', { name: 'Turn off assistant panel', exact: true }),
  ).toBeVisible()
  expect(streams.size).toBe(0)
  await page.getByRole('button', { name: 'Assistant', exact: true }).click()
  await expect(panel.getByText('Omnara assistant', { exact: true })).toBeVisible()
  await expect(panel.getByText('Keep this draft', { exact: true })).toBeVisible()
  await page.screenshot({ path: testInfo.outputPath('assistant-desktop.png') })
  await page.setViewportSize(mobile)
  await expect(panel).toBeInViewport()
  await expect(composer).toBeInViewport()
  await page.screenshot({ path: testInfo.outputPath('assistant-mobile.png') })
  await page.setViewportSize(desktop)
  await panel.getByRole('button', { name: 'Open agent', exact: true }).click()
  await expect(page).toHaveURL(`/projects/${projectID}/agents/${created.agent_id}/chat`)
  await expect(page.getByRole('button', { name: orgName })).toBeVisible()
  await expectFloatingLauncher(page, page.getByRole('button', { name: 'Send message' }))
  await page.goto(`/projects/${projectID}/agents/new`)
  await expectFloatingLauncher(page, page.getByRole('button', { name: 'Create & launch agent' }))
  // A short viewport puts the last action of a scrolling page or pane at its scroll end.
  await page.setViewportSize({ width: desktop.width, height: 360 })
  await page.goto(`/projects/${projectID}/agents/${created.agent_id}/events`)
  const addSchedule = page.getByRole('button', { name: 'Add', exact: true })
  await expectClearOfAssistantLauncher(page, addSchedule, [768, desktop.width])
  await page.setViewportSize({ width: desktop.width, height: 200 })
  await page.goto('/user/api-tokens')
  const lastTokenActions = page.getByRole('button', { name: /^Actions for / }).last()
  await expectClearOfAssistantLauncher(page, lastTokenActions, [mobile.width, 768, desktop.width])
  await page.setViewportSize(desktop)
  const consentPanel = page.waitForResponse((response) =>
    response.url().endsWith('/me/agent-panel'),
  )
  await page.goto('/device')
  await consentPanel
  await expect(page.getByRole('heading', { name: 'Approve device login' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Assistant', exact: true })).toHaveCount(0)
  expect(failures).toEqual([])
})
