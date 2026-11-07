import { schemas } from '@omnara/sdk'
import { expect, test } from '@playwright/test'

import { installFailureTracking, requiredEnvironmentVariable, signIn } from './helpers'
import { fetchLocalAPI, fetchLocalRoute } from './transport'

const projectID = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROJECT_ID')
const adminEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_ADMIN_EMAIL')
const providerConfig = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROVIDER_CONFIG')
const modelName = requiredEnvironmentVariable('OMNARA_WEB_E2E_MODEL_NAME')
const createAgentPath = `/projects/${projectID}/agents/new`

test('recovers lost launch and message responses without creating duplicates', async ({ page }) => {
  const failures = installFailureTracking(page)
  const lostResponses: string[] = []
  const attempts: Record<
    'launch' | 'input',
    { status: number; key: string | null; body: string | null; id: string }[]
  > = { launch: [], input: [] }
  let agentsURL = ''

  await page.route(
    new RegExp(`/projects/${projectID}/agents(?:/[^/]+/inputs)?$`),
    async (route) => {
      const request = route.request()
      if (request.method() !== 'POST') return route.continue()
      const operation = request.url().endsWith('/inputs') ? 'input' : 'launch'
      if (operation === 'launch') agentsURL = request.url()
      // Let the real API commit the write, then lose only the response to the browser.
      const response = await fetchLocalRoute(route)
      const json: unknown = await response.json()
      const id =
        operation === 'launch'
          ? schemas.zCreateAgentResponse.parse(json).agent.id
          : schemas.zCreateAgentInputResponse.parse(json).agent_input.id
      attempts[operation].push({
        status: response.status(),
        key: await request.headerValue('Idempotency-Key'),
        body: request.postData(),
        id,
      })
      if (attempts[operation].length === 1) {
        lostResponses.push(`request: POST ${request.url()} (net::ERR_CONNECTION_RESET)`)
        return route.abort('connectionreset')
      }
      return route.fulfill({ response })
    },
  )

  await signIn(page, adminEmail, createAgentPath)
  const agentName = `Lost response agent (${test.info().retry})`
  await page.getByRole('textbox', { name: 'Name', exact: true }).fill(agentName)
  await page.getByLabel('Instruction').fill('Verify durable launch and message retries.')
  await page.getByRole('combobox', { name: 'Model', exact: true }).click()
  await page.getByPlaceholder('Search shared models…').fill(modelName)
  await page
    .getByRole('option')
    .filter({ hasText: modelName })
    .filter({ hasText: providerConfig })
    .click()
  await page.getByRole('button', { name: 'Create & launch agent' }).click()
  await expect(page).toHaveURL(new RegExp(`/projects/${projectID}/agents/agt_[a-z2-7]+/events$`))

  await page.goto(page.url().replace(/\/events$/, '/chat'))
  const composer = page.getByPlaceholder('Message the agent…')
  const message = 'Keep exactly one copy of this message.'
  await composer.fill(message)
  const accepted = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      response.url().endsWith('/inputs') &&
      response.status() === 200,
  )
  await page.getByRole('button', { name: 'Send message' }).click()
  await accepted
  await expect(composer).not.toHaveAttribute('readonly')
  await expect(composer).toHaveValue('')
  await expect(page.getByText(message, { exact: true })).toHaveCount(1)
  await expect(page.getByRole('alert')).toHaveCount(0)

  // Launch gets its key from the SDK; chat supplies its own. Both must replay
  // identical bytes and return the original resource on the second attempt.
  for (const requests of Object.values(attempts)) {
    expect(requests).toHaveLength(2)
    const [first, retry] = requests
    if (!first) throw new Error('The browser did not submit the write')
    expect(first.status).toBe(201)
    expect(first.key).toBeTruthy()
    expect(retry).toEqual({ ...first, status: 200 })
  }
  const agentsResponse = await fetchLocalAPI(
    page,
    `${agentsURL}?name=${encodeURIComponent(agentName)}`,
  )
  expect(agentsResponse.status()).toBe(200)
  const agents = schemas.zListAgentsResponse.parse(await agentsResponse.json())
  expect(agents.data.map(({ id }) => id)).toEqual([attempts.launch[0]?.id])
  const backlogResponse = await fetchLocalAPI(
    page,
    `${agentsURL}/${attempts.launch[0]?.id}/inputs/backlog`,
  )
  expect(backlogResponse.status()).toBe(200)
  const backlog = schemas.zListAgentInputsResponse.parse(await backlogResponse.json())
  // This harness runs the API without an agent worker, so inputs stay queued.
  expect(backlog.data.map(({ id }) => id)).toEqual([attempts.input[0]?.id])
  expect(backlog.data[0]?.input_idempotency_key).toBe(attempts.input[0]?.key)
  expect(lostResponses).toHaveLength(2)
  expect(failures).toEqual(lostResponses)
})
