import { schemas } from '@omnara/sdk'
import { expect, type Page, test } from '@playwright/test'
import { parse, stringify } from 'yaml'
import { z } from 'zod'

import { exerciseProjectWebhooks } from './event-webhooks'
import {
  connectIntegrationWithCredentialRetry,
  expectIntegrationCapabilities,
  expectInteractionToolMenu,
  fillProviderAccount,
  installFailureTracking,
  installIntegrationFailureTracking,
  integrationCreation,
  openIntegrationSetup,
  openIntegrationTab,
  readIntegration,
  requiredEnvironmentVariable,
} from './fixtures'
import {
  expectClearOfAssistantLauncher,
  replaceConfigEditor,
  signIn,
  typeInConfigEditor,
} from './helpers'
import { exerciseGuidedGitHubSetup } from './integration-github'
import { exerciseDiscordIntegrationSchedule } from './integration-schedules'
import { exerciseSlackIntegrationSetup } from './integration-slack'
import {
  exerciseIntegrationConversations,
  stopDisconnectedConversation,
} from './integration-subscriptions'

const projectID = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROJECT_ID')
const modelGrantRequest = z.object({ configured_model_id: z.string() })
const adminEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_ADMIN_EMAIL')
const viewerEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_VIEWER_EMAIL')
const onboardingEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_ONBOARDING_EMAIL')
const providerConfig = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROVIDER_CONFIG')
const modelName = requiredEnvironmentVariable('OMNARA_WEB_E2E_MODEL_NAME')
const ungrantedModelName = requiredEnvironmentVariable('OMNARA_WEB_E2E_UNGRANTED_MODEL')
const createAgentPath = `/projects/${projectID}/agents/new`

async function selectConfiguredModel(page: Page) {
  const modelPicker = page.getByRole('combobox', { name: 'Model', exact: true })
  await modelPicker.click()
  await page.getByPlaceholder('Search shared models…').fill(modelName)
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
  const created = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname.endsWith('/agent-profiles'),
  )
  await page.getByRole('button', { name: 'Create profile' }).click()
  await expect(page).toHaveURL(
    new RegExp(`/projects/${projectID}/agent-profiles/aprf_[a-z2-7]+/configuration$`),
  )
  return schemas.zAgentProfile.parse(await (await created).json())
}

function uniqueName(base: string) {
  const retry = test.info().retry
  return retry === 0 ? base : `${base} (retry ${retry})`
}

async function visitAgentsTabAndReturn(page: Page) {
  await page.getByRole('button', { name: 'Agents', exact: true }).click()
  await expect(page.getByText('No agents from this profile yet', { exact: false })).toBeVisible()
  await page.getByRole('button', { name: 'Configuration' }).click()
}

