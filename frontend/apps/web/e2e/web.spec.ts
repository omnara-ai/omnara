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
const inviteeEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_INVITEE_EMAIL')
const password = requiredEnvironmentVariable('OMNARA_WEB_E2E_PASSWORD')
const providerConfig = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROVIDER_CONFIG')
const modelName = requiredEnvironmentVariable('OMNARA_WEB_E2E_MODEL_NAME')
const ungrantedModelName = requiredEnvironmentVariable('OMNARA_WEB_E2E_UNGRANTED_MODEL')
const createAgentPath = `/projects/${projectID}/agents/new`

function installFailureTracking(page: Page, ignore: RegExp[] = []) {
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

async function selectConfiguredModel(page: Page) {
  const modelPicker = page.getByRole('combobox', { name: 'Search granted models…' })
  await modelPicker.fill(modelName)
  await page
    .getByRole('option')
    .filter({ hasText: modelName })
    .filter({ hasText: providerConfig })
    .click()
}

async function createProfile(page: Page, name: string, instruction: string) {
  await signIn(page, adminEmail, createAgentPath)
  await page.getByLabel('Name', { exact: true }).fill(name)
  await page.getByLabel('Instruction').fill(instruction)
  await selectConfiguredModel(page)
  await expect(page.getByRole('button', { name: 'Create profile' })).toBeEnabled()
  await page.getByRole('button', { name: 'Create profile' }).click()
  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agent-profiles/aprf_[a-z2-7]+$`))
}

async function typeInConfigEditor(page: Page, text: string) {
  const editor = page.getByRole('textbox', { name: 'Config (YAML)' })
  await expect(editor).toBeVisible()
  await editor.focus()
  await page.keyboard.insertText(text)
}

async function visitAgentsTabAndReturn(page: Page) {
  await page.getByRole('button', { name: 'Agents', exact: true }).click()
  await expect(page.getByText('No agents from this profile yet', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: 'Configuration' }).click()
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

test('opens and declines pending invitations outside onboarding', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, inviteeEmail, '/')

  const pendingInvitations = page.getByRole('link', {
    name: /^Pending invitations, [1-3]$/,
  })
  await expect(pendingInvitations).toBeVisible()
  await pendingInvitations.click()

  await expect(page).toHaveURL('/invitations')
  await expect(page.getByRole('heading', { name: 'Pending invitations' })).toBeVisible()

  const declineButtons = page.getByRole('button', { name: /^Decline invitation to / })
  await expect(declineButtons.first()).toBeVisible()
  const invitationCount = await declineButtons.count()
  expect(invitationCount).toBeGreaterThan(0)

  const invitationLabel = await declineButtons.first().getAttribute('aria-label')
  const invitationName = invitationLabel?.replace('Decline invitation to ', '').trim()
  if (!invitationName) throw new Error('Pending invitation organization name is missing')
  await declineButtons.first().click()

  await expect(
    page.getByRole('button', { name: `Decline invitation to ${invitationName}` }),
  ).toHaveCount(0)
  await expect(declineButtons).toHaveCount(invitationCount - 1)

  expect(failures).toEqual([])
})

test('creates an agent from YAML', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, adminEmail, createAgentPath)

  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'YAML' })).toBeVisible()

  await page.getByRole('button', { name: 'YAML' }).click()

  await page.getByLabel('Name', { exact: true }).fill('YAML E2E Agent')
  const yamlLines = [
    'instruction: Test agent creation from YAML.',
    `model: { provider_config: "${providerConfig}", name: "${modelName}" }`,
  ]
  await typeInConfigEditor(page, yamlLines.join('\n'))
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
  await selectConfiguredModel(page)

  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeEnabled()
  await page.getByRole('button', { name: 'Create & launch agent' }).click()

  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agents/agt_[a-z2-7]+$`))
  await expect(page.getByText('Builder Agent', { exact: true })).toBeVisible()
  expect(failures).toEqual([])
})

test('creates a profile without launching an agent', async ({ page }) => {
  const failures = installFailureTracking(page)
  await createProfile(
    page,
    'Profile Only Agent',
    'Save this config as a profile without launching.',
  )
  await expect(page.getByText('Profile Only Agent').first()).toBeVisible()
  expect(failures).toEqual([])
})

