import { readFile } from 'node:fs/promises'

import { type Cookie, expect, type Page, type Request, test } from '@playwright/test'
import { z } from 'zod'

function requiredEnvironmentVariable(name: string): string {
  const value = process.env[name]
  if (!value) throw new Error(`${name} is required. Run \`make web-e2e\` from the repository root.`)
  return value
}

const projectID = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROJECT_ID')
const orgName = requiredEnvironmentVariable('OMNARA_WEB_E2E_ORG_NAME')
const switchOrgName = requiredEnvironmentVariable('OMNARA_WEB_E2E_SWITCH_ORG_NAME')
const createdOrgName = 'zz web created organization'
const modelGrantRequest = z.object({ configured_model_id: z.string() })
const adminEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_ADMIN_EMAIL')
const viewerEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_VIEWER_EMAIL')
const inviteeEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_INVITEE_EMAIL')
const onboardingEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_ONBOARDING_EMAIL')
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

const sessionCookies = new Map<string, Cookie[]>()

async function signInThroughLoginForm(page: Page, email: string, returnTo: string) {
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

async function signIn(page: Page, email: string, returnTo: string) {
  const cookies = sessionCookies.get(email)
  if (cookies) {
    await page.context().addCookies(cookies)
    await page.goto(returnTo)
    await expect(page).toHaveURL(returnTo)
    return
  }
  await signInThroughLoginForm(page, email, returnTo)
}

async function selectConfiguredModel(page: Page) {
  const modelPicker = page.getByRole('combobox', { name: 'Model', exact: true })
  await modelPicker.click()
  await page.getByPlaceholder('Search granted models…').fill(modelName)
  await page
    .getByRole('option')
    .filter({ hasText: modelName })
    .filter({ hasText: providerConfig })
    .click()
}

async function createProfile(page: Page, name: string, instruction: string) {
  await signIn(page, adminEmail, createAgentPath)
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(name)
  await page.getByLabel('Instruction').fill(instruction)
  await selectConfiguredModel(page)
  await expect(page.getByRole('button', { name: 'Create profile' })).toBeEnabled()
  await page.getByRole('button', { name: 'Create profile' }).click()
  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agent-profiles/aprf_[a-z2-7]+$`))
}

function uniqueName(base: string) {
  const retry = test.info().retry
  return retry === 0 ? base : `${base} (retry ${retry})`
}

async function typeInConfigEditor(page: Page, text: string) {
  const editor = page.getByRole('textbox', { name: 'Config (YAML)' })
  await expect(editor).toBeVisible()
  await editor.focus()
  await page.keyboard.insertText(text)
}

async function replaceConfigEditor(page: Page, text: string) {
  const editor = page.getByRole('textbox', { name: 'Config (YAML)' })
  await expect(editor).toBeVisible()
  await editor.focus()
  await page.keyboard.press('Control+A')
  await page.keyboard.insertText(text)
}

async function visitAgentsTabAndReturn(page: Page) {
  await page.getByRole('button', { name: 'Agents', exact: true }).click()
  await expect(page.getByText('No agents from this profile yet', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: 'Configuration' }).click()
}

test('returns to a protected deep link after login', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signInThroughLoginForm(page, adminEmail, `${createAgentPath}?source=e2e#yaml`)
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

test('opens and answers pending invitations outside onboarding', async ({ page }) => {
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

  const acceptButton = page.getByRole('button', { name: /^Accept invitation to / }).first()
  const acceptLabel = await acceptButton.getAttribute('aria-label')
  const acceptedOrgName = acceptLabel?.replace('Accept invitation to ', '').trim()
  if (!acceptedOrgName) throw new Error('Accepted invitation organization name is missing')
  await acceptButton.click()

  await expect(page).toHaveURL('/')
  await expect(page.getByRole('button', { name: acceptedOrgName })).toBeVisible()

  expect(failures).toEqual([])
})

test('creates an agent from YAML', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, adminEmail, createAgentPath)

  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeVisible()
  await expect(page.getByRole('button', { name: 'YAML' })).toBeVisible()

  await page.getByRole('button', { name: 'YAML' }).click()
  await expect(page.getByRole('textbox', { name: 'Config (YAML)' })).toHaveAttribute(
    'aria-required',
    'true',
  )

  const agentName = uniqueName('YAML E2E Agent')
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(agentName)
  const yamlLines = [
    'instruction: Test agent creation from YAML.',
    `model: { provider_config: "${providerConfig}", name: "${modelName}" }`,
  ]
  await replaceConfigEditor(page, yamlLines.join('\n'))
  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeEnabled()
  await page.getByRole('button', { name: 'Create & launch agent' }).click()

  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agents/agt_[a-z2-7]+/events$`))
  await expect(page.locator('[data-slot="breadcrumb-page"]')).toHaveText(agentName)
  expect(failures).toEqual([])
})

test('creates an agent with the Builder', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, adminEmail, createAgentPath)

  await expect(page.getByRole('button', { name: 'Builder' })).toBeVisible()
  await expect(page.getByRole('combobox', { name: 'Model', exact: true })).toHaveAttribute(
    'aria-required',
    'true',
  )
  await expect(page.getByText('web_search', { exact: true })).toBeVisible()
  await expect(page.getByText('web_fetch', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Toolsets' })).toHaveCount(0)
  await expect(page.getByLabel('First message (optional)')).toHaveCount(0)
  await expect(page.getByText('No machine sources', { exact: true })).toHaveCount(0)
  await expect(page.getByText('No skills attached', { exact: true })).toHaveCount(0)
  await expect(page.getByText('No MCP servers', { exact: true })).toHaveCount(0)

  await page.getByRole('button', { name: 'Add tools' }).click()
  await expect(page.getByRole('menuitem', { name: 'skill', exact: true })).toHaveCount(0)
  await expect(
    page.getByRole('menuitem', { name: 'send_integration_message', exact: true }),
  ).toHaveCount(0)
  await expect(
    page.getByRole('menuitem', { name: 'set_integration_target', exact: true }),
  ).toHaveCount(0)
  await page.keyboard.press('Escape')

  const agentName = uniqueName('Builder Agent')
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(agentName)
  await page.getByLabel('Instruction').fill('Use the visual Builder to create this test agent.')
  await selectConfiguredModel(page)

  const modelPicker = page.getByRole('combobox', { name: 'Model', exact: true })
  await modelPicker.press('m')
  const modelSearch = page.getByPlaceholder('Search granted models…')
  await expect(modelSearch).toHaveValue('m')
  await modelSearch.fill(modelName)
  await modelSearch.clear()
  await expect(modelSearch).toHaveValue('')
  await expect(modelPicker).toContainText(modelName)
  await modelSearch.press('Escape')

  await page.getByRole('button', { name: `Clear ${modelName} · ${providerConfig}` }).click()
  await expect(modelPicker).not.toContainText(modelName)
  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeDisabled()
  await selectConfiguredModel(page)

  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeEnabled()
  await page.getByRole('button', { name: 'Create & launch agent' }).click()

  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agents/agt_[a-z2-7]+/events$`))
  await expect(page.locator('[data-slot="breadcrumb-page"]')).toHaveText(agentName)
  expect(failures).toEqual([])
})

