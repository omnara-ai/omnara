import { generateKeyPairSync } from 'node:crypto'

import { schemas } from '@omnara/sdk'
import { type Cookie, expect, type Page, test } from '@playwright/test'
import { z } from 'zod'

import { exerciseDiscordAppSchedule } from './app-schedules'
import { exerciseAppConversations, stopDisconnectedConversation } from './app-subscriptions'
import {
  createAppDraft,
  expectInteractionToolMenu,
  expectSlackAuthorization,
  fillProviderAccount,
  installAppFailureTracking,
  installFailureTracking,
  mockSlackSetupReturn,
  mockVerifiedAppSetup,
  readApp,
  requiredEnvironmentVariable,
} from './fixtures'

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
  // Desktop Chrome uses a Windows user agent, which selects Monaco's Ctrl
  // bindings even on macOS. Paste avoids typing auto-closing extra JSON quotes.
  await page.keyboard.press('Control+A')
  await editor.evaluate((element, value) => {
    const clipboardData = new DataTransfer()
    clipboardData.setData('text/plain', value)
    element.dispatchEvent(
      new ClipboardEvent('paste', { clipboardData, bubbles: true, cancelable: true }),
    )
  }, text)
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

  const agentName = uniqueName('Builder Agent')
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(agentName)
  await page.getByLabel('Instruction').fill('Use the visual Builder to create this test agent.')
  await selectConfiguredModel(page)

  await expectInteractionToolMenu(page)

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
  // Tab changes cancel obsolete profile reads; failed HTTP responses are still recorded.
  const failures = installFailureTracking(page, [
    /^page: Canceled$/,
    /request: .*\/agent-profiles\/aprf_[a-z2-7]+(?:\/config)? \(net::ERR_ABORTED\)$/,
  ])
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
  // Refetch cancellation is expected here; the held save is explicitly completed below.
  const failures = installFailureTracking(page, [
    /^page: Canceled$/,
    /request: .*\/agent-profiles\/aprf_[a-z2-7]+(?:\/config)? \(net::ERR_ABORTED\)$/,
  ])
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

  const agentListLoaded = page.waitForResponse((response) =>
    new URL(response.url()).pathname.endsWith(`/projects/${projectID}/agents`),
  )
  page.once('dialog', (dialog) => void dialog.accept())
  await page.getByRole('button', { name: 'Delete profile' }).click()

  await expect(page).toHaveURL(`/projects/${projectID}/agents`)
  await expect(page.getByRole('heading', { name: 'Agent profiles' })).toBeVisible()
  await expect(page.getByText(profileName)).toHaveCount(0)
  await (await agentListLoaded).finished()

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

  await page.getByRole('link', { name: 'Project apps', exact: true }).click()
  await expect(page).toHaveURL(`/projects/${projectID}/apps`)
  await expect(page.getByRole('heading', { name: 'Apps', exact: true })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Add app', exact: true })).toBeVisible()
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

for (const provider of ['github', 'discord'] as const) {
  test(`saves ${provider} app-owned setup, launcher, tool selections and independent lifecycle`, async ({
    page,
  }) => {
    test.setTimeout(60_000)
    const failures = installAppFailureTracking(page)
    const appName = `${provider}-browser-${test.info().retry}`
    const profileName = uniqueName(`${provider} App Profile`)
    await createProfile(page, profileName, 'Answer in the selected conversation.')
    const profilePath = new URL(page.url()).pathname,
      profileId = schemas.zAgentProfileId.parse(profilePath.split('/').at(-1))
    const { app: draft, apiProjectPath } = await createAppDraft(page, projectID, provider, appName)

    // Only provider verification is replaced. The draft, secret, metadata and
    // agent configs use the real API; the loopback fixture configures this app.
    await mockVerifiedAppSetup(page, apiProjectPath, provider)
    await page.getByRole('button', { name: 'Connect account', exact: true }).click()
    await fillProviderAccount(page, provider)
    if (provider === 'github') {
      const { privateKey } = generateKeyPairSync('rsa', {
        modulusLength: 2048,
        privateKeyEncoding: { type: 'pkcs8', format: 'pem' },
        publicKeyEncoding: { type: 'spki', format: 'pem' },
      })
      await page.getByLabel('RSA private key (PEM)').fill(privateKey)
      await page.getByLabel('Webhook secret').fill('local-github-webhook-secret')
    } else {
      await page.getByLabel('Bot token', { exact: true }).fill('local-discord-token')
      await page.getByLabel('Interaction public key', { exact: true }).fill('ab'.repeat(32))
      await page.getByLabel('Gateway shards', { exact: true }).fill('4')
    }
    const configured = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname.endsWith(`/apps/${draft.id}/setup`),
    )
    await page.getByRole('button', { name: 'Connect app', exact: true }).click()
    expect((await configured).status()).toBe(200)
    const app = await readApp(page, apiProjectPath, draft.id)
    expect(app).toMatchObject({
      id: draft.id,
      name: appName,
      definition_id: `omnara.${provider}`,
      provider_tenant_id: '111',
      provider_account_ref: '222',
      state: 'active',
    })
    const secretID = schemas.zSecretId.parse(app.credential_secret_id)
    expect(JSON.stringify(app)).not.toContain(
      provider === 'github' ? 'PRIVATE KEY' : 'local-discord-token',
    )
    if (provider === 'discord')
      await exerciseDiscordAppSchedule(page, app, profileId, profileName, apiProjectPath)
    await expect(page.getByLabel('App name', { exact: true })).toHaveAttribute('readonly', '')
    const launcher = page.getByRole('checkbox', {
      name:
        provider === 'github' ? 'Launch agents from GitHub events' : 'Launch agents from mentions',
      exact: true,
    })
    await expect(launcher).not.toBeChecked()
    await launcher.check()
    await page
      .getByLabel(provider === 'github' ? 'Repository ID' : 'Channel ID', { exact: true })
      .fill('333')
    if (provider === 'github')
      await page.getByRole('combobox', { name: 'Offered profiles', exact: true }).click()
    await page
      .getByPlaceholder(
        provider === 'github' ? 'Choose an agent profile…' : 'Search agent profiles…',
      )
      .fill(profileName)
    await page.getByRole('option', { name: profileName, exact: true }).click()
    if (provider === 'discord')
      await expect(
        page.getByRole('button', { name: `Remove ${profileName}`, exact: true }),
      ).toBeVisible()
    // Escape clears a combobox selection once its popup has closed. Move focus
    // to the launcher toggle so closing the chooser preserves the selection.
    await launcher.focus()
    await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeEnabled()
    const savedLauncher = page.waitForResponse(
      (response) =>
        response.request().method() === 'PUT' &&
        new URL(response.url()).pathname.endsWith(`/apps/${app.id}`),
    )
    await page.getByRole('button', { name: 'Save changes', exact: true }).click()
    const launched = schemas.zProjectApp.parse(await (await savedLauncher).json())
    expect(launched.settings.launcher?.slots).toEqual([
      { key: 'default', agent_profile_id: profileId },
    ])
    expect(launched.setup_revision).toBe(app.setup_revision)
    await page.reload()
    await expect(page.getByRole('heading', { name: appName, exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Edit settings', exact: true }).click()
    await expect(page.getByLabel('App name', { exact: true })).toHaveAttribute('readonly', '')
    if (provider === 'github') await page.getByLabel('Launch when').selectOption('mention')
    else await page.getByLabel('Channel ID', { exact: true }).fill('334')
    const updated = page.waitForResponse(
      (response) =>
        response.request().method() === 'PUT' &&
        new URL(response.url()).pathname.endsWith(`/apps/${app.id}`),
    )
    await page.getByRole('button', { name: 'Save changes', exact: true }).click()
    const changedApp = schemas.zProjectApp.parse(await (await updated).json())
    expect(changedApp.name).toBe(appName)
    expect(changedApp.settings.launcher?.slots).toEqual(launched.settings.launcher?.slots)
    expect(changedApp.setup_revision).toBe(app.setup_revision)
    if (provider === 'github') {
      expect(changedApp.settings.launcher?.trigger).toBe('mention')
      await expect(page.getByText(/GitHub webhook URL:/)).toContainText(
        '/api/integrations/github/111/events',
      )
    } else expect(changedApp.settings.launcher?.scope_ref).toBe('334')

    await page.goto(profilePath)
    await page.getByRole('button', { name: 'YAML', exact: true }).click()
    const selectedTool = `app__${app.name}__read`
    const selectedHandlers = provider === 'discord' ? { [app.name]: {} } : {}
    await replaceConfigEditor(
      page,
      JSON.stringify({
        instruction: 'Read this conversation.',
        model: { provider_config: providerConfig, name: modelName },
        tools: { [selectedTool]: {} },
        interaction_handlers: selectedHandlers,
      }),
    )
    const preview = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname.endsWith('/agent-configs/tools'),
    )
    await page.getByRole('button', { name: 'Builder', exact: true }).click()
    const previewResponse = await preview
    expect(previewResponse.status()).toBe(200)
    const previewRequest = schemas.zResolveAgentConfigToolsRequest.parse(
      previewResponse.request().postDataJSON(),
    )
    expect(JSON.parse(previewRequest.source)).toMatchObject({
      tools: { [selectedTool]: {} },
      interaction_handlers: selectedHandlers,
    })
    const tools = schemas.zResolvedAgentConfigTools.parse(await previewResponse.json()).tools
    expect(tools).toContainEqual(expect.objectContaining({ name: selectedTool, enabled: true }))
    const excludedTool = provider === 'github' ? 'discussion_comment' : 'post_message'
    expect(tools.map((tool) => tool.name)).not.toContain(`app__${app.name}__${excludedTool}`)
    await page.getByRole('button', { name: 'Other tools', exact: true }).click()
    await expect(page.getByText(selectedTool, { exact: true }).first()).toBeVisible()
    const savedRevision = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname.endsWith(`/agent-profiles/${profileId}/config`),
    )
    await page.getByRole('button', { name: 'Save revision', exact: true }).click()
    expect((await savedRevision).status()).toBe(200)
    await expect(page.getByRole('button', { name: 'Save revision', exact: true })).toBeDisabled()

    const conversation = await exerciseAppConversations(page, app, profileId, apiProjectPath)

    // A second app may select the same secret. Its lifecycle stays independent.
    const { app: secondary } = await createAppDraft(
      page,
      projectID,
      provider,
      `${provider}-secondary-${test.info().retry}`,
    )
    await page.getByRole('button', { name: 'Connect account', exact: true }).click()
    await fillProviderAccount(page, provider)
    await page.getByRole('checkbox', { name: 'Create a new credential' }).uncheck()
    await page.getByLabel('Saved credential', { exact: true }).selectOption(secretID)
    if (provider === 'discord')
      await page.getByLabel('Interaction public key', { exact: true }).fill('ab'.repeat(32))
    await page.getByRole('button', { name: 'Connect app', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    expect(await readApp(page, apiProjectPath, secondary.id)).toMatchObject({
      state: 'active',
      credential_secret_id: secretID,
      settings: {},
    })
    await page.goto(`/projects/${projectID}/apps/${app.id}`)
    page.once('dialog', (dialog) => void dialog.accept())
    const disconnected = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname.endsWith(`/apps/${app.id}/disconnect`),
    )
    await page.getByRole('button', { name: 'Disconnect app', exact: true }).click()
    const offline = schemas.zProjectApp.parse(await (await disconnected).json())
    expect(offline.state).toBe('disconnected')
    expect(offline.setup_revision).toBeGreaterThan(app.setup_revision)
    expect(offline.settings).toEqual(changedApp.settings)
    expect((await readApp(page, apiProjectPath, secondary.id)).state).toBe('active')
    await stopDisconnectedConversation(page, app, conversation, apiProjectPath)
    await page.getByRole('button', { name: 'Reconnect account', exact: true }).click()
    await expect(
      page.getByLabel(provider === 'github' ? 'GitHub App ID' : 'Discord Application ID', {
        exact: true,
      }),
    ).toHaveAttribute('readonly', '')
    await page.getByRole('checkbox', { name: 'Create a new credential' }).uncheck()
    await page.getByLabel('Saved credential', { exact: true }).selectOption(secretID)
    await page.getByRole('button', { name: 'Reconnect app', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Save changes', exact: true })).toBeVisible()
    await page.getByRole('button', { name: 'Cancel', exact: true }).click()
    const reconnected = await readApp(page, apiProjectPath, app.id)
    expect(reconnected).toMatchObject({
      state: 'active',
      settings: changedApp.settings,
      id: app.id,
    })
    expect(reconnected.setup_revision).toBeGreaterThan(offline.setup_revision)
    page.once('dialog', (dialog) => void dialog.accept())
    await page.getByRole('button', { name: 'Remove app', exact: true }).click()
    await expect(page).toHaveURL(`/projects/${projectID}/apps`)
    await expect(page.getByRole('link').filter({ hasText: appName })).toHaveCount(0)
    expect((await readApp(page, apiProjectPath, secondary.id)).state).toBe('active')
    expect(failures).toEqual([])
  })
}

test('connects a saved Slack app and waits for its exact OAuth flow without contacting Slack', async ({
  page,
  context,
}) => {
  const failures = installFailureTracking(page, [
    /request: .*\/apps\/app_[a-z2-7]+ \(net::ERR_ABORTED\)$/,
  ])
  await signIn(page, adminEmail, `/projects/${projectID}/apps`)
  const { app, apiProjectPath } = await createAppDraft(
    page,
    projectID,
    'slack',
    `slack-browser-${test.info().retry}`,
  )
  const appPath = new URL(page.url()).pathname
  const browserOrigin = new URL(page.url()).origin
  await page.getByRole('button', { name: 'Connect account', exact: true }).click()
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
      new URL(response.url()).pathname.endsWith(`/apps/${app.id}/oauth/setup`) &&
      response.request().method() === 'POST',
  )
  await page.getByRole('button', { name: 'Continue', exact: true }).click()
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
  await expect(page).toHaveURL(appPath)

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
  await expect(page.getByRole('dialog')).toHaveCount(0)
  await expect(page.getByLabel('App name', { exact: true })).toHaveValue(app.name)
  await expect(
    page.getByRole('checkbox', { name: 'Launch agents from mentions', exact: true }),
  ).not.toBeChecked()
  await page.getByRole('button', { name: 'Save changes', exact: true }).click()
  await expect(page.getByRole('heading', { name: app.name, exact: true })).toBeVisible()
  await page.goto(`${browserOrigin}${appPath}?integration_oauth=success&app_id=${app.id}`)
  await expect(page.getByRole('dialog')).toContainText('Slack app connected')
  await page.getByRole('button', { name: 'Got it', exact: true }).click()
  await expect(page).toHaveURL(appPath)
  await expect(page.getByRole('heading', { name: app.name, exact: true })).toBeVisible()
  expect(failures).toEqual([])
})

test('project viewers can browse apps but cannot open app setup', async ({ page }) => {
  const failures = installFailureTracking(page)
  await signIn(page, viewerEmail, `/projects/${projectID}/apps`)
  await expect(page.getByRole('heading', { name: 'Apps', exact: true })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Add app', exact: true })).toHaveCount(0)
  await page.goto(`/projects/${projectID}/apps/new/github`)
  await expect(page.getByRole('alert')).toContainText('You don’t have permission to manage apps')
  await expect(page.getByRole('button', { name: 'Create app', exact: true })).toHaveCount(0)
  expect(failures).toEqual([])
})
