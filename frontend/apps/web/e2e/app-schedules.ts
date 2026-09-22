import { type ProjectApp, schemas } from '@omnara/sdk'
import { expect, type Page } from '@playwright/test'

import { readApp } from './fixtures'

export async function exerciseDiscordAppSchedule(
  page: Page,
  app: ProjectApp,
  profileId: string,
  profileName: string,
  apiProjectPath: string,
) {
  expect(app.provider_config).toEqual({ public_key: 'ab'.repeat(32) })
  const mentions = page.getByRole('region', { name: 'Mentions', exact: true })
  await expect(
    mentions.getByRole('combobox', { name: 'Profiles for mentions', exact: true }),
  ).toBeVisible()
  await expect(mentions).toContainText('0/16 selected')
  const savedSettings = page.waitForResponse(
    (response) =>
      response.request().method() === 'PUT' &&
      new URL(response.url()).pathname.endsWith(`/apps/${app.id}`),
  )
  await mentions.getByRole('button', { name: 'Save changes', exact: true }).click()
  const saved = await savedSettings
  expect(saved.status()).toBe(200)
  expect(schemas.zProjectApp.parse(await saved.json()).settings.launcher).toBeUndefined()
  await expect(mentions.getByRole('button', { name: 'Choose profiles', exact: true })).toBeVisible()
  const name = `${app.name} schedule`
  const schedules = page.getByRole('region', { name: 'Schedules', exact: true })
  await schedules.getByRole('button', { name: 'Add schedule', exact: true }).click()
  const createDialog = page.getByRole('dialog', { name: 'Add cron schedule', exact: true })
  await expect(createDialog.getByLabel('Server ID', { exact: false })).toHaveCount(0)
  await createDialog.getByLabel('Name', { exact: true }).fill(name)
  await createDialog.getByRole('combobox', { name: 'Agent profile', exact: true }).click()
  await page.getByPlaceholder('Choose an agent profile…').fill(profileName)
  await page.getByRole('option', { name: profileName, exact: true }).click()
  await createDialog.getByLabel('Channel ID', { exact: true }).fill('333')
  await expect(createDialog.getByLabel('Opening message')).toHaveValue(
    '{{.trigger.name}} — {{.trigger.local_date}}',
  )
  await createDialog.getByLabel('Cron expression').fill('0 9 1 1 *')
  await createDialog.getByRole('combobox', { name: 'Timezone', exact: true }).fill('UTC')
  await page.getByRole('option', { name: 'UTC (GMT+00:00)', exact: true }).click()
  await createDialog.getByLabel('Task instructions').fill('Summarize this channel.')
  const createdResponse = page.waitForResponse(
    (response) =>
      response.request().method() === 'POST' &&
      new URL(response.url()).pathname.endsWith('/cron-triggers'),
  )
  await createDialog.getByRole('button', { name: 'Create schedule', exact: true }).click()
  const created = await createdResponse
  expect(created.status()).toBe(201)
  expect(created.request().postDataJSON()).toEqual({
    name,
    cron: '0 9 1 1 *',
    timezone: 'UTC',
    target: {
      type: 'app',
      app_id: app.id,
      settings: {
        agent_profile_id: profileId,
        channel_id: '333',
        opening_message_template: '{{.trigger.name}} — {{.trigger.local_date}}',
        message_template: 'Summarize this channel.',
      },
    },
  })
  const trigger = schemas.zCronTrigger.parse(await created.json())
  expect(trigger.message_template).toBeUndefined()
  if (trigger.target.type !== 'app') throw new Error('Expected an app schedule')
  await expect(createDialog).toBeHidden()
  await expect(schedules.getByText('Not run yet', { exact: true })).toBeVisible()
  await schedules.getByRole('button', { name: `Edit schedule ${name}`, exact: true }).click()
  const editDialog = page.getByRole('dialog', { name: 'Edit cron schedule', exact: true })
  await expect(
    editDialog.getByRole('combobox', { name: 'Agent profile', exact: true }),
  ).toBeEnabled()
  await expect(editDialog.getByLabel('Task instructions')).toHaveValue('Summarize this channel.')
  await editDialog.getByLabel('Channel ID', { exact: true }).fill('334')
  const opening = 'Channel update\n{{.trigger.local_date}}'
  await editDialog.getByLabel('Opening message').fill(opening)
  const updatedResponse = page.waitForResponse(
    (response) =>
      response.request().method() === 'PATCH' &&
      new URL(response.url()).pathname.endsWith(`/cron-triggers/${trigger.id}`),
  )
  await editDialog.getByRole('button', { name: 'Save changes', exact: true }).click()
  const updated = await updatedResponse
  expect(updated.status()).toBe(200)
  expect(updated.request().postDataJSON()).not.toHaveProperty('message_template')
  expect(updated.request().postDataJSON()).toMatchObject({
    target: {
      ...trigger.target,
      settings: {
        ...trigger.target.settings,
        channel_id: '334',
        opening_message_template: opening,
      },
    },
  })
  await expect(editDialog).toBeHidden()
  await expect(schedules.getByText('Not run yet', { exact: true })).toBeVisible()
  const disabledResponse = page.waitForResponse(
    (response) =>
      response.request().method() === 'PATCH' &&
      new URL(response.url()).pathname.endsWith(`/cron-triggers/${trigger.id}`),
  )
  await schedules.getByRole('button', { name: `Disable schedule ${name}`, exact: true }).click()
  const disabled = await disabledResponse
  expect(disabled.status()).toBe(200)
  expect(disabled.request().postDataJSON()).toEqual({ enabled: false })
  expect(schemas.zCronTrigger.parse(await disabled.json()).enabled).toBe(false)
  expect(await disabled.finished()).toBeNull()
  await expect(
    schedules.getByRole('button', { name: `Enable schedule ${name}`, exact: true }),
  ).toBeEnabled()
  const deletedResponse = page.waitForResponse(
    (response) =>
      response.request().method() === 'DELETE' &&
      new URL(response.url()).pathname.endsWith(`/cron-triggers/${trigger.id}`),
  )
  page.once('dialog', (dialog) => void dialog.accept())
  await schedules.getByRole('button', { name: `Delete schedule ${name}`, exact: true }).click()
  const deleted = await deletedResponse
  expect(deleted.status()).toBe(204)
  await expect(schedules.getByText('No schedules yet.', { exact: true })).toBeVisible()
  // Schedules can be managed before enabling mention launches.
  expect((await readApp(page, apiProjectPath, app.id)).settings.launcher).toBeUndefined()
  await mentions.getByRole('button', { name: 'Choose profiles', exact: true }).click()
}
