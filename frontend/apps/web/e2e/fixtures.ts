import { generateKeyPairSync } from 'node:crypto'

import { type Integration, type IntegrationKind, schemas, zJsonText } from '@omnara/sdk'
import { expect, type Page, type Request, type Response } from '@playwright/test'
import { z } from 'zod'

export async function mockSlackSetupReturn(
  page: Page,
  projectPath: string,
  draft: Integration,
  flowID: string,
) {
  let integration: Integration = { ...draft }
  await page.route(`**${projectPath}/integrations/${draft.id}`, async (route) => {
    if (route.request().method() === 'PUT') {
      const update = schemas.zUpdateIntegrationRequest.parse(route.request().postDataJSON())
      integration = {
        ...integration,
        settings: update.settings,
        updated_at: new Date().toISOString(),
      }
    } else if (route.request().method() !== 'GET') return route.continue()
    await route.fulfill({ json: integration })
  })
  return {
    complete: () => {
      integration = {
        ...integration,
        state: 'active',
        setup_revision: integration.setup_revision + 1,
        last_oauth_flow_id: flowID,
        provider_tenant_id: 'T123',
        provider_account_ref: 'A123',
        provider_agent_display_name: 'Browser Slack bot',
      }
    },
  }
}

export function requiredEnvironmentVariable(name: string): string {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required. Run \`make web-e2e\` from the repository root.`)
  return value
}

interface FailureTrackingPage {
  on(event: 'pageerror', listener: (error: Error) => void): void
  on(event: 'requestfailed', listener: (request: Request) => void): void
  on(event: 'response', listener: (response: Response) => void): void
}

export function installFailureTracking(page: FailureTrackingPage, ignore: RegExp[] = []) {
  const failures: string[] = []
  const responses = new WeakMap<Request, number>()
  const record = (failure: string) => {
    if (!ignore.some((pattern) => pattern.test(failure))) failures.push(failure)
  }

  page.on('pageerror', (error) => {
    record(`page: ${error.message}`)
  })
  page.on('requestfailed', (request) => {
    const method = request.method()
    if (method === 'POST' && new URL(request.url()).pathname === '/api/auth/login') return
    const error = request.failure()?.errorText ?? 'failed'
    if (method === 'GET' && error === 'net::ERR_ABORTED') return
    // Chromium can abort the empty 204 DELETE stream through the TLS proxy.
    if (method === 'DELETE' && error === 'net::ERR_ABORTED' && responses.get(request) === 204)
      return
    const failure = `request: ${method} ${request.url()} (${error})`
    if (
      error === 'net::ERR_ABORTED' &&
      method === 'POST' &&
      /^\/api\/v1\/orgs\/[^/]+\/projects\/[^/]+\/agent-configs\/tools$/.test(
        new URL(request.url()).pathname,
      )
    )
      record(failure)
    else failures.push(failure)
  })
  page.on('response', (response) => {
    responses.set(response.request(), response.status())
    const url = new URL(response.url())
    if (response.status() === 401 && url.pathname === '/api/v1/me') return
    if (response.status() >= 400) {
      record(`response: ${response.status()} ${url.pathname}`)
    }
  })

  return failures
}

export function installIntegrationFailureTracking(page: FailureTrackingPage) {
  return installFailureTracking(page, [
    /^page: Canceled$/,
    /^request: POST .*\/agent-configs\/tools \(net::ERR_ABORTED\)$/,
  ])
}

export async function openIntegrationSetup(
  page: Page,
  projectID: string,
  integrationKind: IntegrationKind,
  name: string,
) {
  const label =
    integrationKind === 'github_pr'
      ? 'GitHub PR review'
      : integrationKind === 'discord_thread'
        ? 'Discord bot'
        : 'Slack bot'
  const integrationsPath = `/projects/${projectID}/integrations`
  if (new URL(page.url()).pathname !== integrationsPath) await page.goto(integrationsPath)
  const add = page.getByRole('link', { name: 'Add integration', exact: true })
  const choice = page.getByRole('link', { name: `Set up ${label}`, exact: false })
  await expect(add.or(choice)).toBeVisible()
  if (await add.isVisible()) await add.click()
  await choice.click()
  await expect(page).toHaveURL(`/projects/${projectID}/integrations/new/${integrationKind}`)
  if (integrationKind === 'github_pr')
    await page.getByRole('button', { name: 'Enter App details', exact: true }).click()
  await page.getByLabel('Integration name', { exact: true }).fill(name)
  await expect(
    page.getByLabel(
      integrationKind === 'slack_thread'
        ? 'App configuration token'
        : integrationKind === 'github_pr'
          ? 'GitHub App ID'
          : 'Discord Application ID',
      { exact: true },
    ),
  ).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
}

