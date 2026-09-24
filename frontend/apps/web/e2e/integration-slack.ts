import { schemas } from '@omnara/sdk'
import { type BrowserContext, expect, type Page } from '@playwright/test'

import {
  expectIntegrationCapabilities,
  expectSlackAuthorization,
  installIntegrationFailureTracking,
  integrationCreation,
  mockSlackSetupReturn,
  openIntegrationSetup,
} from './fixtures'

export async function exerciseSlackIntegrationSetup(
  page: Page,
  context: BrowserContext,
  projectID: string,
  integrationName: string,
) {
  const failures = installIntegrationFailureTracking(page)
  await openIntegrationSetup(page, projectID, 'slack_thread', integrationName)
  const browserOrigin = new URL(page.url()).origin
  await page.getByLabel('App configuration token', { exact: true }).fill('local-config-token')
  const connect = page.getByRole('button', { name: 'Create and connect', exact: true })
  await expect(connect).toBeEnabled()
  for (const height of [256, 512]) {
    const image = await page.evaluate((height) => {
      const canvas = document.createElement('canvas')
      canvas.width = 512
      canvas.height = height
      return canvas.toDataURL('image/png').slice('data:image/png;base64,'.length)
    }, height)
    await page.locator('#slack-app-icon').setInputFiles({
      name: 'icon.png',
      mimeType: 'image/png',
      buffer: Buffer.from(image, 'base64'),
    })
    if (height === 256) {
      await expect(page.getByRole('alert')).toContainText('App icon must be square')
      await expect(page.getByRole('button', { name: 'Remove', exact: true })).toHaveCount(0)
      await expect(page.getByText('icon.png', { exact: true })).toHaveCount(0)
      await expect(connect).toBeEnabled()
    } else {
      await expect(page.getByText('icon.png', { exact: true })).toBeVisible()
      await expect(page.getByRole('alert')).toHaveCount(0)
      await expect(connect).toBeEnabled()
      await page.getByRole('button', { name: 'Remove', exact: true }).click()
      await expect(page.getByText('icon.png', { exact: true })).toHaveCount(0)
    }
  }
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
  const created = integrationCreation(page)
  await page.getByRole('button', { name: 'Create and connect', exact: true }).click()
  const creation = await created
  expect(creation.status()).toBe(201)
  const integration = schemas.zProjectIntegration.parse(await creation.json())
  const apiProjectPath = creation.url().replace(/\/integrations$/, '')
  const integrationPath = `/projects/${projectID}/integrations/${integration.id}`
  const response = await pending
  expect(response.status()).toBe(201)
  const setup = schemas.zIntegrationOAuthSetup.parse(await response.json())
  expect(setup.integration_id).toBe(integration.id)
  expect(response.request().postDataJSON()).toMatchObject({
    client_id: 'local-slack-client',
    return_to: integrationPath,
  })
  expect(response.request().postDataJSON()).not.toHaveProperty('provider')
  expectSlackAuthorization(setup.oauth_url, browserOrigin)
  const popup = page.waitForEvent('popup')
  await page.getByRole('link', { name: 'Authorize in Slack' }).click()
  const authorization = await popup
  await expect(authorization.getByText('Provider authorization boundary')).toBeVisible()
  await authorization.close()
  await expect(page).toHaveURL(`/projects/${projectID}/integrations/new/slack_thread`)

  const projectPath = new URL(apiProjectPath).pathname
  const fixture = await mockSlackSetupReturn(page, projectPath, integration, setup.flow_id)
  const waiting = await page.waitForResponse(
    (result) =>
      result.request().method() === 'GET' &&
      new URL(result.url()).pathname === `${projectPath}/integrations/${integration.id}`,
  )
  const pendingIntegration = schemas.zProjectIntegration.parse(await waiting.json())
  expect(pendingIntegration.state).toBe('disconnected')
  expect(pendingIntegration.setup_revision).toBe(integration.setup_revision)
  expect(pendingIntegration.last_oauth_flow_id).not.toBe(setup.flow_id)
  await expect(page.getByRole('link', { name: 'Authorize in Slack' })).toBeVisible()
  fixture.complete()
  const mentions = page.getByRole('region', { name: 'Mentions', exact: true })
  await expect(
    mentions.getByRole('combobox', { name: 'Profiles for mentions', exact: true }),
  ).toBeVisible()
  await expect(page).toHaveURL(`/projects/${projectID}/integrations/new/slack_thread`)
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(mentions.getByLabel('Integration name', { exact: true })).toHaveCount(0)
  await expect(mentions.getByRole('checkbox')).toHaveCount(0)
  await expect(mentions).toContainText('0/16 selected')
  const savedSettings = page.waitForResponse(
    (result) =>
      result.request().method() === 'PUT' &&
      new URL(result.url()).pathname === `${projectPath}/integrations/${integration.id}`,
  )
  await mentions.getByRole('button', { name: 'Save changes', exact: true }).click()
  const saved = await savedSettings
  expect(saved.status()).toBe(200)
  const connected = schemas.zProjectIntegration.parse(await saved.json())
  expect(connected).toMatchObject({
    id: integration.id,
    state: 'active',
    last_oauth_flow_id: setup.flow_id,
  })
  expect(connected.settings.launcher).toBeUndefined()
  await expect(mentions.getByRole('button', { name: 'Choose profiles', exact: true })).toBeVisible()
  await expectIntegrationCapabilities(page, connected)
  await expect(page.getByRole('heading', { name: integration.name, exact: true })).toBeVisible()
  await page.goto(
    `${browserOrigin}${integrationPath}?integration_oauth=success&integration_id=${integration.id}`,
  )
  await expect(page.getByRole('status').filter({ hasText: 'Account connected.' })).toBeVisible()
  await expect(mentions.getByRole('button', { name: 'Save changes', exact: true })).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page).toHaveURL(integrationPath)
  await expect(page.getByRole('heading', { name: integration.name, exact: true })).toBeVisible()
  expect(failures).toEqual([])
}