for (const { format, ...viewport } of [
  { format: 'YAML', name: 'desktop', width: 1280, height: 720 },
  { format: 'YAML', name: 'mobile', width: 390, height: 844 },
  { format: 'JSON', name: 'desktop', width: 1280, height: 720 },
]) {
  test(`creates an agent from ${format} text on ${viewport.name}`, async ({ page }) => {
    await page.setViewportSize(viewport)
    const failures = installFailureTracking(page)
    await signIn(page, adminEmail, createAgentPath)

    await expect(page.getByRole('button', { name: 'Create & launch agent' })).toBeVisible()
    await expect(page.getByRole('button', { name: 'YAML' })).toBeVisible()

    if (viewport.name === 'mobile') {
      await page.getByLabel('Instruction').fill('')
      const submit = page.getByRole('button', { name: 'Create & launch agent' })
      await submit.click()
      await expect(page.getByText('Instructions are required.', { exact: true })).toBeVisible()
      await expectClearOfAssistantLauncher(page, submit, [390])
    }

    await page.getByRole('button', { name: 'YAML' }).click()
    await expect(page.getByRole('textbox', { name: 'Config (YAML)' })).toHaveAttribute(
      'aria-required',
      'true',
    )

    const agentName = uniqueName(`${format} E2E Agent ${viewport.name}`)
    await page.getByRole('textbox', { name: 'Name', exact: true }).fill(agentName)
    const definition = {
      instruction: 'Test agent creation from source.',
      model: { provider_config: providerConfig, name: modelName },
      tools: {
        lookup_item: {
          type: 'custom',
          description: 'Look up an example item.',
          input_schema: { type: 'object' },
        },
      },
    }
    await replaceConfigEditor(
      page,
      format === 'YAML' ? stringify(definition) : JSON.stringify(definition, null, 2),
    )
    await page.getByRole('button', { name: 'Builder', exact: true }).click()
    await expect(page.getByRole('button', { name: 'Edit lookup_item' })).toBeVisible()
    await page.getByRole('button', { name: 'YAML', exact: true }).click()
    await expect(page.getByRole('textbox', { name: 'Config (YAML)' })).toBeVisible()
    await page.getByRole('button', { name: 'Create & launch agent' }).click()

    await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agents/agt_[a-z2-7]+/events$`))
    await expect(page.locator('[data-slot="breadcrumb"] [aria-current="page"]')).toHaveText(
      agentName,
    )
    expect(failures).toEqual([])
  })
}

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
  const modelSearch = page.getByPlaceholder('Search shared models…')
  await expect(modelSearch).toHaveValue('m')
  await modelSearch.fill(modelName)
  await modelSearch.clear()
  await expect(modelSearch).toHaveValue('')
  await expect(modelPicker).toContainText(modelName)
  await modelSearch.press('Escape')

  await page.getByRole('button', { name: `Clear ${modelName} · ${providerConfig}` }).click()
  await expect(modelPicker).not.toContainText(modelName)
  await page.getByRole('button', { name: 'Create & launch agent' }).click()
  await expect(page.getByText('Choose a model.', { exact: true })).toBeVisible()
  await selectConfiguredModel(page)

  await page.getByRole('button', { name: 'Create & launch agent' }).click()

  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agents/agt_[a-z2-7]+/events$`))
  await expect(page.locator('[data-slot="breadcrumb"] [aria-current="page"]')).toHaveText(agentName)
  expect(failures).toEqual([])
})

test('creates a profile without launching an agent', async ({ page }) => {
  const failures = installFailureTracking(page)
  const profileName = uniqueName('Profile Only Agent')
  await createProfile(page, profileName, 'Save this config as a profile without launching.')
  await expect(page.getByText(profileName).first()).toBeVisible()
  expect(failures).toEqual([])
})

test('manages every webhook selection from the project Webhooks tab', async ({ page }) => {
  test.setTimeout(60_000)
  const failures = installFailureTracking(page)
  const profile = await createProfile(
    page,
    uniqueName('Webhook Browser Profile'),
    'Exercise webhook subscription management.',
  )
  await exerciseProjectWebhooks(page, profile)
  expect(failures).toEqual([])
})

