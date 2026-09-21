import { schemas } from '@omnara/sdk'
import { type BrowserContext, expect, type Page } from '@playwright/test'

import {
  appCreation,
  expectAppCapabilities,
  expectSlackAuthorization,
  installAppFailureTracking,
  mockSlackSetupReturn,
  openAppSetup,
} from './fixtures'

export async function exerciseSlackAppSetup(
  page: Page,
  context: BrowserContext,
  projectID: string,
  appName: string,
) {
  const failures = installAppFailureTracking(page)
  await openAppSetup(page, projectID, 'slack_thread', appName)
  const browserOrigin = new URL(page.url()).origin
  await page.getByLabel('Use an existing Slack app').check()
  await page.getByLabel('Client ID', { exact: true }).fill('local-slack-client')
  await page.getByLabel('Client secret', { exact: true }).fill('local-slack-secret')
  await page.getByLabel('Signing secret', { exact: true }).fill('local-slack-signing')
  await context.route('https://slack.com/**', async (route) => {
    await route.fulfill({
      contentType: 'text/html',
      body: '<p>Provider authorization boundary</p>',
    })
  })
  const pending = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith('/oauth/setup') &&
      response.request().method() === 'POST',
  )
  const created = appCreation(page)
  await page.getByRole('button', { name: 'Create and connect Slack', exact: true }).click()
  const creation = await created
  expect(creation.status()).toBe(201)
  const app = schemas.zProjectApp.parse(await creation.json())
  const apiProjectPath = creation.url().replace(/\/apps$/, '')
  const appPath = `/projects/${projectID}/apps/${app.id}`
  const response = await pending
  expect(response.status()).toBe(201)
  const setup = schemas.zIntegrationOAuthSetup.parse(await response.json())
  expect(setup.app_id).toBe(app.id)
  expect(response.request().postDataJSON()).toMatchObject({
    client_id: 'local-slack-client',
    return_to: appPath,
  })
  expect(response.request().postDataJSON()).not.toHaveProperty('provider')
  expectSlackAuthorization(setup.oauth_url, browserOrigin)
  const popup = page.waitForEvent('popup')
  await page.getByRole('link', { name: 'Authorize in Slack' }).click()
  const authorization = await popup
  await expect(authorization.getByText('Provider authorization boundary')).toBeVisible()
  await authorization.close()
  await expect(page).toHaveURL(`/projects/${projectID}/apps/new/slack_thread`)

  // Callback persistence is covered against a local Slack server in Go tests.
  // This fixture exercises app polling, exact-flow matching and callback routing.
  const projectPath = new URL(apiProjectPath).pathname
  const fixture = await mockSlackSetupReturn(page, projectPath, app, setup.flow_id)
  const waiting = await page.waitForResponse(
    (result) =>
      result.request().method() === 'GET' &&
      new URL(result.url()).pathname === `${projectPath}/apps/${app.id}`,
  )
  const pendingApp = schemas.zProjectApp.parse(await waiting.json())
  expect(pendingApp.state).toBe('disconnected')
  expect(pendingApp.setup_revision).toBe(app.setup_revision)
  expect(pendingApp.last_oauth_flow_id).not.toBe(setup.flow_id)
  await expect(page.getByRole('link', { name: 'Authorize in Slack' })).toBeVisible()
  fixture.complete()
  const mentions = page.getByRole('region', { name: 'Mentions', exact: true })
  await expect(
    mentions.getByRole('combobox', { name: 'Offered profiles', exact: true }),
  ).toBeVisible()
  await expect(page).toHaveURL(`/projects/${projectID}/apps/new/slack_thread`)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(mentions.getByLabel('App name', { exact: true })).toHaveCount(0)
  await expect(mentions.getByRole('checkbox')).toHaveCount(0)
  await expect(mentions).toContainText('0/16 selected')
  const savedSettings = page.waitForResponse(
    (result) =>
      result.request().method() === 'PUT' &&
      new URL(result.url()).pathname === `${projectPath}/apps/${app.id}`,
  )
  await mentions.getByRole('button', { name: 'Save changes', exact: true }).click()
  const saved = await savedSettings
  expect(saved.status()).toBe(200)
  const connected = schemas.zProjectApp.parse(await saved.json())
  expect(connected).toMatchObject({
    id: app.id,
    state: 'active',
    last_oauth_flow_id: setup.flow_id,
  })
  expect(connected.settings.launcher).toBeUndefined()
  await expect(mentions.getByRole('button', { name: 'Choose profiles', exact: true })).toBeVisible()
  await expectAppCapabilities(page, connected)
  await expect(page.getByRole('heading', { name: app.name, exact: true })).toBeVisible()
  await page.goto(`${browserOrigin}${appPath}?integration_oauth=success&app_id=${app.id}`)
  await expect(page.getByRole('status').filter({ hasText: 'Account connected.' })).toBeVisible()
  await expect(mentions.getByRole('button', { name: 'Save changes', exact: true })).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page).toHaveURL(appPath)
  await expect(page.getByRole('heading', { name: app.name, exact: true })).toBeVisible()
  expect(failures).toEqual([])
}
