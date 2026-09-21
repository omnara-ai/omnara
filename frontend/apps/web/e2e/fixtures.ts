import { type AppType, type ProjectApp, schemas, zJsonText } from '@omnara/sdk'
import { expect, type Page } from '@playwright/test'
import { z } from 'zod'

/** Browser-side OAuth result fixture; callback persistence is covered by Go integration tests. */
export async function mockSlackSetupReturn(
  page: Page,
  projectPath: string,
  draft: ProjectApp,
  flowID: string,
) {
  // Keep the original setup while authorization is pending. Only the exact
  // flow completion advances the revision and connects this app.
  let app: ProjectApp = { ...draft }
  await page.route(`**${projectPath}/apps/${draft.id}`, async (route) => {
    if (route.request().method() === 'PUT') {
      const metadata = schemas.zSaveProjectAppRequest.parse(route.request().postDataJSON())
      if (metadata.name !== app.name || metadata.app_type !== app.app_type)
        throw new Error('The browser fixture cannot change app identity')
      app = { ...app, ...metadata, updated_at: new Date().toISOString() }
    } else if (route.request().method() !== 'GET') return route.continue()
    await route.fulfill({ json: app })
  })
  return {
    complete: () => {
      app = {
        ...app,
        state: 'active',
        setup_revision: app.setup_revision + 1,
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

export function installAppFailureTracking(page: Page) {
  return installFailureTracking(page, [
    /^page: Canceled$/,
    /request: .*\/agent-profiles\/aprf_[a-z2-7]+(?:\/config)? \(net::ERR_ABORTED\)$/,
    /request: .*\/apps\/app_[a-z2-7]+ \(net::ERR_ABORTED\)$/,
    // Chromium can abort the empty 204 DELETE stream through the TLS proxy.
    // The schedule journey asserts the 204 response and removal from the list.
    /request: .*\/cron-triggers\/cron_[a-z2-7]+ \(net::ERR_ABORTED\)$/,
    /request: .*\/subscriptions\/asub_[a-z2-7]+ \(net::ERR_ABORTED\)$/,
    /request: .*\/apps\/app_[a-z2-7]+\/subscriptions(?:\?.*)? \(net::ERR_ABORTED\)$/,
    /request: .*\/agent-configs\/tools \(net::ERR_ABORTED\)$/,
    // Navigation and successful writes cancel obsolete reads; writes are checked below.
    // Full-document navigation also cancels intent-preloaded route chunks.
    // HTTP failures and import/page errors are still recorded independently.
    /request: .*\/assets\/[^/]+\.js \(net::ERR_ABORTED\)$/,
  ])
}

export async function createAppDraft(
  page: Page,
  projectID: string,
  appType: AppType,
  name: string,
) {
  const label =
    appType === 'github_pr'
      ? 'GitHub PR review'
      : appType === 'discord_thread'
        ? 'Discord threads'
        : 'Slack threads'
  const appsPath = `/projects/${projectID}/apps`
  if (new URL(page.url()).pathname !== appsPath) await page.goto(appsPath)
  await page.getByRole('link', { name: 'Add app', exact: true }).click()
  await page.getByRole('link', { name: `Set up ${label}`, exact: false }).click()
  await expect(page).toHaveURL(`/projects/${projectID}/apps/new/${appType}`)
  await page.getByLabel('App name', { exact: true }).fill(name)
  const saved = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' && new URL(response.url()).pathname.endsWith('/apps'),
  )
  await page.getByRole('button', { name: 'Create app', exact: true }).click()
  const response = await saved
  expect(response.status()).toBe(201)
  expect(response.request().postDataJSON()).toEqual({
    name,
    app_type: appType,
    settings: {},
  })
  const app = schemas.zProjectApp.parse(await response.json())
  expect(app.state).toBe('disconnected')
  expect(app.credential_secret_id).toBeUndefined()
  await expect(page).toHaveURL(`/projects/${projectID}/apps/${app.id}`)
  await expect(page.getByText(/Finish setup: connect an account/)).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect account', exact: true })).toBeVisible()
  await expectAppCapabilities(page, app)
  return { app, apiProjectPath: response.url().replace(/\/apps$/, '') }
}

export async function readApp(page: Page, apiProjectPath: string, appID: string) {
  const body = await page.evaluate(async (path) => {
    const response = await fetch(path)
    if (!response.ok) throw new Error(`Read app failed: ${response.status}`)
    return response.text()
  }, `${apiProjectPath}/apps/${appID}`)
  return zJsonText.pipe(schemas.zProjectApp).parse(body)
}

export async function mockVerifiedAppSetup(
  page: Page,
  apiProjectPath: string,
  appType: Extract<AppType, 'github_pr' | 'discord_thread'>,
) {
  await page.route('**/apps/*/setup', async (route) => {
    if (route.request().method() !== 'POST') return route.continue()
    const request = schemas.zConfigureProjectAppRequest.parse(route.request().postDataJSON())
    const appID = schemas.zProjectAppId.parse(
      new URL(route.request().url()).pathname.split('/').at(-2),
    )
    expect(request.credential_secret_id).toMatch(/^sec_[a-z2-7]{26}$/)
    const seeded = await page.request.post(
      `${requiredEnvironmentVariable('OMNARA_WEB_E2E_PROVIDER_FIXTURE')}/apps/${appID}/setup`,
      { data: request },
    )
    expect(seeded.status()).toBe(200)
    expect(z.object({ id: schemas.zProjectAppId }).parse(await seeded.json()).id).toBe(appID)
    const app = await readApp(page, apiProjectPath, appID)
    expect(app.app_type).toBe(appType)
    expect(app.credential_secret_id).toBe(request.credential_secret_id)
    expect(app.setup_revision).toBe(request.expected_setup_revision + 1)
    await route.fulfill({ status: 200, json: app })
  })
}

export async function fillProviderAccount(
  page: Page,
  appType: Extract<AppType, 'github_pr' | 'discord_thread'>,
) {
  await page
    .getByLabel(appType === 'github_pr' ? 'GitHub App ID' : 'Discord Application ID', {
      exact: true,
    })
    .fill('111')
  await page
    .getByLabel(appType === 'github_pr' ? 'Installation ID' : 'Bot User ID', { exact: true })
    .fill('222')
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

export async function expectAppCapabilities(page: Page, app: ProjectApp) {
  const capabilities = page.getByRole('region', { name: 'Capabilities', exact: true })
  await expect(capabilities.getByText(`app__${app.name}__read`, { exact: true })).toBeVisible()
  const subscription = app.app_type === 'github_pr' ? 'pull_request' : 'thread_messages'
  await expect(capabilities.getByText(subscription, { exact: true })).toBeVisible()
  if (app.app_type !== 'github_pr')
    await expect(capabilities.getByText(app.name, { exact: true })).toBeVisible()
}

export async function expectInteractionToolMenu(page: Page) {
  await page.getByRole('button', { name: 'Add tools' }).click()
  await expect(page.getByRole('menuitem', { name: 'ask_question', exact: true })).toBeVisible()
  for (const name of ['list_interaction_handlers', 'set_interaction_handler'])
    await expect(page.getByRole('menuitem', { name, exact: true })).toHaveCount(0)
  await page.keyboard.press('Escape')
  await page.getByRole('button', { name: 'Other tools', exact: true }).click()
  for (const name of ['list_interaction_handlers', 'set_interaction_handler'])
    await expect(page.getByLabel(`${name} permission`, { exact: true })).toContainText(
      'Always allow',
    )
  await page.getByRole('button', { name: 'Other tools', exact: true }).click()
}