test('sharing a model from the Builder does not create a profile or agent', async ({ page }) => {
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
  await expect(page.getByPlaceholder('Search shared models…')).toHaveValue('')
  const grantModelsAction = page.getByRole('button', { name: 'Share models…', exact: true })
  await expect(grantModelsAction).toBeVisible()
  const providerListResponse = page.waitForResponse((response) => {
    const url = new URL(response.url())
    return response.request().method() === 'GET' && url.pathname.endsWith('/model-provider-configs')
  })
  await grantModelsAction.press('Enter')
  const dialog = page.getByRole('dialog', { name: 'Share models' })
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
  await expect(dialog.getByRole('button', { name: 'Share models', exact: true })).toBeDisabled()
  await providerPicker.click()
  await providerSearch.fill(providerConfig)
  await page.getByRole('option', { name: providerConfig }).click()
  await configuredModelPicker.fill(ungrantedModelName)
  await page.getByRole('option', { name: ungrantedModelName }).click()
  await dialog.getByRole('button', { name: 'Share models' }).click()

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
  await expect(page).toHaveURL(
    new RegExp(`/projects/${projectID}/agent-profiles/aprf_[a-z2-7]+/configuration$`),
  )

  await page.getByRole('button', { name: 'Discard changes' }).click()
  await expect(page.getByText('# draft edit')).toHaveCount(0)
  await page.getByRole('button', { name: 'Save revision' }).click()
  await expect(page.getByText('No changes to save.', { exact: true })).toBeVisible()

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

  await expect(saveButton).toBeEnabled()
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
    /^response: 404 .*\/agent-profiles\/aprf_[a-z2-7]+$/,
  ])
  const profileName = uniqueName('Deleted Profile E2E')
  await createProfile(page, profileName, 'Delete this profile from its detail page.')
  await page.getByLabel('Instruction').fill('Edited before deletion.')
  for (const name of ['Discard changes', 'Delete profile', 'Save revision']) {
    const action = page.getByRole('button', { name, exact: true })
    await expectClearOfAssistantLauncher(page, action, [390, 768])
  }

  page.once('dialog', (dialog) => void dialog.accept())
  const profilesListed = page.waitForResponse(
    (response) =>
      response.request().method() === 'GET' &&
      new URL(response.url()).pathname.endsWith(`/projects/${projectID}/agent-profiles`) &&
      response.ok(),
  )
  await page.getByRole('button', { name: 'Delete profile' }).click()

  await expect(page).toHaveURL(`/projects/${projectID}/agents`)
  await expect(page.getByRole('heading', { name: 'Agent profiles' })).toBeVisible()
  await expect(page.getByText(profileName)).toHaveCount(0)
  await (await profilesListed).finished()

  await page.goBack()
  await expect(page.getByRole('heading', { name: 'Something went wrong' })).toBeVisible()
  await expect(page.getByText(/^404: /)).toBeVisible()
  await expect(page.getByText(profileName)).toHaveCount(0)
  expect(failures).toEqual([])
})