export function integrationCreation(page: Page) {
  return page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname.endsWith('/integrations'),
  )
}

export async function readIntegration(page: Page, apiProjectPath: string, integrationID: string) {
  const body = await page.evaluate(async (path) => {
    const response = await fetch(path)
    if (!response.ok) throw new Error(`Read integration failed: ${response.status}`)
    return response.text()
  }, `${apiProjectPath}/integrations/${integrationID}`)
  return zJsonText.pipe(schemas.zIntegration).parse(body)
}

export async function mockVerifiedIntegrationSetup(
  page: Page,
  integrationKind: Extract<IntegrationKind, 'github_pr' | 'discord_thread'>,
) {
  await page.route('**/integrations/*/setup', async (route) => {
    if (route.request().method() !== 'POST') return route.continue()
    const request = schemas.zConfigureIntegrationRequest.parse(route.request().postDataJSON())
    const integrationID = schemas.zIntegrationId.parse(
      new URL(route.request().url()).pathname.split('/').at(-2),
    )
    expect(request.credential_secret_id).toMatch(/^sec_[a-z2-7]{26}$/)
    const seeded = await page.request.post(
      `${requiredEnvironmentVariable('OMNARA_WEB_E2E_PROVIDER_FIXTURE')}/integrations/${integrationID}/setup`,
      { data: request },
    )
    expect(seeded.status()).toBe(200)
    expect(z.object({ id: schemas.zIntegrationId }).parse(await seeded.json()).id).toBe(
      integrationID,
    )
    const apiProjectPath = route
      .request()
      .url()
      .slice(0, route.request().url().lastIndexOf('/integrations/'))
    const integration = await readIntegration(page, apiProjectPath, integrationID)
    expect(integration.integration_kind).toBe(integrationKind)
    expect(integration.credential_secret_id).toBe(request.credential_secret_id)
    expect(integration.setup_revision).toBe(request.expected_setup_revision + 1)
    await route.fulfill({ status: 200, json: integration })
  })
}

export async function fillProviderAccount(
  page: Page,
  integrationKind: Extract<IntegrationKind, 'github_pr' | 'discord_thread'>,
) {
  await page
    .getByLabel(integrationKind === 'github_pr' ? 'GitHub App ID' : 'Discord Application ID', {
      exact: true,
    })
    .fill('111')
  if (integrationKind === 'github_pr')
    await page.getByLabel('Installation ID', { exact: true }).fill('222')
}

