import {
  type AppSubscription,
  type AppType,
  type CreateAgentRequest,
  type CreateAppSubscriptionRequest,
  type ProjectApp,
  schemas,
  zJsonText,
} from '@omnara/sdk'
import { expect, type Page, test } from '@playwright/test'
import { z } from 'zod'

async function request(
  page: Page,
  path: string,
  body?: CreateAgentRequest | CreateAppSubscriptionRequest,
) {
  return page.evaluate(
    async ({ path, body }) => {
      const headers = new Headers({ 'Content-Type': 'application/json' })
      if (body) {
        const cookie = document.cookie
          .split(';')
          .map((part) => part.trim())
          .find((part) => part.startsWith('__Host-omnara_csrf=') || part.startsWith('omnara_csrf='))
        if (!cookie) throw new Error('Missing browser CSRF cookie')
        headers.set('X-Omnara-Csrf', decodeURIComponent(cookie.slice(cookie.indexOf('=') + 1)))
      }
      const response = await fetch(path, {
        method: body ? 'POST' : 'GET',
        headers,
        body: body ? JSON.stringify(body) : undefined,
      })
      return { status: response.status, body: await response.text() }
    },
    { path, body },
  )
}

export async function exerciseAppConversations(
  page: Page,
  app: ProjectApp,
  profileId: string,
  apiProjectPath: string,
) {
  const profileResponse = await request(page, `${apiProjectPath}/agent-profiles/${profileId}`)
  expect(profileResponse.status).toBe(200)
  const profile = zJsonText.pipe(schemas.zAgentProfile).parse(profileResponse.body)
  const type = app.app_type === 'github_pr' ? 'pull_request' : 'thread_messages'
  const conversation =
    app.app_type === 'github_pr'
      ? { repository_id: 333, pull_request: 1 }
      : { channel_id: '333333333333333333', thread_id: '444444444444444444' }
  const launchResponse = await request(page, `${apiProjectPath}/agents`, {
    config: profile.current_config_id,
    profile: profile.id,
    name: `${app.name} subscriber`,
    subscriptions: [{ app_id: app.id, type, conversation }],
  })
  expect(launchResponse.status).toBe(201)
  const { agent } = zJsonText.pipe(schemas.zLaunchAgentResponse).parse(launchResponse.body)
  expect(agent.current_config_id).toBe(profile.current_config_id)
  const path = `${apiProjectPath}/apps/${app.id}/subscriptions`
  const listing = await request(page, path)
  expect(listing.status).toBe(200)
  const initial = zJsonText.pipe(schemas.zListAppSubscriptionsResponse).parse(listing.body)
  expect(initial.data).toHaveLength(1)
  const first = schemas.zAppSubscription.parse(initial.data[0])
  expect(first).toMatchObject({ agent_id: agent.id, agent_name: agent.name, type, conversation })
  const attached = await request(page, path, {
    agent_id: agent.id,
    type,
    conversation:
      app.app_type === 'github_pr'
        ? { repository_id: 333, pull_request: 2 }
        : {
            guild_id: '111111111111111111',
            channel_id: '333333333333333333',
            thread_id: '555555555555555555',
          },
  })
  expect(attached.status).toBe(201)
  const second = zJsonText.pipe(schemas.zAppSubscription).parse(attached.body)
  await page.goto(`/projects/${app.project_id}/apps/${app.id}`)
  const section = page.getByRole('region', { name: 'Conversations', exact: true })
  await expect(section).toBeVisible()
  await expect(section.getByRole('listitem')).toHaveCount(2)
  const agentLinks = section.getByRole('link', { name: agent.name, exact: true })
  await expect(agentLinks).toHaveCount(2)
  await expect(agentLinks.first()).toHaveAttribute(
    'href',
    `/projects/${app.project_id}/agents/${agent.id}/events`,
  )
  await auditConversationLayout(page, app.app_type, 'connected')
  await stopConversation(page, app, first, apiProjectPath)
  await expect(section.getByRole('listitem')).toHaveCount(1)
  return second
}

async function stopConversation(
  page: Page,
  app: ProjectApp,
  subscription: AppSubscription,
  apiProjectPath: string,
) {
  const section = page.getByRole('region', { name: 'Conversations', exact: true })
  let label: string
  if (app.app_type === 'github_pr') {
    const address = z
      .object({ repository_id: z.number(), pull_request: z.number() })
      .parse(subscription.conversation)
    label = `Repository ${address.repository_id} · PR #${address.pull_request}`
  } else {
    const address = z
      .object({ channel_id: z.string(), thread_id: z.string() })
      .parse(subscription.conversation)
    label = `Channel ${address.channel_id} · Thread ${address.thread_id}`
  }
  const row = section.getByRole('listitem').filter({ has: page.getByText(label, { exact: true }) })
  await expect(row).toBeVisible()
  page.once('dialog', (dialog) => void dialog.accept())
  const deleted = page.waitForResponse(
    (response) =>
      response.request().method() === 'DELETE' &&
      new URL(response.url()).pathname.endsWith(`/subscriptions/${subscription.id}`),
  )
  await row.getByRole('button', { name: /^Stop forwarding/ }).click()
  expect((await deleted).status()).toBe(204)
  await expect(row).toBeHidden()
  const agentResponse = await request(page, `${apiProjectPath}/agents/${subscription.agent_id}`)
  expect(agentResponse.status).toBe(200)
  expect(zJsonText.pipe(schemas.zGetAgentResponse).parse(agentResponse.body).agent.state).toBe(
    'active',
  )
}

export async function stopDisconnectedConversation(
  page: Page,
  app: ProjectApp,
  subscription: AppSubscription,
  apiProjectPath: string,
) {
  const section = page.getByRole('region', { name: 'Conversations', exact: true })
  await expect(section).toBeVisible()
  await expect(section).toContainText('Forwarding is paused')
  await auditConversationLayout(page, app.app_type, 'disconnected')
  await stopConversation(page, app, subscription, apiProjectPath)
  await expect(section).toContainText('No conversations yet.')
}

async function auditConversationLayout(page: Page, appType: AppType, state: string) {
  const section = page.getByRole('region', { name: 'Conversations', exact: true })
  const viewport = page.viewportSize()
  if (!viewport) throw new Error('Browser audit requires a fixed viewport')
  for (const width of [viewport.width, 390]) {
    await page.setViewportSize({ width, height: viewport.height })
    await expect
      .poll(() => section.evaluate((element) => element.scrollWidth <= element.clientWidth))
      .toBe(true)
    await expect(section.getByRole('button', { name: /^Stop forwarding/ }).first()).toBeVisible()
    const name = `${appType}-${state}-conversations-${width}`
    const path = test.info().outputPath(`${name}.png`)
    await section.screenshot({ path })
    await test.info().attach(name, { path, contentType: 'image/png' })
  }
  await page.setViewportSize(viewport)
}
