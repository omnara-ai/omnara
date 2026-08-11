import { expect, type Page, test } from '@playwright/test'

function requiredEnvironmentVariable(name: string): string {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required. Run \`make web-e2e\` from the repository root.`)
  return value
}

const projectID = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROJECT_ID')
const orgName = requiredEnvironmentVariable('OMNARA_WEB_E2E_ORG_NAME')
const switchOrgName = requiredEnvironmentVariable('OMNARA_WEB_E2E_SWITCH_ORG_NAME')
const createdOrgName = 'zz web created organization'
const adminEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_ADMIN_EMAIL')
const viewerEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_VIEWER_EMAIL')
const password = requiredEnvironmentVariable('OMNARA_WEB_E2E_PASSWORD')
const providerConfig = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROVIDER_CONFIG')
const modelName = requiredEnvironmentVariable('OMNARA_WEB_E2E_MODEL_NAME')
const createAgentPath = `/projects/${projectID}/agents/new`

function installFailureTracking(page: Page) {
  const failures: string[] = []

  page.on('pageerror', (error) => {
    failures.push(`page: ${error.message}`)
  })
  page.on('requestfailed', (request) => {
    if (request.method() === 'POST' && new URL(request.url()).pathname === '/api/auth/login') return
    failures.push(`request: ${request.url()} (${request.failure()?.errorText ?? 'failed'})`)
  })
  page.on('response', (response) => {
    const url = new URL(response.url())
    if (response.status() === 401 && url.pathname === '/api/v1/me') return
    if (response.status() >= 400) {
      failures.push(`response: ${response.status()} ${url.pathname}`)
    }
  })

  return failures
}

async function signIn(page: Page, email: string, returnTo: string) {
  await page.goto(returnTo)

  await expect(page).toHaveURL((url) => {
    return url.pathname === '/login' && url.searchParams.get('return_to') === returnTo
  })
  await expect(page.getByRole('heading', { name: 'Sign in to Omnara' })).toBeVisible()

  await page.getByLabel('Email').fill(email)
  await page.getByLabel('Password').fill(password)
  await page.getByRole('button', { name: 'Sign in' }).click()

  await expect(page).toHaveURL(returnTo)
}

test('returns to a protected deep link after login', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, adminEmail, `${createAgentPath}?source=e2e#yaml`)
  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeVisible()
  expect(failures).toEqual([])
})

test('switches organizations from a project page', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, adminEmail, `/projects/${projectID}/agents`)

  await page.getByRole('button', { name: orgName }).click()
  await page.getByRole('menuitem', { name: switchOrgName }).click()

  await expect(page).toHaveURL('/')
  await expect(page.getByRole('button', { name: switchOrgName })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Project not found' })).toHaveCount(0)
  expect(failures).toEqual([])
})

test('creates an organization from a project page', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, adminEmail, `/projects/${projectID}/agents`)

  await page.getByRole('button', { name: orgName }).click()
  await page.getByRole('menuitem', { name: 'New organization' }).click()
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(createdOrgName)
  await page.getByRole('button', { name: 'Create organization' }).click()

  await expect(page).toHaveURL('/')
  await expect(page.getByRole('button', { name: createdOrgName })).toBeVisible()
  await expect(page.getByRole('heading', { name: 'Project not found' })).toHaveCount(0)
  expect(failures).toEqual([])
})

test('creates an agent from YAML', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, adminEmail, createAgentPath)

  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'YAML' })).toBeVisible()

  await page.getByRole('button', { name: 'YAML' }).click()
  await page.getByLabel('Agent name (optional)').fill('YAML E2E Agent')

  await page.getByLabel('Name', { exact: true }).fill('YAML E2E Agent')
  const editorInput = page.getByRole('textbox', { name: 'Config (YAML)' })
  await expect(editorInput).toBeVisible()

  await editorInput.focus()
  const yamlLines = [
    'instruction: Test agent creation from YAML.',
    `model: { provider_config: "${providerConfig}", name: "${modelName}" }`,
  ]
  await page.keyboard.insertText(yamlLines.join('\n'))
  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeEnabled()
  await page.getByRole('button', { name: 'Create & launch agent' }).click()

  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agents/agt_[a-z2-7]+$`))
  await expect(page.getByText('YAML E2E Agent', { exact: true })).toBeVisible()
  expect(failures).toEqual([])
})

test('creates an agent with the Builder', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, adminEmail, createAgentPath)

  await expect(page.getByRole('button', { name: 'Builder' })).toBeVisible()
  await page.getByLabel('Name', { exact: true }).fill('Builder Agent')
  await page.getByLabel('Instruction').fill('Use the visual Builder to create this test agent.')

  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeEnabled()
  await page.getByRole('button', { name: 'Create & launch agent' }).click()

  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agents/agt_[a-z2-7]+$`))
  await expect(page.getByText('Builder Agent', { exact: true })).toBeVisible()
  expect(failures).toEqual([])
})

test('creates a profile without launching an agent', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, adminEmail, createAgentPath)

  await page.getByLabel('Name', { exact: true }).fill('Profile Only Agent')
  await page.getByLabel('Instruction').fill('Save this config as a profile without launching.')

  await expect(page.getByRole('button', { name: 'Create profile' })).toBeEnabled()
  await page.getByRole('button', { name: 'Create profile' }).click()

  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agent-profiles/aprf_[a-z2-7]+$`))
  await expect(page.getByText('Profile Only Agent').first()).toBeVisible()
  expect(failures).toEqual([])
})

test('denies agent creation when the project lacks manage permission', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, viewerEmail, createAgentPath)

  await expect(page.getByRole('heading', { name: 'Not allowed' })).toBeVisible()
  await expect(
    page.getByText('You don’t have permission to create agents in this project.'),
  ).toBeVisible()
  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toHaveCount(0)
  expect(failures).toEqual([])
})