test('granting a model from the Builder does not create a profile or agent', async ({ page }) => {
  const failures = installFailureTracking(page)
  await page.route(/\/model-grants(?:\?.*)?$/, async (route) => {
    if (route.request().method() !== 'POST') {
      await route.continue()
      return
    }
    const body = route.request().postDataJSON() as { configured_model_id: string }
    const timestamp = new Date().toISOString()
    await route.fulfill({
      status: 201,
      contentType: 'application/json',
      body: JSON.stringify({
        grant: {
          id: `pmog_${'a'.repeat(26)}`,
          org_id: `org_${'a'.repeat(26)}`,
          project_id: projectID,
          configured_model_id: body.configured_model_id,
          supported_reasoning_efforts: [],
          input_modalities: [],
          output_modalities: [],
          created_at: timestamp,
          updated_at: timestamp,
        },
      }),
    })
  })
  await signIn(page, adminEmail, createAgentPath)

  await page.getByLabel('Name', { exact: true }).fill('Unintended Agent')
  await page.getByLabel('Instruction').fill('This form must not submit from a grant dialog.')
  await selectConfiguredModel(page)
  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeEnabled()

  let agentSubmissionRequests = 0
  page.on('request', (request) => {
    const path = new URL(request.url()).pathname
    if (
      request.method() === 'POST' &&
      (path.endsWith('/agent-configs') ||
        path.endsWith('/agent-profiles') ||
        path.endsWith('/agents'))
    ) {
      agentSubmissionRequests += 1
    }
  })

  await page.getByRole('button', { name: 'Grant models' }).click()
  const dialog = page.getByRole('dialog', { name: 'Grant models' })
  const providerPicker = dialog.getByRole('combobox', { name: 'Search model providers…' })
  await providerPicker.fill(providerConfig)
  await page.getByRole('option', { name: providerConfig }).click()
  const configuredModelPicker = dialog.getByRole('combobox', {
    name: 'Search configured models…',
  })
  await configuredModelPicker.fill(ungrantedModelName)
  await page.getByRole('option', { name: ungrantedModelName }).click()
  await dialog.getByRole('button', { name: 'Grant models' }).click()

  await expect(dialog).toHaveCount(0)
  await expect(page).toHaveURL(createAgentPath)
  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeEnabled()
  expect(agentSubmissionRequests).toBe(0)
  expect(failures).toEqual([])
})

test('keeps profile config edits across tabs and confirms launching with unsaved edits', async ({
  page,
}) => {
  const failures = installFailureTracking(page, [/^page: Canceled$/])
  await createProfile(page, 'Draft Keeper Profile', 'Keep unsaved edits across tab switches.')

  await typeInConfigEditor(page, '# draft edit\n')
  await expect(page.getByRole('button', { name: 'Save revision' })).toBeEnabled()

  await visitAgentsTabAndReturn(page)
  await expect(page.getByText('# draft edit')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Save revision' })).toBeEnabled()

  const confirms: string[] = []
  page.once('dialog', (dialog) => {
    confirms.push(dialog.message())
    void dialog.dismiss()
  })
  await page.getByRole('button', { name: 'Launch' }).click()
  await expect.poll(() => confirms.length).toBe(1)
  expect(confirms[0]).toContain('unsaved configuration changes')
  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agent-profiles/aprf_[a-z2-7]+$`))

  await page.getByRole('button', { name: 'Discard changes' }).click()
  await expect(page.getByText('# draft edit')).toHaveCount(0)
  await expect(page.getByRole('button', { name: 'Save revision' })).toBeDisabled()

  await page.getByRole('button', { name: 'Launch' }).click()
  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agents/agt_[a-z2-7]+$`))
  expect(failures).toEqual([])
})

test('keeps the save pending across tab switches while the revision uploads', async ({ page }) => {
  const failures = installFailureTracking(page, [/^page: Canceled$/])
  await createProfile(page, 'Pending Save Profile', 'Hold the save request while tabs switch.')

  let releaseSave!: () => void
  const holdSave = new Promise<void>((resolve) => {
    releaseSave = resolve
  })
  await page.route('**/agent-profiles/aprf_*/config', async (route) => {
    await holdSave
    await route.continue()
  })

  await typeInConfigEditor(page, '# pending save\n')

  const saveButton = page.getByRole('button', { name: 'Save revision' })
  await expect(saveButton).toBeEnabled()
  await saveButton.click()
  await expect(saveButton).toBeDisabled()

  await visitAgentsTabAndReturn(page)

  await expect(page.getByText('# pending save')).toBeVisible()
  await expect(saveButton).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Discard changes' })).toHaveCount(0)

  const saveResponse = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      /\/agent-profiles\/aprf_[a-z2-7]+\/config$/.test(new URL(response.url()).pathname),
  )
  releaseSave()
  expect((await saveResponse).ok()).toBe(true)

  await expect(saveButton).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Discard changes' })).toHaveCount(0)
  expect(failures).toEqual([])
})

test('deletes a profile from its detail page', async ({ page }) => {
  const failures = installFailureTracking(page, [
    /agent-profiles\/aprf_[a-z2-7]+ \(net::ERR_ABORTED\)$/,
    /^response: 404 .*\/agent-profiles\/aprf_[a-z2-7]+$/,
  ])
  await createProfile(page, 'Deleted Profile E2E', 'Delete this profile from its detail page.')

  page.once('dialog', (dialog) => void dialog.accept())
  await page.getByRole('button', { name: 'Row actions' }).click()
  await page.getByRole('menuitem', { name: 'Delete' }).click()

  await expect(page).toHaveURL(`/projects/${projectID}/agents`)
  await expect(page.getByRole('heading', { name: 'Agent profiles' })).toBeVisible()
  await expect(page.getByText('Deleted Profile E2E')).toHaveCount(0)

  await page.goBack()
  await expect(page.getByRole('heading', { name: 'Something went wrong' })).toBeVisible()
  await expect(page.getByText(/^404: /)).toBeVisible()
  await expect(page.getByText('Deleted Profile E2E')).toHaveCount(0)
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
