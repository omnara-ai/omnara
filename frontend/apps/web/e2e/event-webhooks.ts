import { type AgentProfile, type EventWebhook, schemas } from '@omnara/sdk'
import { expect, type Page } from '@playwright/test'

/** Exercises the project form against the real API; only page size is reduced. */
export async function exerciseProjectWebhooks(
  page: Page,
  profile: Pick<AgentProfile, 'id' | 'project_id' | 'name'>,
) {
  await expect(page.getByRole('button', { name: 'Webhooks', exact: true })).toHaveCount(0)
  const launched = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname.endsWith('/agents'),
  )
  await page.getByRole('button', { name: 'Launch', exact: true }).click()
  const { agent } = schemas.zLaunchAgentResponse.parse(await (await launched).json())
  await expect(page).toHaveURL(`/projects/${profile.project_id}/agents/${agent.id}/events`)
  await expect(
    page.locator('[data-sidebar="group-label"]').filter({ hasText: /^Webhooks$/ }),
  ).toHaveCount(0)

  // Forward the genuine list request to the API with a small page size so the
  // browser exercises pagination without creating dozens of subscriptions.
  await page.route(/\/event-webhooks(?:\?.*)?$/, async (route) => {
    if (route.request().method() !== 'GET') return route.continue()
    const url = new URL(route.request().url())
    url.searchParams.set('limit', '2')
    await route.continue({ url: url.toString() })
  })
  await page.getByRole('link', { name: 'Webhooks', exact: true }).click()
  await expect(page).toHaveURL(`/projects/${profile.project_id}/webhooks`)
  const subscriptions: EventWebhook[] = []
  for (const type of ['project', 'profile', 'agent'] as const) {
    await page.getByRole('button', { name: 'Add webhook', exact: true }).click()
    const dialog = page.getByRole('dialog')
    await dialog
      .getByLabel('Webhook URL', { exact: true })
      .fill(`https://example.com/${profile.id}/${type}`)
    if (type !== 'project') {
      await dialog.getByRole('combobox', { name: 'Receive events from', exact: true }).click()
      await page
        .getByRole('option', {
          name: type === 'profile' ? 'Agents using one profile' : 'One agent',
          exact: true,
        })
        .click()
      await expect(
        dialog.getByRole('button', { name: 'Create webhook', exact: true }),
      ).toBeDisabled()
      await dialog
        .getByRole('combobox', {
          name: type === 'profile' ? 'Agent profile' : 'Agent',
          exact: true,
        })
        .click()
      await page
        .getByRole('option', { name: type === 'profile' ? profile.name : agent.name, exact: true })
        .click()
    }
    const created = page.waitForResponse(
      (response) =>
        response.request().method() === 'POST' &&
        new URL(response.url()).pathname.endsWith('/event-webhooks'),
    )
    await dialog.getByRole('button', { name: 'Create webhook', exact: true }).click()
    const response = await created
    expect(response.status()).toBe(201)
    const subscription = schemas.zEventWebhook.parse(await response.json())
    expect(subscription.scope).toEqual(
      type === 'project'
        ? { type }
        : type === 'profile'
          ? { type, agent_profile_id: profile.id }
          : { type, agent_id: agent.id },
    )
    subscriptions.push(subscription)
    await expect(dialog).toHaveCount(0)
  }
  await page.getByRole('button', { name: 'Load more webhooks', exact: true }).click()
  for (const subscription of subscriptions) {
    await expect(
      page.getByRole('button', { name: 'Edit webhook ' + subscription.url, exact: true }),
    ).toBeVisible()
  }
  await expect(page.getByText('All agents in this project', { exact: true })).toBeVisible()
  await expect(page.getByText('Agents using profile:', { exact: false })).toBeVisible()

  const selected = subscriptions[2]
  if (!selected) throw new Error('Agent subscription was not created')
  await page.getByRole('button', { name: 'Edit webhook ' + selected.url, exact: true }).click()
  const dialog = page.getByRole('dialog')
  await expect(
    dialog.getByText('Notifications already queued can still arrive after an edit.', {
      exact: false,
    }),
  ).toBeVisible()
  await dialog.getByRole('combobox', { name: 'Receive events from', exact: true }).click()
  await page.getByRole('option', { name: 'Agents using one profile', exact: true }).click()
  await dialog.getByRole('combobox', { name: 'Agent profile', exact: true }).click()
  await page.getByRole('option', { name: profile.name, exact: true }).click()
  const patched = page.waitForResponse(
    (response) =>
      response.request().method() === 'PATCH' &&
      new URL(response.url()).pathname.endsWith('/event-webhooks/' + selected.id),
  )
  await dialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  const response = await patched
  expect(response.status()).toBe(200)
  expect(schemas.zEventWebhook.parse(await response.json())).toMatchObject({
    id: selected.id,
    url: selected.url,
    event_types: selected.event_types,
    scope: { type: 'profile', agent_profile_id: profile.id },
    created_at: selected.created_at,
  })
  await expect(dialog).toHaveCount(0)
  const row = page
    .getByRole('listitem')
    .filter({ has: page.getByText(selected.url, { exact: true }) })
  await expect(row).toContainText('Agents using profile:')
  await expect(row).toContainText(profile.name)
}