test('creates a profile without launching an agent', async ({ page }) => {
  const failures = installFailureTracking(page)
  const profileName = uniqueName('Profile Only Agent')
  await createProfile(page, profileName, 'Save this config as a profile without launching.')
  await expect(page.getByText(profileName).first()).toBeVisible()
  expect(failures).toEqual([])
})

test('granting a model from the Builder does not create a profile or agent', async ({ page }) => {
  const failures = installFailureTracking(page)
  await page.route(/\/model-grants(?:\?.*)?$/, async (route) => {
    if (route.request().method() !== 'POST') {
      await route.continue()
      return
    }
    const body = modelGrantRequest.parse(route.request().postDataJSON())
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

  await page.getByRole('textbox', { name: 'Name', exact: true }).fill('Unintended Agent')
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

  const modelPicker = page.getByRole('combobox', { name: 'Model', exact: true })
  await modelPicker.click()
  await expect(page.getByPlaceholder('Search granted models…')).toHaveValue('')
  const grantModelsAction = page.getByRole('button', { name: 'Grant models…', exact: true })
  await expect(grantModelsAction).toBeVisible()
  const providerListResponse = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return response.request().method() === 'GET' && url.pathname.endsWith('/model-provider-configs')
  })
  await grantModelsAction.press('Enter')
  const dialog = page.getByRole('dialog', { name: 'Grant models' })
  const providerPicker = dialog.getByRole('combobox', { name: 'Provider', exact: true })
  expect((await providerListResponse).ok()).toBe(true)
  await dialog.getByText('Provider', { exact: true }).click()
  const providerSearch = page.getByPlaceholder('Search model providers…')
  await expect(providerSearch).toBeFocused()
  await page.keyboard.type(providerConfig)
  await expect(providerSearch).toHaveValue(providerConfig)
  await page.getByRole('option', { name: providerConfig }).click()
  const configuredModelPicker = dialog.getByRole('combobox', {
    name: 'Models',
    exact: true,
  })
  await configuredModelPicker.fill(ungrantedModelName)
  await page.getByRole('option', { name: ungrantedModelName }).click()
  await expect(dialog.getByRole('button', { name: `Remove ${ungrantedModelName}` })).toBeVisible()
  await dialog.getByRole('button', { name: `Clear ${providerConfig}` }).click()
  await expect(dialog.getByRole('button', { name: `Remove ${ungrantedModelName}` })).toHaveCount(0)
  await expect(dialog.getByRole('button', { name: 'Grant models', exact: true })).toBeDisabled()
  await providerPicker.click()
  await providerSearch.fill(providerConfig)
  await page.getByRole('option', { name: providerConfig }).click()
  await configuredModelPicker.fill(ungrantedModelName)
  await page.getByRole('option', { name: ungrantedModelName }).click()
  await dialog.getByRole('button', { name: 'Grant models' }).click()

  await expect(dialog).toHaveCount(0)
  await expect(modelPicker).toBeFocused()
  await expect(page).toHaveURL(createAgentPath)
  await page.keyboard.press('Escape')
  await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeEnabled()
  expect(agentSubmissionRequests).toBe(0)
  expect(failures).toEqual([])
})

