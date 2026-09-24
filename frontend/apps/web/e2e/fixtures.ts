import { generateKeyPairSync } from 'node:crypto'

import { type IntegrationType, type ProjectIntegration, schemas, zJsonText } from '@omnara/sdk'
import { expect, type Page, type Request, type Response } from '@playwright/test'
import { z } from 'zod'

export async function mockSlackSetupReturn(
  page: Page,
  projectPath: string,
  draft: ProjectIntegration,
  flowID: string,
) {
  let integration: ProjectIntegration = { ...draft }
  await page.route(`**${projectPath}/integrations/${draft.id}`, async (route) => {
    if (route.request().method() === 'PUT') {
      const metadata = schemas.zSaveProjectIntegrationRequest.parse(route.request().postDataJSON())
      if (
        metadata.name !== integration.name ||
        metadata.integration_type !== integration.integration_type
      )
        throw new Error('The browser fixture cannot change integration identity')
      integration = { ...integration, ...metadata, updated_at: new Date().toISOString() }
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
    // Chromium can abort the empty 204 DELETE stream through the TLS proxy.
    if (method === 'DELETE' && error === 'net::ERR_ABORTED' && responses.get(request) === 204)
      return
    const failure = `request: ${method} ${request.url()} (${error})`
    if (
      error === 'net::ERR_ABORTED' &&
      (method === 'GET' ||
        (method === 'POST' &&
          /^\/api\/v1\/orgs\/[^/]+\/projects\/[^/]+\/agent-configs\/tools$/.test(
            new URL(request.url()).pathname,
          )))
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
  const origin = new URL(requiredEnvironmentVariable('OMNARA_WEB_E2E_BASE_URL')).origin.replace(
    /[.*+?^${}()|[\]\\]/g,
    '\\$&',
  )
  return installFailureTracking(page, [
    /^page: Canceled$/,
    /^request: GET .*\/agent-profiles\/aprf_[a-z2-7]+(?:\/config)? \(net::ERR_ABORTED\)$/,
    /^request: GET .*\/integrations\/itg_[a-z2-7]+ \(net::ERR_ABORTED\)$/,
    /^request: GET .*\/cron-triggers\/cron_[a-z2-7]+ \(net::ERR_ABORTED\)$/,
    /^request: GET .*\/integrations\/itg_[a-z2-7]+\/subscriptions(?:\?.*)? \(net::ERR_ABORTED\)$/,
    /^request: GET .*\/cron-triggers\?.* \(net::ERR_ABORTED\)$/,
    /^request: POST .*\/agent-configs\/tools \(net::ERR_ABORTED\)$/,
    // Full-document navigation also cancels intent-preloaded route chunks.
    /^request: GET .*\/assets\/[^/]+\.js \(net::ERR_ABORTED\)$/,
    // Navigation can also cancel a lazily loaded bundled font.
    new RegExp(String.raw`^request: GET ${origin}/assets/[^/?]+\.woff2 \(net::ERR_ABORTED\)$`),
  ])
}

export async function openIntegrationSetup(
  page: Page,
  projectID: string,
  integrationType: IntegrationType,
  name: string,
) {
  const label =
    integrationType === 'github_pr'
      ? 'GitHub PR review'
      : integrationType === 'discord_thread'
        ? 'Discord threads'
        : 'Slack threads'
  const integrationsPath = `/projects/${projectID}/integrations`
  if (new URL(page.url()).pathname !== integrationsPath) await page.goto(integrationsPath)
  const add = page.getByRole('link', { name: 'Add integration', exact: true })
  const choice = page.getByRole('link', { name: `Set up ${label}`, exact: false })
  await expect(add.or(choice)).toBeVisible()
  if (await add.isVisible()) await add.click()
  await choice.click()
  await expect(page).toHaveURL(`/projects/${projectID}/integrations/new/${integrationType}`)
  if (integrationType === 'github_pr')
    await page.getByRole('button', { name: 'Use an existing App', exact: true }).click()
  await page.getByLabel('Integration name', { exact: true }).fill(name)
  await expect(
    page.getByLabel(
      integrationType === 'slack_thread'
        ? 'App configuration token'
        : integrationType === 'github_pr'
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
  return zJsonText.pipe(schemas.zProjectIntegration).parse(body)
}

export async function mockVerifiedIntegrationSetup(
  page: Page,
  integrationType: Extract<IntegrationType, 'github_pr' | 'discord_thread'>,
) {
  await page.route('**/integrations/*/setup', async (route) => {
    if (route.request().method() !== 'POST') return route.continue()
    const request = schemas.zConfigureProjectIntegrationRequest.parse(
      route.request().postDataJSON(),
    )
    const integrationID = schemas.zProjectIntegrationId.parse(
      new URL(route.request().url()).pathname.split('/').at(-2),
    )
    expect(request.credential_secret_id).toMatch(/^sec_[a-z2-7]{26}$/)
    const seeded = await page.request.post(
      `${requiredEnvironmentVariable('OMNARA_WEB_E2E_PROVIDER_FIXTURE')}/integrations/${integrationID}/setup`,
      { data: request },
    )
    expect(seeded.status()).toBe(200)
    expect(z.object({ id: schemas.zProjectIntegrationId }).parse(await seeded.json()).id).toBe(
      integrationID,
    )
    const apiProjectPath = route
      .request()
      .url()
      .slice(0, route.request().url().lastIndexOf('/integrations/'))
    const integration = await readIntegration(page, apiProjectPath, integrationID)
    expect(integration.integration_type).toBe(integrationType)
    expect(integration.credential_secret_id).toBe(request.credential_secret_id)
    expect(integration.setup_revision).toBe(request.expected_setup_revision + 1)
    await route.fulfill({ status: 200, json: integration })
  })
}

export async function fillProviderAccount(
  page: Page,
  integrationType: Extract<IntegrationType, 'github_pr' | 'discord_thread'>,
) {
  await page
    .getByLabel(integrationType === 'github_pr' ? 'GitHub App ID' : 'Discord Application ID', {
      exact: true,
    })
    .fill('111')
  if (integrationType === 'github_pr')
    await page.getByLabel('Installation ID', { exact: true }).fill('222')
}

export async function connectIntegrationWithCredentialRetry(
  page: Page,
  projectID: string,
  integrationType: Extract<IntegrationType, 'github_pr' | 'discord_thread'>,
  name: string,
  failures: string[],
) {
  await openIntegrationSetup(page, projectID, integrationType, name)
  await mockVerifiedIntegrationSetup(page, integrationType)
  let credentialCreates = 0
  const trackCredential = (request: Request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname.endsWith('/secrets'))
      credentialCreates++
  }
  page.on('request', trackCredential)
  await fillProviderAccount(page, integrationType)
  if (integrationType === 'github_pr') {
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
    integration_type: integrationType,
    settings: {},
  })
  const draft = schemas.zProjectIntegration.parse(await creation.json())
  const apiProjectPath = creation.url().replace(/\/integrations$/, '')
  const failed = await failedSetup
  expect(failed.status()).toBe(409)
  await expect(page.getByRole('alert')).toContainText('Verification failed; try again')
  await expect(page.getByText('Credentials saved. Retry reuses the saved secret.')).toBeVisible()
  await expect(page).toHaveURL(`/projects/${projectID}/integrations/new/${integrationType}`)
  expect(failures.splice(0)).toEqual([`response: 409 ${new URL(failed.url()).pathname}`])
  const configured = setupResponse()
  await page.getByRole('button', { name: 'Create and connect', exact: true }).click()
  expect((await configured).status()).toBe(200)
  await expect(page).toHaveURL(`/projects/${projectID}/integrations/new/${integrationType}`)
  await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeVisible()
  await expect(page.getByRole('dialog')).toHaveCount(0)
  const integration = await readIntegration(page, apiProjectPath, draft.id)
  expect(integration).toMatchObject({
    id: draft.id,
    name: draft.name,
    integration_type: integrationType,
    provider_tenant_id: '111',
    provider_account_ref: '222',
    state: 'active',
  })
  const attempt = schemas.zConfigureProjectIntegrationRequest.parse(failed.request().postDataJSON())
  if (integrationType === 'discord_thread')
    expect(attempt.provider_config).toMatchObject({ public_key: 'ab'.repeat(32) })
  expect(attempt.credential_secret_id).toBe(integration.credential_secret_id)
  expect(credentialCreates).toBe(1)
  page.off('request', trackCredential)
  expect(JSON.stringify(integration)).not.toContain(
    integrationType === 'github_pr' ? 'PRIVATE KEY' : 'local-discord-token',
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

export async function expectIntegrationCapabilities(page: Page, integration: ProjectIntegration) {
  const capabilities = page.getByRole('region', { name: 'Advanced', exact: true })
  const disclosure = capabilities.getByRole('button', { name: 'Advanced', exact: true })
  await expect(disclosure).toHaveAttribute('aria-expanded', 'false')
  await disclosure.click()
  await expect(
    capabilities.getByText(`int__${integration.name}__read`, { exact: true }),
  ).toBeVisible()
  const subscription =
    integration.integration_type === 'github_pr' ? 'pull_request' : 'thread_messages'
  await expect(capabilities.getByText(subscription, { exact: true })).toBeVisible()
  if (integration.integration_type !== 'github_pr')
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
