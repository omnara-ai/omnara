import { expect, test } from '@playwright/test'

import {
  installFailureTracking,
  requiredEnvironmentVariable,
  signIn,
  signInThroughLoginForm,
} from './helpers'

const projectID = requiredEnvironmentVariable('OMNARA_WEB_E2E_PROJECT_ID')
const orgName = requiredEnvironmentVariable('OMNARA_WEB_E2E_ORG_NAME')
const switchOrgName = requiredEnvironmentVariable('OMNARA_WEB_E2E_SWITCH_ORG_NAME')
const createdOrgName = 'zz web created organization'
const adminEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_ADMIN_EMAIL')
const inviteeEmail = requiredEnvironmentVariable('OMNARA_WEB_E2E_INVITEE_EMAIL')
const createAgentPath = `/projects/${projectID}/agents/new`

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
