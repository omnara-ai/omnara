import { type Cookie, expect, type Page } from '@playwright/test'

export function requiredEnvironmentVariable(name: string): string {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required. Run \`make web-e2e\` from the repository root.`)
  return value
}

const password = requiredEnvironmentVariable('OMNARA_WEB_E2E_PASSWORD')

export function installFailureTracking(page: Page, ignore: RegExp[] = []) {
  const failures: string[] = []
  const record = (failure: string) => {
    if (!ignore.some((pattern) => pattern.test(failure))) failures.push(failure)
  }

  page.on('pageerror', (error) => {
    record(`page: ${error.message}`)
  })
  page.on('requestfailed', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/auth/login') return
    record(`request: ${request.url()} (${request.failure()?.errorText ?? 'failed'})`)
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (response.status() === 401 && url.pathname === '/api/v1/me') return
    if (response.status() >= 400) {
      record(`response: ${response.status()} ${url.pathname}`)
    }
  })

  return failures
}

const sessionCookies = new Map<string, Cookie[]>()

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