test('edits a profile with the Builder', async ({ page }) => {
  const failures = installFailureTracking(page)
  const helperName = uniqueName('Builder Edit Helper')
  await createProfile(page, helperName, 'Help the parent agent.')
  await createProfile(page, uniqueName('Builder Edit Agent'), 'Original instruction.')

  await expect(page.getByRole('combobox', { name: 'Model', exact: true })).toBeVisible()
  const instruction = page.getByLabel('Instruction')
  await expect(instruction).toHaveValue('Original instruction.')

  await instruction.fill('Updated instruction.')
  await page.getByRole('button', { name: 'Add subagent' }).click()
  await page.getByRole('textbox', { name: 'Subagent name' }).fill('helper')
  await page.getByRole('button', { name: 'Save revision' }).click()
  await expect(page.getByText('Subagent “helper”: Choose an agent profile.')).toBeVisible()
  await page.getByRole('combobox').filter({ hasText: 'Search agent profiles…' }).click()
  const profileResults = page.waitForResponse(
    (response) => new URL(response.url()).searchParams.get('name') === `*${helperName}*`,
  )
  await page.getByPlaceholder('Search agent profiles…').fill(helperName)
  await profileResults
  await expect(page.getByRole('option', { name: helperName })).toBeVisible()
  await page.getByPlaceholder('Search agent profiles…').press('Enter')
  await expect(page.getByText('Subagent “helper”: Choose an agent profile.')).toHaveCount(0)
  await page.getByRole('button', { name: 'Save revision' }).click()
  await expect(page.getByText('Version 2', { exact: true })).toBeVisible()

  await page.getByRole('button', { name: 'YAML' }).click()
  await expect(page.locator('.monaco-editor')).toContainText('Updated instruction.')
  await expect(page.locator('.monaco-editor')).toContainText(`profile: ${helperName}`)

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

test('sets up an assistant from an empty organization, talks to it on the overview and finds it in the corner elsewhere', async ({
  page,
}) => {
  const failures = installFailureTracking(page)
  await signIn(page, onboardingEmail, '/')
  const assistant = page.getByRole('region', { name: 'Assistant', exact: true })
  const launcher = page.getByRole('button', { name: 'Assistant', exact: true })
  const setup = page.getByRole('heading', { name: 'Set up your assistant' })
  // A retry finds the assistant the first attempt created.
  await expect(setup.or(assistant)).toBeVisible()
  await expect(launcher).toHaveCount(0)
  if (await setup.isVisible()) {
    await page.getByRole('button', { name: 'Create assistant', exact: true }).click()
  }
  await expect(assistant.getByText('Omnara assistant', { exact: true })).toBeVisible()
  await expect(launcher).toHaveCount(0)
  await expect(page.getByText('No profiles yet', { exact: true })).toBeVisible()

  const question = 'Which models can my agents use?'
  const sent = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      /\/agents\/agt_[a-z2-7]+\/inputs$/.test(new URL(response.url()).pathname),
  )
  const composer = assistant.getByRole('textbox')
  await composer.fill(question)
  await composer.press('Enter')
  const accepted = await sent
  expect(accepted.ok()).toBe(true)
  const { agent_input: input } = schemas.zCreateAgentInputResponse.parse(await accepted.json())
  const blocks = input.content_blocks ?? []
  expect(blocks.some((block) => block.type === 'text' && block.text === question)).toBe(true)
  const context = blocks.find(
    (block) => block.type === 'text' && block.metadata?.omnara_hidden === 'true',
  )
  if (context?.type !== 'text') throw new Error('Missing page context')
  expect(context.text).toContain('"route":"/"')
  expect(context.text).toContain('## Usage')
  expect(context.text).toContain('No profiles yet')
  expect(context.text).not.toContain(question)
  await expect(assistant.getByText(question, { exact: true })).toBeVisible()

  // The same conversation, with an unsent draft, follows to other pages in the corner.
  await composer.fill('Still thinking')
  await page.getByRole('link', { name: 'Members', exact: true }).click()
  await expect(page).toHaveURL('/members')
  await launcher.click()
  const panel = page.getByRole('dialog', { name: 'Assistant', exact: true })
  await expect(panel.getByText(question, { exact: true })).toBeVisible()
  await expect(panel.getByRole('textbox')).toHaveValue('Still thinking')

  expect(failures).toEqual([])
})

for (const integrationKind of ['github_pr', 'discord_thread'] as const) {
  test(`saves ${integrationKind} integration-owned setup, launcher, tool selections and independent lifecycle`, async ({
    page,
  }) => {
    test.setTimeout(60_000)
    const failures = installIntegrationFailureTracking(page)
    const integrationName = `${integrationKind.replaceAll('_', '-')}-browser-${test.info().retry}`
    const profileName = uniqueName(`${integrationKind} Integration Profile`)
    const profile = await createProfile(page, profileName, 'Answer in the selected conversation.')
    const profilePath = new URL(page.url()).pathname,
      profileId = profile.id
    const { integration, apiProjectPath } = await connectIntegrationWithCredentialRetry(
      page,
      projectID,
      integrationKind,
      integrationName,
      failures,
    )
    const integrationPath = `/projects/${projectID}/integrations/${integration.id}`
    const launch = page.getByRole('region', {
      name: integrationKind === 'github_pr' ? 'Pull requests' : 'Mentions',
      exact: true,
    })

    const secretID = schemas.zSecretId.parse(integration.credential_secret_id)
    if (integrationKind === 'discord_thread') {
      await expect(page.getByLabel('Interactions Endpoint URL', { exact: true })).toHaveValue(
        new RegExp(`/api/integrations/discord/${integration.provider_tenant_id}/interactions$`),
      )
      await expect(page.getByRole('link', { name: 'Add bot to server', exact: true })).toBeVisible()
      await launch.getByRole('button', { name: 'Skip for now', exact: true }).click()
      await openIntegrationTab(page, 'Advanced')
      await expect(page.getByRole('region', { name: 'Advanced', exact: true })).toContainText(
        `/api/integrations/discord/${integration.provider_tenant_id}/interactions`,
      )
      await openIntegrationTab(page, 'Mentions')
      await exerciseDiscordIntegrationSchedule(
        page,
        integration,
        profileId,
        profileName,
        apiProjectPath,
      )
    }
    const save = launch.getByRole('button', { name: 'Save changes', exact: true })
    await expect(save).toBeDisabled()
    if (integrationKind === 'github_pr') {
      await expect(launch.getByRole('checkbox', { name: 'PR opened', exact: true })).toBeChecked()
      await expect(
        launch.getByRole('checkbox', { name: 'Bot mentioned', exact: true }),
      ).toBeChecked()
      await launch.getByRole('combobox', { name: 'Agent profile', exact: true }).click()
    } else {
      await expect(launch.getByRole('checkbox')).toHaveCount(0)
      await expect(launch.getByLabel('Channel ID', { exact: true })).toHaveCount(0)
      await expect(launch.getByLabel('Respond to mentions in', { exact: true })).toHaveCount(0)
    }
    await page
      .getByPlaceholder(
        integrationKind === 'github_pr' ? 'Choose an agent profile…' : 'Search agent profiles…',
      )
      .fill(profileName)
    await page.getByRole('option', { name: profileName, exact: true }).click()
    if (integrationKind === 'discord_thread') {
      await page.keyboard.press('Escape')
      await expect(
        page.getByRole('button', { name: `Remove ${profileName}`, exact: true }),
      ).toBeVisible()
    }
    await expect(save).toBeEnabled()
    const savedLauncher = page.waitForResponse(
      (response) =>
        response.request().method() === 'PUT' &&
        new URL(response.url()).pathname.endsWith(`/integrations/${integration.id}`),
    )
    await save.click()
    const launched = schemas.zIntegration.parse(await (await savedLauncher).json())
    expect(launched.settings.launcher).toEqual(
      integrationKind === 'github_pr'
        ? { profile: profileId, trigger: 'both' }
        : { profiles: [profileId] },
    )
    expect(launched.setup_revision).toBe(integration.setup_revision)
    await expect(page).toHaveURL(integrationPath)
    const selectedProfile =
      integrationKind === 'github_pr'
        ? launch.getByRole('combobox', { name: 'Agent profile', exact: true })
        : launch.getByRole('button', { name: `Remove ${profileName}`, exact: true })
    await expect(selectedProfile).toBeVisible()
    if (integrationKind === 'github_pr') await expect(selectedProfile).toContainText(profileName)
    await expect(save).toBeDisabled()
    await page.reload()
    await expect(page.getByRole('heading', { name: integrationName, exact: true })).toBeVisible()
    await expect(selectedProfile).toBeVisible()
    if (integrationKind === 'github_pr') await expect(selectedProfile).toContainText(profileName)
    await expect(save).toBeDisabled()
    await expect(launch.getByLabel('Integration name', { exact: true })).toHaveCount(0)
    let changedIntegration = launched
    if (integrationKind === 'github_pr') {
      await launch.getByRole('checkbox', { name: 'PR opened', exact: true }).uncheck()
      const updated = page.waitForResponse(
        (response) =>
          response.request().method() === 'PUT' &&
          new URL(response.url()).pathname.endsWith(`/integrations/${integration.id}`),
      )
      await expect(save).toBeEnabled()
      await save.click()
      changedIntegration = schemas.zIntegration.parse(await (await updated).json())
    } else {
      await selectedProfile.click()
      await page.keyboard.press('Escape')
      await expect(save).toBeEnabled()
      await launch.getByRole('button', { name: 'Discard changes', exact: true }).click()
      await expect(selectedProfile).toBeVisible()
    }
    await expect(save).toBeDisabled()
    expect(changedIntegration.name).toBe(integrationName)
    expect(changedIntegration.settings.launcher).toEqual(
      integrationKind === 'github_pr'
        ? { profile: profileId, trigger: 'mention' }
        : launched.settings.launcher,
    )
    expect(changedIntegration.setup_revision).toBe(integration.setup_revision)
    await expectIntegrationCapabilities(page, changedIntegration)
    if (integrationKind === 'github_pr') {
      await expect(page.getByRole('region', { name: 'Advanced', exact: true })).toContainText(
        '/api/integrations/github/events',
      )
    } else {
      await openIntegrationTab(page, 'Mentions')
      await expect(launch).toContainText('without an Omnara account')
    }

    await page.goto(profilePath)
    await page.getByRole('button', { name: 'YAML', exact: true }).click()
    const selectedTool = `int__${integration.name}__read`
    const selectedHandlers = integrationKind === 'discord_thread' ? { [integration.name]: {} } : {}
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
    expect(previewRequest.source_format).toBe('yaml')
    expect(parse(z.string().parse(previewRequest.source))).toMatchObject({
      tools: { [selectedTool]: {} },
      interaction_handlers: selectedHandlers,
    })
    const tools = schemas.zResolvedAgentConfigTools.parse(await previewResponse.json()).tools
    expect(tools).toContainEqual(expect.objectContaining({ name: selectedTool, enabled: true }))
    const excludedTool = integrationKind === 'github_pr' ? 'discussion_comment' : 'post_message'
    expect(tools.map((tool) => tool.name)).not.toContain(
      `int__${integration.name}__${excludedTool}`,
    )
    await page.getByRole('button', { name: 'Built-in tools', exact: true }).click()
    await expect(page.getByText(selectedTool, { exact: true }).first()).toBeVisible()
    const savedRevision = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname.endsWith(`/agent-profiles/${profileId}/config`),
    )
    await page.getByRole('button', { name: 'Save revision', exact: true }).click()
    expect((await savedRevision).status()).toBe(200)

    const conversation = await exerciseIntegrationConversations(
      page,
      integration,
      profileId,
      apiProjectPath,
    )

    await openIntegrationSetup(page, projectID, integrationKind, `${integrationName}-2`)
    await fillProviderAccount(page, integrationKind)
    await page.getByRole('checkbox', { name: 'Create a new credential' }).uncheck()
    await page.getByRole('combobox', { name: 'Saved credential', exact: true }).click()
    await page.getByRole('option', { name: `${integrationName}-credentials`, exact: true }).click()
    if (integrationKind === 'discord_thread')
      await page.getByLabel('Public key', { exact: true }).fill('ab'.repeat(32))
    const secondCreation = integrationCreation(page)
    await page.getByRole('button', { name: 'Create and connect', exact: true }).click()
    const secondary = schemas.zIntegration.parse(await (await secondCreation).json())
    await expect(launch.getByRole('button', { name: 'Save changes', exact: true })).toBeVisible()
    await launch.getByRole('button', { name: 'Skip for now', exact: true }).click()
    expect(await readIntegration(page, apiProjectPath, secondary.id)).toMatchObject({
      state: 'active',
      credential_secret_id: secretID,
      settings: {},
    })
    await page.goto(`/projects/${projectID}/integrations/${integration.id}`)
    await page.getByRole('button', { name: 'Integration actions', exact: true }).click()
    await page.getByRole('menuitem', { name: 'Reconnect account', exact: true }).click()
    const connection = page.getByRole('region', { name: 'Connection', exact: true })
    await expect(connection).toBeVisible()
    await expect(page.getByRole('dialog')).toHaveCount(0)
    await connection.getByRole('button', { name: 'Cancel', exact: true }).click()
    page.once('dialog', (dialog) => void dialog.accept())
    const disconnected = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname.endsWith(`/integrations/${integration.id}/disconnect`),
    )
    await page.getByRole('button', { name: 'Integration actions', exact: true }).click()
    await page.getByRole('menuitem', { name: 'Disconnect integration', exact: true }).click()
    const offline = schemas.zIntegration.parse(await (await disconnected).json())
    expect(offline.state).toBe('disconnected')
    expect(offline.setup_revision).toBeGreaterThan(integration.setup_revision)
    expect(offline.settings).toEqual(changedIntegration.settings)
    expect((await readIntegration(page, apiProjectPath, secondary.id)).state).toBe('active')
    await stopDisconnectedConversation(page, integration, conversation, apiProjectPath)
    await page.getByRole('button', { name: 'Reconnect account', exact: true }).click()
    await expect(
      page.getByLabel(
        integrationKind === 'github_pr' ? 'GitHub App ID' : 'Discord Application ID',
        {
          exact: true,
        },
      ),
    ).toHaveAttribute('readonly', '')
    await page.getByRole('checkbox', { name: 'Create a new credential' }).uncheck()
    await page.getByRole('combobox', { name: 'Saved credential', exact: true }).click()
    await page.getByRole('option', { name: `${integrationName}-credentials`, exact: true }).click()
    const reconfigured = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname.endsWith(`/integrations/${integration.id}/setup`),
    )
    await connection.getByRole('button', { name: 'Reconnect integration', exact: true }).click()
    expect((await reconfigured).status()).toBe(200)
    await expect(connection).toHaveCount(0)
    if (integrationKind === 'discord_thread') {
      await expect(page.getByLabel('Interactions Endpoint URL', { exact: true })).toHaveValue(
        new RegExp(`/api/integrations/discord/${integration.provider_tenant_id}/interactions$`),
      )
      await expect(page.getByRole('link', { name: 'Add bot to server', exact: true })).toBeVisible()
    }
    await openIntegrationTab(page, integrationKind === 'github_pr' ? 'Pull requests' : 'Mentions')
    await expect(selectedProfile).toBeVisible()
    if (integrationKind === 'github_pr') await expect(selectedProfile).toContainText(profileName)
    const reconnected = await readIntegration(page, apiProjectPath, integration.id)
    expect(reconnected).toMatchObject({
      state: 'active',
      settings: changedIntegration.settings,
      id: integration.id,
    })
    expect(reconnected.setup_revision).toBeGreaterThan(offline.setup_revision)
    page.once('dialog', (dialog) => void dialog.accept())
    await openIntegrationTab(page, 'Advanced')
    await page.getByRole('button', { name: 'Delete integration', exact: true }).click()
    await expect(page).toHaveURL(`/projects/${projectID}/integrations`)
    await expect(page.locator(`a[href="${integrationPath}"]`)).toHaveCount(0)
    expect((await readIntegration(page, apiProjectPath, secondary.id)).state).toBe('active')
    expect(failures).toEqual([])
  })
}

