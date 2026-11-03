import { type Cookie, expect, type Page } from '@playwright/test'

import { requiredEnvironmentVariable } from './fixtures'

export { installFailureTracking, requiredEnvironmentVariable } from './fixtures'

const password = requiredEnvironmentVariable('OMNARA_WEB_E2E_PASSWORD')

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
