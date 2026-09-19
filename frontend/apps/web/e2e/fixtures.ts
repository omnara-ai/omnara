import { schemas } from '@omnara/sdk'
import { expect, type Page } from '@playwright/test'

/** Browser-side OAuth result fixture; callback persistence is covered by Go integration tests. */
export async function mockSlackSetupReturn(page: Page, projectPath: string, projectID: string) {
  const connection = schemas.zIntegrationConnection.parse({
    id: `iin_${'a'.repeat(26)}`,
    org_id: projectPath.split('/')[4],
    project_id: projectID,
    provider: 'slack',
    provider_tenant_id: 'T_BROWSER',
    provider_account_ref: 'A_BROWSER',
    provider_agent_display_name: 'Browser Slack bot',
    state: 'active',
    provider_config: {},
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
  })
  await page.route(`**${projectPath}/integration-connections**`, async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const detail = new URL(route.request().url()).pathname.endsWith(`/${connection.id}`)
    await route.fulfill({ json: detail ? connection : { data: [connection], next_cursor: null } })
  })
  const appID = `app_${'a'.repeat(26)}`
  await page.route(`**${projectPath}/apps`, async (route) => {
    if (route.request().method() !== 'POST') return route.continue()
    const body = schemas.zSaveProjectAppRequest.parse(route.request().postDataJSON())
    expect(body.settings.resource.connection).toBe(connection.id)
    expect(body.settings.launcher).toBeUndefined()
    const app = schemas.zProjectApp.parse({
      ...body,
      id: appID,
      project_id: projectID,
      created_at: connection.created_at,
      updated_at: connection.updated_at,
    })
    await page.route(`**${projectPath}/apps/${appID}`, (read) => read.fulfill({ json: app }))
    await route.fulfill({ status: 201, json: app })
  })
  return { connection, appID }
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