test('keeps profile config edits across tabs and confirms launching with unsaved edits', async ({
  page,
}) => {
  const failures = installFailureTracking(page, [/^page: Canceled$/])
  await createProfile(
    page,
    uniqueName('Draft Keeper Profile'),
    'Keep unsaved edits across tab switches.',
  )

  await page.getByRole('button', { name: 'YAML' }).click()
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
  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agents/agt_[a-z2-7]+/events$`))
  expect(failures).toEqual([])
})

test('keeps the save pending across tab switches while the revision uploads', async ({ page }) => {
  const failures = installFailureTracking(page, [/^page: Canceled$/])
  await createProfile(
    page,
    uniqueName('Pending Save Profile'),
    'Hold the save request while tabs switch.',
  )

  let releaseSave!: () => void
  const holdSave = new Promise<void>((resolve) => {
    releaseSave = resolve
  })
  await page.route('**/agent-profiles/aprf_*/config', async (route) => {
    await holdSave
    await route.continue()
  })

  await page.getByRole('button', { name: 'YAML' }).click()
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

test('renames a profile from its detail page', async ({ page }) => {
  const failures = installFailureTracking(page)
  const renamedName = uniqueName('Renamed Profile E2E')
  await createProfile(
    page,
    uniqueName('Rename Profile E2E'),
    'Rename this profile from its detail page.',
  )

  await page.getByRole('button', { name: 'Rename profile' }).click()
  await page.getByRole('textbox', { name: 'Profile name' }).fill(renamedName)
  await page.getByRole('button', { name: 'Save name' }).click()

  await expect(page.getByRole('heading', { name: renamedName })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Rename profile' })).toBeVisible()
  expect(failures).toEqual([])
})

test('deletes a profile from its detail page', async ({ page }) => {
  const failures = installFailureTracking(page, [
    /agent-profiles\/aprf_[a-z2-7]+ \(net::ERR_ABORTED\)$/,
    /^response: 404 .*\/agent-profiles\/aprf_[a-z2-7]+$/,
  ])
  const profileName = uniqueName('Deleted Profile E2E')
  await createProfile(page, profileName, 'Delete this profile from its detail page.')

  page.once('dialog', (dialog) => void dialog.accept())
  const agentsListed = page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith(`/projects/${projectID}/agents`) && response.ok(),
  )
  await page.getByRole('button', { name: 'Delete profile' }).click()

  await expect(page).toHaveURL(`/projects/${projectID}/agents`)
  await expect(page.getByRole('heading', { name: 'Agent profiles' })).toBeVisible()
  await expect(page.getByText(profileName)).toHaveCount(0)
  await agentsListed

  await page.goBack()
  await expect(page.getByRole('heading', { name: 'Something went wrong' })).toBeVisible()
  await expect(page.getByText(/^404: /)).toBeVisible()
  await expect(page.getByText(profileName)).toHaveCount(0)
  expect(failures).toEqual([])
})

test('edits a profile with the Builder', async ({ page }) => {
  const failures = installFailureTracking(page)
  await createProfile(page, uniqueName('Builder Edit Agent'), 'Original instruction.')

  await expect(page.getByRole('combobox', { name: 'Model', exact: true })).toBeVisible()
  const instruction = page.getByLabel('Instruction')
  await expect(instruction).toHaveValue('Original instruction.')
  const save = page.getByRole('button', { name: 'Save revision' })
  await expect(save).toBeDisabled()

  await instruction.fill('Updated instruction.')
  await expect(save).toBeEnabled()
  await save.click()
  await expect(save).toBeDisabled()

  await page.getByRole('button', { name: 'YAML' }).click()
  await expect(page.locator('.monaco-editor')).toContainText('Updated instruction.')

  await page.getByRole('button', { name: 'Integrations' }).click()
  await expect(page.getByText('No integrations yet.')).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add integration' })).toBeVisible()
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

test('walks a new organization through onboarding to its first chat', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, onboardingEmail, '/')

  await expect(page.getByRole('heading', { name: 'Launch your first agent' })).toBeVisible()
  const step = (index: number) => page.locator(`[data-step="${index}"]`)

  await expect(step(1)).toHaveAttribute('data-status', 'active')
  await expect(step(1)).toContainText('npx omnara login')
  await expect(step(2)).toHaveAttribute('data-status', 'upcoming')
  await expect(step(3)).toHaveAttribute('data-status', 'upcoming')

  await page.getByRole('tab', { name: 'Browser' }).click()
  await expect(step(1)).toHaveAttribute('data-status', 'active')
  await expect(page.getByRole('radio', { name: /General agent/ })).toHaveAttribute(
    'aria-checked',
    'true',
  )
  await page.getByRole('radio', { name: /Deep researcher/ }).click()
  await page.getByRole('button', { name: 'Customize first' }).click()
  await expect(page.getByRole('textbox', { name: 'Name', exact: true })).toHaveValue(
    'Deep researcher',
  )
  await page.getByRole('button', { name: 'OK' }).click()
  await expect(page.getByText('Customized as Deep researcher')).toBeVisible()
  await page.getByRole('button', { name: 'Create profile' }).click()

  await expect(step(1)).toHaveAttribute('data-status', 'done')
  await expect(step(1).getByRole('link', { name: 'Deep researcher' })).toBeVisible()
  await expect(step(2)).toHaveAttribute('data-status', 'active')
  await expect(page.getByRole('button', { name: 'Create chat' })).toBeVisible()

  await page.getByRole('tab', { name: 'CLI' }).click()
  await expect(step(1)).toHaveAttribute('data-status', 'done')
  await expect(step(2)).toHaveAttribute('data-status', 'done')
  await expect(step(3)).toHaveAttribute('data-status', 'active')
  await expect(step(3)).toContainText('npx omnara agents launch')
  await page.getByRole('tab', { name: 'TypeScript SDK' }).click()
  await expect(step(3)).toContainText('sdk.createAgent(')
  await page.getByRole('tab', { name: 'cURL' }).click()
  await expect(step(3)).toContainText('curl "')
  await page.getByRole('button', { name: 'Run' }).click()

  await expect(step(3)).toHaveAttribute('data-status', 'done')
  await expect(step(3).getByRole('button', { name: 'Run' })).toHaveCount(0)
  const openChat = step(3).getByRole('link', { name: 'Open chat' })
  await expect(openChat).toBeVisible()
  await openChat.click()
  await expect(page).toHaveURL(new RegExp(`/projects/proj_[a-z2-7]+/agents/agt_[a-z2-7]+/chat$`))

  expect(failures).toEqual([])
})

test('memory stores support upload, editing, conflict-safe replacement, and deletion', async ({
  page,
}) => {
  const failures = installFailureTracking(page, [
    /response: (?:403|404|409) .*memory-stores/,
    /^request: .*memory-stores\/mst_[a-z2-7]+(?:\/file\?[^ ]+)? \(net::ERR_ABORTED\)$/,
    /^page: Canceled$/,
  ])
  const memoryPath = `/projects/${projectID}/memory`
  const storeName = `memory-browser-${Date.now()}`
  await signIn(page, adminEmail, memoryPath)
  await expect(page.getByRole('link', { name: 'Memory guide' })).toHaveAttribute(
    'href',
    'https://docs.omnara.com/agents/configuration#memory-stores',
  )
  await page.getByRole('button', { name: 'Create store', exact: true }).click()
  const dialog = page.getByRole('dialog')
  await dialog.getByLabel('Name', { exact: true }).fill(storeName)
  await dialog.getByLabel('Description').fill('Browser verification notes')
  await dialog.getByRole('button', { name: 'Create store', exact: true }).click()
  await page.getByRole('link', { name: storeName, exact: true }).click()
  await expect(page.getByRole('heading', { name: storeName, exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Add file', exact: true }).click()
  await dialog.getByLabel('Path', { exact: true }).fill('notes/plan.txt')
  await dialog.getByLabel('Content', { exact: true }).fill('\ufeffOriginal notes')
  await dialog.getByRole('button', { name: 'Create file', exact: true }).click()
  const editor = page.getByRole('textbox', { name: 'notes/plan.txt', exact: true })
  await expect(editor).toBeVisible()
  await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeDisabled()
  await editor.focus()
  await page.keyboard.press('Control+A')
  await page.keyboard.insertText('Edited notes')
  await expect(page.getByText('Unsaved changes', { exact: false })).toBeVisible()
  page.once('dialog', (prompt) => prompt.dismiss())
  await page.getByRole('link', { name: '← All stores' }).click()
  await expect(editor).toBeVisible()
  const saveBanner = await page.evaluateHandle(() => {
    let seen = false
    const observer = new MutationObserver(() => {
      if (document.body.textContent.includes('This file changed since you opened it')) seen = true
    })
    observer.observe(document.body, { childList: true, subtree: true, characterData: true })
    return {
      finish: () => {
        observer.disconnect()
        return seen
      },
    }
  })
  const saveRequest = page.waitForRequest(
    (request) => request.method() === 'PUT' && /\/memory-stores\/[^/]+\/file\?/.test(request.url()),
  )
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  const fileURL = new URL((await saveRequest).url())
  fileURL.searchParams.delete('expected_digest')
  await expect(page.getByText('Unsaved changes', { exact: false })).toBeHidden()
  const savedFile = await page.request.get(fileURL.toString())
  expect(savedFile.ok()).toBe(true)
  expect(await savedFile.body()).toEqual(Buffer.from('\ufeffEdited notes'))
  expect(await saveBanner.evaluate((state) => state.finish())).toBe(false)
  await saveBanner.dispose()
  await editor.focus()
  await page.keyboard.press('Control+A')
  await page.keyboard.insertText('Unsaved local edits')
  await expect(page.locator('.view-lines')).toContainText('Unsaved local edits')
  await page.getByRole('button', { name: 'Add file', exact: true }).click()
  await dialog.getByRole('tab', { name: 'Upload file', exact: true }).click()
  await dialog.getByLabel('File', { exact: true }).setInputFiles({
    name: 'plan.txt',
    mimeType: 'application/octet-stream',
    buffer: Buffer.from('Replacement notes'),
  })
  const conflictDownloads: string[] = []
  const trackConflictDownload = (request: Request) => {
    if (request.method() === 'GET' && /\/memory-stores\/[^/]+\/file\?/.test(request.url()))
      conflictDownloads.push(request.url())
  }
  page.on('request', trackConflictDownload)
  await dialog.getByRole('button', { name: 'Upload', exact: true }).click()
  await expect(
    dialog.getByText('A file already exists at this path.', { exact: false }),
  ).toBeVisible()
  expect(conflictDownloads).toEqual([])
  page.off('request', trackConflictDownload)
  await dialog.getByRole('button', { name: 'Replace file', exact: true }).click()
  await expect(dialog).toBeHidden()
  const fileRoute = '**/memory-stores/*/file?*'
  let releaseSave!: () => void
  const holdSave = new Promise<void>((resolve) => {
    releaseSave = resolve
  })
  await page.route(
    fileRoute,
    async (route) => {
      await holdSave
      await route.continue()
    },
    { times: 1 },
  )
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(page.getByRole('button', { name: 'Load latest', exact: true })).toBeHidden()
  releaseSave()
  const replace = page.getByRole('button', { name: 'Replace current contents', exact: true })
  await expect(replace).toBeEnabled()
  await expect(page.getByRole('button', { name: 'Load latest', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Check latest', exact: true })).toBeHidden()
  await expect(page.locator('.view-lines')).toContainText('Unsaved local edits')
  await replace.click()
  await expect(page.getByText('Unsaved changes', { exact: false })).toBeHidden()
  await expect(page.getByRole('button', { name: 'Load latest', exact: true })).toBeHidden()
  await editor.focus()
  await page.keyboard.press('Control+A')
  await page.keyboard.insertText('More unsaved local edits')
  await page.route(
    fileRoute,
    async (route) => {
      const remoteWrite = await route.fetch({ postData: 'Replacement notes' })
      expect(remoteWrite.ok()).toBe(true)
      await route.continue()
    },
    { times: 1 },
  )
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  const conflictAlert = page.getByRole('alert').filter({ hasText: 'This file changed.' })
  await expect(conflictAlert).toHaveText('This file changed. Your draft is preserved. Check latest')
  await expect(replace).toBeEnabled()
  await page.route(
    fileRoute,
    (route) =>
      route.fulfill({
        status: 403,
        json: { error: 'File refresh denied', code: 'forbidden' },
      }),
    { times: 1 },
  )
  await page.getByRole('button', { name: 'Check latest', exact: true }).click()
  await expect(page.getByRole('alert').filter({ hasText: 'File refresh denied' })).toBeVisible()
  await expect(page.locator('.view-lines')).toContainText('More unsaved local edits')
  await page.getByRole('button', { name: 'Retry', exact: true }).click()
  await expect(page.getByText('File refresh denied', { exact: true })).toBeHidden()
  await expect(conflictAlert).toBeHidden()
  page.once('dialog', (prompt) => prompt.accept())
  await page.getByRole('button', { name: 'Load latest', exact: true }).click()
  await expect(page.locator('.view-lines')).toContainText('Replacement notes')
  await page.screenshot({ path: test.info().outputPath('memory-store.png') })
  const downloadPromise = page.waitForEvent('download')
  await page.getByRole('button', { name: 'Download', exact: true }).click()
  const download = await downloadPromise
  expect(download.suggestedFilename()).toBe('plan.txt')
  const downloadedPath = await download.path()
  expect(await readFile(downloadedPath, 'utf8')).toBe('Replacement notes')
  await editor.focus()
  await page.keyboard.insertText('Unsaved before deletion')
  await expect(page.getByText('Unsaved changes', { exact: false })).toBeVisible()
  page.once('dialog', (prompt) => prompt.accept())
  await page.getByRole('button', { name: 'Delete', exact: true }).click()
  await expect(page.getByText('This folder is empty.')).toBeVisible()
  await page.getByRole('button', { name: 'Add file', exact: true }).click()
  await dialog.getByRole('tab', { name: 'Upload file', exact: true }).click()
  await dialog.getByLabel('File', { exact: true }).setInputFiles({
    name: 'empty.bin',
    mimeType: 'application/octet-stream',
    buffer: Buffer.alloc(0),
  })
  await page.route(
    fileRoute,
    (route) =>
      route.fulfill({
        status: 409,
        json: { error: 'memory file limit reached', code: 'conflict' },
      }),
    { times: 1 },
  )
  await dialog.getByRole('button', { name: 'Upload', exact: true }).click()
  await expect(dialog.getByRole('alert')).toHaveText('memory file limit reached')
  await expect(dialog.getByRole('button', { name: 'Upload', exact: true })).toBeEnabled()
  await expect(dialog.getByRole('button', { name: 'Replace file', exact: true })).toBeHidden()
  await dialog.getByRole('button', { name: 'Upload', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'empty.bin', exact: true })).toBeVisible()
  let fileDownloads = 0
  page.on('request', (request) => {
    if (request.method() === 'GET' && /\/memory-stores\/[^/]+\/file\?/.test(request.url()))
      fileDownloads++
  })
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  await dialog.getByLabel('Read-only for agents', { exact: true }).check()
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(dialog).toBeHidden()
  await expect(page.getByText('Read-only for agents', { exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Add file', exact: true })).toBeEnabled()
  expect(fileDownloads).toBe(0)

  const storeURL = new URL(page.url()).pathname + new URL(page.url()).search
  await page.context().clearCookies()
  await signIn(page, viewerEmail, storeURL)
  await expect(page.getByRole('heading', { name: 'empty.bin', exact: true })).toBeVisible()
  await expect(page.getByRole('button', { name: 'Settings', exact: true })).toBeHidden()
  await expect(page.getByRole('button', { name: 'Delete store', exact: true })).toBeHidden()
  await signIn(page, adminEmail, createAgentPath)
  await page.getByRole('button', { name: 'Attach memory store', exact: true }).click()
  await page.getByRole('combobox', { name: 'Search memory stores…', exact: true }).click()
  await page.getByPlaceholder('Search memory stores…').fill(storeName)
  await page.getByRole('option', { name: new RegExp(storeName) }).click()
  await expect(page.getByRole('combobox', { name: `Access to ${storeName}` })).toContainText(
    'Read-only',
  )
  await page.getByRole('combobox', { name: `Access to ${storeName}` }).click()
  await page.getByRole('option', { name: 'Read & write', exact: true }).click()
  await expect(page.getByRole('combobox', { name: `Access to ${storeName}` })).toContainText(
    'Read & write',
  )
  await expect(page.getByText('This store is read-only for agents', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: `Detach ${storeName}` }).click()
  await page.goto(storeURL)
  await expect(page.getByRole('button', { name: 'Delete store', exact: true })).toBeHidden()
  await page.getByRole('button', { name: 'Settings', exact: true }).click()
  page.once('dialog', (prompt) => prompt.accept())
  await dialog.getByRole('button', { name: 'Delete store', exact: true }).click()
  await expect(page).toHaveURL(memoryPath)
  expect(failures).toEqual([])
})