export async function connectIntegrationWithCredentialRetry(
  page: Page,
  projectID: string,
  integrationKind: Extract<IntegrationKind, 'github_pr' | 'discord_thread'>,
  name: string,
  failures: string[],
) {
  await openIntegrationSetup(page, projectID, integrationKind, name)
  await mockVerifiedIntegrationSetup(page, integrationKind)
  let credentialCreates = 0
  const trackCredential = (request: Request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/secrets'))
      credentialCreates++
  }
  page.on('request', trackCredential)
  await fillProviderAccount(page, integrationKind)
  if (integrationKind === 'github_pr') {
    const { privateKey } = generateKeyPairSync('rsa', {
      modulusLength: 2048,
      privateKeyEncoding: { type: 'pkcs8', format: 'pem' },
      publicKeyEncoding: { type: 'spki', format: 'pem' },
    })
    await page.getByLabel('RSA private key (PEM)').fill(privateKey)
    await page.getByLabel('Webhook secret').fill('local-github-webhook-secret')
  } else {
    await page.getByLabel('Bot token', { exact: true }).fill('local-discord-token')
    await page.getByLabel('Public key', { exact: true }).fill('ab'.repeat(32))
    await expect(page.getByLabel('Interactions Endpoint URL', { exact: true })).toHaveCount(0)
  }
  await page.route(
    '**/integrations/*/setup',
    (route) =>
      route.fulfill({
        status: 409,
        json: { code: 'conflict', error: 'Verification failed; try again' },
      }),
    { times: 1 },
  )
  const setupResponse = () =>
    page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname.endsWith('/setup'),
    )
  const created = integrationCreation(page)
  const failedSetup = setupResponse()
  await page.getByRole('button', { name: 'Create and connect', exact: true }).click()
  const creation = await created
  expect(creation.status()).toBe(201)
  expect(creation.request().postDataJSON()).toEqual({
    name,
    integration_kind: integrationKind,
    settings: {},
  })
  const draft = schemas.zIntegration.parse(await creation.json())
  const apiProjectPath = creation.url().replace(/\/integrations$/, '')
  const failed = await failedSetup
  expect(failed.status()).toBe(409)
  await expect(page.getByRole('alert')).toContainText('Verification failed; try again')
  await expect(page.getByText('Credentials saved. Retry reuses the saved secret.')).toBeVisible()
  await expect(page).toHaveURL(`/projects/${projectID}/integrations/new/${integrationKind}`)
  expect(failures.splice(0)).toEqual([`response: 409 ${new URL(failed.url()).pathname}`])
  const configured = setupResponse()
  await page.getByRole('button', { name: 'Create and connect', exact: true }).click()
  expect((await configured).status()).toBe(200)
  await expect(page).toHaveURL(`/projects/${projectID}/integrations/new/${integrationKind}`)
  await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  const integration = await readIntegration(page, apiProjectPath, draft.id)
  expect(integration).toMatchObject({
    id: draft.id,
    name: draft.name,
    integration_kind: integrationKind,
    provider_tenant_id: '111',
    provider_account_ref: '222',
    state: 'active',
  })
  const attempt = schemas.zConfigureIntegrationRequest.parse(failed.request().postDataJSON())
  if (integrationKind === 'discord_thread')
    expect(attempt.provider_config).toMatchObject({ public_key: 'ab'.repeat(32) })
  expect(attempt.credential_secret_id).toBe(integration.credential_secret_id)
  expect(credentialCreates).toBe(1)
  page.off('request', trackCredential)
  expect(JSON.stringify(integration)).not.toContain(
    integrationKind === 'github_pr' ? 'PRIVATE KEY' : 'local-discord-token',
  )
  return { integration, apiProjectPath }
}

export function expectSlackAuthorization(oauthURL: string, browserOrigin: string) {
  const oauth = new URL(oauthURL)
  expect(oauth.hostname).toBe('slack.com')
  expect(oauth.pathname).toBe('/oauth/v2/authorize')
  expect(oauth.searchParams.get('client_id')).toBe('local-slack-client')
  expect(oauth.searchParams.get('state')).toBeTruthy()
  expect(oauth.searchParams.get('redirect_uri')).toBe(
    `${browserOrigin}/api/integrations/oauth/callback`,
  )
}

/** Switches the integration page to one of its settings tabs. */
export async function openIntegrationTab(
  page: Page,
  name: 'Mentions' | 'Pull requests' | 'Schedules' | 'Conversations' | 'Advanced',
) {
  await page
    .getByRole('group', { name: 'Integration settings', exact: true })
    .getByRole('button', { name, exact: true })
    .click()
}

export async function expectIntegrationCapabilities(page: Page, integration: Integration) {
  await openIntegrationTab(page, 'Advanced')
  const capabilities = page.getByRole('region', { name: 'Advanced', exact: true })
  await expect(
    capabilities.getByRole('link', { name: 'Tools added to the agent profile', exact: true }),
  ).toHaveAttribute('href', /^https:\/\/docs\.omnara\.com\/integrations\//)
  expect(integration.capabilities.subscription).toBeDefined()
  if (integration.integration_kind !== 'github_pr')
    await expect(
      capabilities.getByText(/Listed under/).getByText(integration.name, { exact: true }),
    ).toBeVisible()
}

export async function expectInteractionToolMenu(page: Page) {
  for (const name of ['list_interaction_handlers', 'set_interaction_handler']) {
    await page.getByRole('button', { name: 'Add tools', exact: true }).click()
    await expect(page.getByRole('menuitem', { name: 'ask_question', exact: true })).toBeVisible()
    await page.getByRole('menuitem', { name, exact: true }).click()
    const permission = page.getByRole('radiogroup', { name: `${name} permission`, exact: true })
    await expect(permission.getByRole('radio', { name: 'Always allow', exact: true })).toBeChecked()
    await permission.getByRole('radio', { name: 'Always ask', exact: true }).click()
    await expect(permission.getByRole('radio', { name: 'Always ask', exact: true })).toBeChecked()
  }
}
