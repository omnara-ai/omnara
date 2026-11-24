import { schemas } from '@omnara/sdk'
import { type Cookie, expect, type Page } from '@playwright/test'

import { requiredEnvironmentVariable } from './fixtures'

export { installFailureTracking, requiredEnvironmentVariable } from './fixtures'

const password = requiredEnvironmentVariable('OMNARA_WEB_E2E_PASSWORD')
const providerConfig = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROVIDER_CONFIG')
const modelName = requiredEnvironmentVariable('OMNARA_WEB_E2E_MODEL_NAME')
const projectID = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROJECT_ID')
const adminEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_ADMIN_EMAIL')

const sessionCookies = new Map<string, Cookie[]>()

export async function typeInConfigEditor(page: Page, text: string) {
  const editor = page.getByRole('textbox', { name: 'Config (YAML)' })
  await expect(editor).toBeVisible()
  await editor.focus()
  await page.keyboard.insertText(text)
}

export async function replaceConfigEditor(page: Page, text: string) {
  const editor = page.getByRole('textbox', { name: 'Config (YAML)' })
  await expect(editor).toBeVisible()
  await editor.focus()
  // Desktop Chrome uses a Windows user agent, which selects Monaco's Ctrl
  // bindings even on macOS. Paste avoids typing auto-closing extra JSON quotes.
  await page.keyboard.press('Control+A')
  await editor.evaluate((element, value) => {
    const clipboardData = new DataTransfer()
    clipboardData.setData('text/plain', value)
    element.dispatchEvent(
      new ClipboardEvent('paste', { clipboardData, bubbles: true, cancelable: true }),
    )
  }, text)
}

export async function signInThroughLoginForm(page: Page, email: string, returnTo: string) {
  await page.goto(returnTo)

  await expect(page).toHaveURL((url) => {
    return url.pathname === '/login' && url.searchParams.get('return_to') === returnTo
  })
  await expect(page.getByRole('heading', { name: 'Sign in to Omnara' })).toBeVisible()

  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()

  await expect(page).toHaveURL(returnTo)
  sessionCookies.set(email, await page.context().cookies())
}

export async function signIn(page: Page, email: string, returnTo: string) {
  const cookies = sessionCookies.get(email)
  if (cookies) {
    await page.context().addCookies(cookies)
    await page.goto(returnTo)
    await expect(page).toHaveURL(returnTo)
    return
  }
  await signInThroughLoginForm(page, email, returnTo)
}

export async function selectConfiguredModel(page: Page) {
  const modelPicker = page.getByRole('combobox', { name: 'Model', exact: true })
  await modelPicker.click()
  await page.getByPlaceholder('Search shared models…').fill(modelName)
  await page
    .getByRole('option')
    .filter({ hasText: modelName })
    .filter({ hasText: providerConfig })
    .click()
}

export async function createProfile(page: Page, name: string, instruction: string) {
  await signIn(page, adminEmail, `/projects/${projectID}/agents/new`)
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(name)
  await page.getByLabel('Instruction').fill(instruction)
  await selectConfiguredModel(page)
  await expect(page.getByRole('button', { name: 'Create profile' })).toBeEnabled()
  const created = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname.endsWith('/agent-profiles'),
  )
  await page.getByRole('button', { name: 'Create profile' }).click()
  await expect(page).toHaveURL(
    new RegExp(`/projects/${projectID}/agent-profiles/aprf_[a-z2-7]+/configuration$`),
  )
  return schemas.zAgentProfile.parse(await (await created).json())
}
