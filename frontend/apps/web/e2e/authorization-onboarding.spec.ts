import { schemas } from '@omnara/sdk'
import { expect, test } from '@playwright/test'

import { currentUser, currentUserOrg, fakeId } from '../src/test/fixtures'

const devicePath = '/device?user_code=ABCDE-F1234'
const oauthQuery = '?client_id=browser-client&state=preserved-state&scope=openid%20email'
const oauthPath = `/oauth/authorize${oauthQuery}`
const timestamp = '2026-01-01T00:00:00Z'

for (const destination of [devicePath, oauthPath]) {
  test(`returns a new user to ${destination.split('?')[0]} after organization onboarding`, async ({
    page,
    baseURL,
  }) => {
    if (!baseURL) throw new Error('Browser base URL is required')
    const origin = new URL(baseURL).origin
    // The router normalizes query encoding (for example, spaces from %20 to +).
    const returnURL = new URL(destination, origin)
    returnURL.search = returnURL.searchParams.toString()
    let hasOrg = false
    const creations: unknown[] = []
    const decisions: unknown[] = []
    const unexpected: string[] = []
    const errors: string[] = []
    page.on('pageerror', (error) => errors.push(error.message))
    await page
      .context()
      .addCookies([{ name: 'omnara_csrf', value: 'browser-session', url: origin }])
    // Mock the API contract, while exercising real routing, forms, SDK calls, and full reloads.
    await page.route('**/api/**', async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      const path = url.pathname
      if (path === '/api/v1/me') {
        await route.fulfill({ json: currentUser(hasOrg ? [currentUserOrg()] : []) })
      } else if (path === '/api/v1/invitations') {
        await route.fulfill({ json: { data: [], next_cursor: null } })
      } else if (path === '/api/v1/orgs' && request.method() === 'POST') {
        creations.push(schemas.zCreateOrganizationRequest.parse(request.postDataJSON()))
        expect(request.headers()['idempotency-key']).toBeTruthy()
        expect(request.headers()['x-omnara-csrf']).toBe('browser-session')
        hasOrg = true
        await route.fulfill({
          status: 201,
          json: schemas.zCreateOrganizationResponse.parse({
            org: { ...currentUserOrg(), updated_at: timestamp },
            project: {
              id: fakeId('proj'),
              org_id: fakeId('org'),
              name: 'default',
              created_at: timestamp,
              updated_at: timestamp,
            },
            membership: {
              org_id: fakeId('org'),
              user_id: fakeId('usr'),
              role: 'owner',
              created_at: timestamp,
            },
          }),
        })
      } else if (path === '/api/auth/device/pending') {
        expect(url.searchParams.get('user_code')).toBe('ABCDE-F1234')
        await route.fulfill({
          json: {
            client_name: 'Browser CLI',
            token_name: 'Browser token',
            created_at: timestamp,
            expires_at: '2099-01-01T00:00:00Z',
          },
        })
      } else if (path === '/api/auth/oauth/authorize/pending') {
        expect(url.search).toBe(returnURL.search)
        await route.fulfill({
          json: {
            client_id: 'browser-client',
            client_name: 'Browser MCP',
            client_uri: '',
            redirect_uri: `${origin}/oauth/callback`,
            redirect_host: new URL(origin).host,
            loopback: false,
            resource: `${origin}/mcp`,
            scope: 'openid email',
          },
        })
      } else if (
        path === '/api/auth/device/approve' ||
        path === '/api/auth/oauth/authorize/approve'
      ) {
        expect(request.method()).toBe('POST')
        expect(request.headers()['x-omnara-csrf']).toBe('browser-session')
        decisions.push(request.postDataJSON())
        await route.fulfill(
          path.includes('/oauth/')
            ? {
                json: {
                  redirect_url: `${origin}/oauth/callback?code=browser-code&state=preserved-state`,
                },
              }
            : { status: 204 },
        )
      } else {
        unexpected.push(`${request.method()} ${path}`)
        await route.fulfill({ status: 500, json: { error: 'Unexpected test request' } })
      }
    })
    await page.route('**/oauth/callback?*', (route) =>
      route.fulfill({ body: 'Authorization complete' }),
    )

    await page.goto(destination)
    await expect(page).toHaveURL(
      (url) =>
        url.pathname === '/onboarding' &&
        url.searchParams.get('return_to') === returnURL.pathname + returnURL.search,
    )
    await expect
      .poll(() => page.evaluate(() => window.omnaraBrowserContext))
      .toEqual({
        userId: fakeId('usr'),
        organizationIds: [],
      })
    await page.getByLabel('Organization name').fill('Browser organization')
    await page.getByRole('button', { name: 'Create organization', exact: true }).click()
    await expect(page).toHaveURL(returnURL.href)
    await expect(
      page.getByRole('heading', {
        name: destination === devicePath ? 'Approve device login' : 'Authorize access to Omnara',
      }),
    ).toBeVisible()
    await expect
      .poll(() => page.evaluate(() => window.omnaraBrowserContext))
      .toEqual({
        userId: fakeId('usr'),
        organizationIds: [fakeId('org')],
      })
    await page.getByRole('button', { name: 'Approve', exact: true }).click()

    if (destination === devicePath) {
      await expect(page.getByText('Return to the CLI to continue.', { exact: true })).toBeVisible()
      expect(decisions).toEqual([{ user_code: 'ABCDE-F1234' }])
    } else {
      await expect(page).toHaveURL(
        `${origin}/oauth/callback?code=browser-code&state=preserved-state`,
      )
      expect(decisions).toEqual([{ query: returnURL.search }])
    }
    expect(creations).toEqual([{ name: 'Browser organization' }])
    expect(unexpected).toEqual([])
    expect(errors).toEqual([])
  })
}