test('creates and connects Slack through a same-tab authorization return', async ({
  page,
  context,
}) => {
  test.setTimeout(60_000)
  const profile = await createProfile(
    page,
    uniqueName('Slack Integration Profile'),
    'Answer in the selected conversation.',
  )
  await exerciseSlackIntegrationSetup(
    page,
    context,
    projectID,
    `slack-browser-${test.info().retry}`,
    profile.id,
    profile.name,
  )
})

test('project viewers can browse integrations but cannot open integration setup', async ({
  page,
}) => {
  const failures = installFailureTracking(page)
  await signIn(page, viewerEmail, `/projects/${projectID}/integrations`)
  await expect(page.getByRole('heading', { name: 'Integrations', exact: true })).toBeVisible()
  await expect(page.getByRole('link', { name: 'Add integration', exact: true })).toHaveCount(0)
  await page.goto(`/projects/${projectID}/integrations/new/github_pr`)
  await expect(page.getByRole('alert')).toContainText(
    'You don’t have permission to manage integrations',
  )
  await expect(page.getByRole('button', { name: 'Create and connect', exact: true })).toHaveCount(0)
  expect(failures).toEqual([])
})

test('guides customer-owned GitHub registration and installation without live provider calls', async ({
  page,
}) => {
  test.setTimeout(60_000)
  await signIn(page, adminEmail, `/projects/${projectID}/integrations`)
  await exerciseGuidedGitHubSetup(page, projectID)
})
