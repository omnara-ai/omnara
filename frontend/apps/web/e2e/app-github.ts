import { type GitHubInstallations, type ProjectApp, schemas, zJsonText } from '@omnara/sdk'
import { expect, type Page, test } from '@playwright/test'
import { z } from 'zod'

/** Browser-only provider fixtures: no GitHub App, installation or credential is created. */
export async function exerciseGuidedGitHubSetup(page: Page, projectID: string) {
  const origin = new URL(page.url()).origin
  const secretID = `sec_${'g'.repeat(26)}`
  let app: ProjectApp = {
    id: `app_${'g'.repeat(26)}`,
    project_id: projectID,
    name: 'guided-reviewer',
    app_type: 'github_pr',
    state: 'disconnected',
    setup_revision: 1,
    settings: {},
    provider_tenant_id: '',
    provider_account_ref: '',
    provider_agent_display_name: '',
    provider_config: {},
    capabilities: { tools: {}, subscriptions: {} },
    created_at: '2026-09-22T00:00:00Z',
    updated_at: '2026-09-22T00:00:00Z',
  }
  const appPath = `/projects/${projectID}/apps/${app.id}`
  const manifest = {
    name: 'Guided reviewer',
    public: false,
    request_oauth_on_install: false,
    default_permissions: { pull_requests: 'write', issues: 'read' },
    default_events: [
      'pull_request',
      'issue_comment',
      'pull_request_review',
      'pull_request_review_comment',
    ],
  }
  const installation = {
    id: '222',
    account: 'engineering',
    account_type: 'Organization',
    settings_url: 'https://github.com/organizations/engineering/settings/installations/222',
  }
  const installations: GitHubInstallations = {
    provider_app_id: '111',
    name: 'Guided reviewer',
    slug: 'guided-reviewer',
    install_url: `https://github.com/apps/guided-reviewer/installations/new?state=${secretID}`,
    installations: [installation],
  }
  let approved = false,
    formPosts = 0,
    inspections = 0,
    connects = 0
  await page.route(`**/projects/${projectID}/apps`, async (route) => {
    if (route.request().method() !== 'POST') return route.continue()
    const request = schemas.zSaveProjectAppRequest.parse(route.request().postDataJSON())
    expect(request.settings).toEqual({})
    app = { ...app, name: request.name }
    await route.fulfill({ status: 201, json: app })
  })
  await page.route(`**/apps/${app.id}`, (route) => route.fulfill({ json: app }))
  await page.route(`**/apps/${app.id}/subscriptions*`, (route) =>
    route.fulfill({ json: { data: [], next_cursor: null } }),
  )
  await page.route(`**/projects/${projectID}/secrets*`, (route) =>
    route.fulfill({ json: { data: [], next_cursor: null } }),
  )
  await page.route(`**/apps/${app.id}/github-setup`, async (route) => {
    expect(schemas.zCreateGitHubSetupRequest.parse(route.request().postDataJSON())).toEqual({
      expected_setup_revision: 1,
      organization: 'engineering',
    })
    await route.fulfill({
      status: 201,
      json: {
        app_id: app.id,
        setup_revision: 1,
        registration_url:
          'https://github.com/organizations/engineering/settings/apps/new?state=mock-registration',
        manifest,
        expires_at: '2099-01-01T00:00:00Z',
      },
    })
  })
  await page.route(`**/apps/${app.id}/github-setup/installations`, async (route) => {
    inspections++
    expect(
      schemas.zInspectGitHubInstallationsRequest.parse(route.request().postDataJSON()),
    ).toEqual({ credentials_secret_ref: secretID, page: 1 })
    await route.fulfill({
      json: { ...installations, installations: approved ? installations.installations : [] },
    })
  })
  await page.route(`**/apps/${app.id}/setup`, async (route) => {
    connects++
    expect(schemas.zConfigureProjectAppRequest.parse(route.request().postDataJSON())).toEqual({
      expected_setup_revision: 1,
      provider_tenant_id: '111',
      provider_account_ref: '222',
      credential_secret_id: secretID,
    })
    app = {
      ...app,
      state: 'active',
      setup_revision: 2,
      provider_tenant_id: '111',
      provider_account_ref: '222',
      credential_secret_id: secretID,
      provider_agent_display_name: 'Guided reviewer',
      updated_at: '2026-09-22T00:01:00Z',
    }
    await route.fulfill({ json: app })
  })
  await page.context().route('https://github.com/**', async (route) => {
    if (route.request().method() === 'POST') {
      formPosts++
      expect(new URL(route.request().url()).pathname).toBe(
        '/organizations/engineering/settings/apps/new',
      )
      const posted = new URLSearchParams(route.request().postData() ?? '').get('manifest')
      expect(zJsonText.pipe(z.json()).parse(posted)).toEqual(manifest)
      await route.fulfill({
        status: 302,
        headers: {
          location: `${origin}${appPath}?github_setup=credentials_saved&credentials_secret_ref=${secretID}`,
        },
        body: '',
      })
    } else {
      expect(route.request().url()).toBe(installations.install_url)
      await route.fulfill({
        contentType: 'text/html',
        body: '<p>Mock GitHub installation approval</p>',
      })
    }
  })

  await page.goto(`/projects/${projectID}/apps/new/github_pr`)
  await expect(page.getByRole('button', { name: 'Continue to GitHub', exact: true })).toBeVisible()
  await expect(page.getByLabel('GitHub App ID', { exact: true })).toHaveCount(0)
  await expect(page.getByLabel('Bot display name', { exact: false })).toHaveCount(0)
  await page.getByLabel('App name', { exact: true }).fill('guided-reviewer')
  await captureGitHubSetup(page, 'personal')
  await page.getByLabel('GitHub App owner').selectOption('organization')
  await page.getByLabel('Organization login').fill('engineering')
  await captureGitHubSetup(page, 'organization')
  await page.getByRole('button', { name: 'Continue to GitHub', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Check installations', exact: true })).toBeVisible()
  await expect(page).toHaveURL(appPath)
  expect(formPosts).toBe(1)
  expect(inspections).toBe(0)
  expect(connects).toBe(0)
  await page.getByRole('button', { name: 'Check installations', exact: true }).click()
  await expect(page.getByText('No installations on this page.', { exact: false })).toBeVisible()
  await captureGitHubSetup(page, 'approval-pending')
  const popup = page.waitForEvent('popup')
  await page.getByRole('link', { name: 'Install in GitHub', exact: true }).click()
  const installPage = await popup
  await expect(installPage.getByText('Mock GitHub installation approval')).toBeVisible()
  await installPage.close()
  approved = true
  await page.getByRole('button', { name: 'Refresh installations', exact: true }).click()
  await expect(page.getByLabel('Installation', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Connect app', exact: true })).toBeDisabled()
  await page.getByLabel('Installation', { exact: true }).selectOption('222')
  await expect(
    page.getByRole('link', { name: 'Manage repository access in GitHub' }),
  ).toHaveAttribute('href', installation.settings_url)
  await captureGitHubSetup(page, 'installation-selected')
  expect(connects).toBe(0)
  await page.getByRole('button', { name: 'Connect app', exact: true }).click()
  await expect(page.getByText('Account connected.', { exact: false })).toBeVisible()
  expect(connects).toBe(1)
  const launch = page.getByRole('region', { name: 'Pull requests', exact: true })
  await expect(
    launch.getByRole('checkbox', { name: 'Launch agents from GitHub events', exact: true }),
  ).not.toBeChecked()
  await launch
    .getByRole('checkbox', { name: 'Launch agents from GitHub events', exact: true })
    .check()
  await expect(launch.getByRole('combobox', { name: 'Agent profile', exact: true })).toBeVisible()
  await expect(launch.getByLabel('Repository ID')).toHaveCount(0)
  await captureGitHubSetup(page, 'connected')
}

async function captureGitHubSetup(page: Page, phase: string) {
  const viewport = page.viewportSize()
  if (!viewport) throw new Error('Browser audit requires a viewport')
  for (const width of [viewport.width, 390]) {
    await page.setViewportSize({ width, height: viewport.height })
    await expect
      .poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth))
      .toBe(true)
    const name = `github-guided-${phase}-${width}`
    const path = test.info().outputPath(`${name}.png`)
    await page.screenshot({ path, fullPage: true })
    await test.info().attach(name, { path, contentType: 'image/png' })
  }
  await page.setViewportSize(viewport)
}
