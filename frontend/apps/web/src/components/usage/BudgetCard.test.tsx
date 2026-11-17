/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import { type Budget, type BudgetScope, createOmnaraClient, type UsageReport } from '@omnara/sdk'
import { getCurrentUserQueryKey } from '@omnara/sdk/tanstack'
import { zCreateBudgetRequest, zUpdateBudgetRequest } from '@omnara/sdk/zod'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  RouterProvider,
} from '@tanstack/react-router'
import { act, Suspense } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it } from 'vitest'

import { ActiveOrgProvider } from '@/components/active-org/ActiveOrgProvider'
import { BudgetCard } from '@/components/usage/BudgetCard'
import { OrganizationUsagePage } from '@/routes/OrganizationUsagePage'
import {
  emptyResponse,
  type FakeApi,
  fakeApi,
  type FakeRoute,
  jsonResponse,
  type RecordedRequest,
} from '@/test/fake-api'
import { currentUser, currentUserOrg, fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, choose, enter, field, submit, waitForUI } from '@/test/secret-editor'

const orgId = fakeId('org')
const projectId = fakeId('proj')
const budgetsPath = `/api/v1/orgs/${orgId}/budgets`
const budgetIds = ['b', 'c', 'd', 'e'].map((letter) => `bdg_${letter.repeat(26)}`)

function budget(interval: Budget['interval'], amount: string, spent = '0'): Budget {
  return {
    id: budgetIds[['day', 'week', 'month', 'lifetime'].indexOf(interval)] ?? '',
    org_id: orgId,
    scope: { type: 'org' },
    interval,
    amount_usd: amount,
    usage: { amount_usd: spent },
    created_at: '2026-11-01T00:00:00Z',
    updated_at: '2026-11-01T00:00:00Z',
  }
}

let root: Root
let container: HTMLDivElement
let restore: () => void
beforeEach(() => {
  restore = enableReactActEnvironment()
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})
afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
  restore()
})

function budgetApi(initial: Budget[], extra: FakeRoute[] = []) {
  let budgets = initial
  return fakeApi([
    ...extra,
    {
      method: 'GET',
      path: budgetsPath,
      respond: () => jsonResponse({ data: budgets, next_cursor: null }),
    },
    {
      method: 'POST',
      path: budgetsPath,
      respond: (request) => {
        const body = zCreateBudgetRequest.parse(request.body)
        const created = { ...budget(body.interval, body.amount_usd), scope: body.scope }
        budgets = [...budgets, created]
        const { usage: _, ...response } = created
        return jsonResponse(response, 201)
      },
    },
    ...budgetIds.flatMap((id) => [
      {
        method: 'PATCH',
        path: `${budgetsPath}/${id}`,
        respond: (request: RecordedRequest) => {
          const body = zUpdateBudgetRequest.parse(request.body)
          budgets = budgets.map((item) =>
            item.id === id ? { ...item, amount_usd: body.amount_usd } : item,
          )
          const { usage: _, ...response } =
            budgets.find((item) => item.id === id) ?? budget('day', '1')
          return jsonResponse(response)
        },
      },
      {
        method: 'DELETE',
        path: `${budgetsPath}/${id}`,
        respond: () => {
          budgets = budgets.filter((item) => item.id !== id)
          return emptyResponse()
        },
      },
    ]),
  ])
}

function clientFor(api: FakeApi) {
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  return client
}

async function renderCard(
  api: FakeApi,
  scope: BudgetScope = { type: 'org' },
  variant: 'card' | 'sidebar' = 'card',
) {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={clientFor(api)}>
        <QueryClientProvider client={queryClient}>
          <BudgetCard orgId={orgId} scope={scope} variant={variant} />
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
}

it('lists each interval with its spend, reset, and reached state', async () => {
  const api = budgetApi([
    budget('lifetime', '100', '3'),
    budget('month', '10', '3'),
    budget('day', '2', '2'),
  ])
  await renderCard(api)
  await waitForUI(() => {
    expect(container.textContent).toContain('Monthly$3.00of $10.00')
  })
  const text = container.textContent
  expect(text.indexOf('Daily')).toBeLessThan(text.indexOf('Monthly'))
  expect(text.indexOf('Monthly')).toBeLessThan(text.indexOf('Lifetime'))
  expect(text).toContain('30% used')
  const now = new Date()
  const nextMonth = new Intl.DateTimeFormat(undefined, {
    month: 'short',
    day: 'numeric',
    timeZone: 'UTC',
  }).format(Date.UTC(now.getUTCFullYear(), now.getUTCMonth() + 1, 1))
  expect(text).toContain(`Resets ${nextMonth}`)
  expect(text).toMatch(/New model calls are blocked until the budget is raised or resets \w+ \d+/)
  expect(text).toContain('Budget reached')
  expect(text).toContain('Lifetime$3.00of $100.00')
  expect(text).toContain('Never resets')
  expect(button('Add budget')).toBeDefined()
  expect([...(api.requestsTo('GET', budgetsPath)[0]?.url.searchParams ?? [])]).toEqual([
    ['scope_type', 'org'],
  ])
})

it('adds a project budget for an interval without one', async () => {
  const api = budgetApi([])
  await renderCard(api, { type: 'project', project_id: projectId })
  await waitForUI(() => {
    expect(container.textContent).toContain('No budgets set')
  })
  expect([...(api.requestsTo('GET', budgetsPath)[0]?.url.searchParams ?? [])]).toEqual([
    ['scope_type', 'project'],
    ['project_id', projectId],
  ])
  act(() => {
    button('Add budget').click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="dialog"]')).not.toBeNull()
  })
  await choose('Interval', 'Weekly')
  await enter('Budget (USD)', '25')
  await submit()
  expect(api.requestsTo('POST', budgetsPath).map((request) => request.body)).toEqual([
    { scope: { type: 'project', project_id: projectId }, interval: 'week', amount_usd: '25' },
  ])
  await waitForUI(() => {
    expect(container.textContent).toContain('Weekly$0.00of $25.00')
  })
})

it('lists and adds budgets for an agent and its subagents in the sidebar', async () => {
  const scope = { type: 'agent', project_id: projectId, agent_id: fakeId('agt') } as const
  const api = budgetApi([])
  await renderCard(api, scope, 'sidebar')
  await waitForUI(() => {
    expect(container.textContent).toContain('No budgets')
  })
  expect(container.textContent).not.toContain('Model calls only')
  expect([...(api.requestsTo('GET', budgetsPath)[0]?.url.searchParams ?? [])]).toEqual([
    ['scope_type', 'agent'],
    ['project_id', projectId],
    ['agent_id', scope.agent_id],
  ])
  act(() => {
    button('Add').click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="dialog"]')?.textContent).toContain(
      'New model calls from this agent and its subagents stop',
    )
  })
  await enter('Budget (USD)', '5')
  await submit()
  expect(api.requestsTo('POST', budgetsPath).map((request) => request.body)).toEqual([
    { scope, interval: 'month', amount_usd: '5' },
  ])
})

it('edits and removes a budget', async () => {
  const api = budgetApi([budget('month', '10')])
  await renderCard(api)
  await waitForUI(() => {
    expect(button('Edit Monthly budget')).toBeDefined()
  })
  act(() => {
    button('Edit Monthly budget').click()
  })
  await waitForUI(() => {
    expect(field('Budget (USD)').value).toBe('10')
  })
  await enter('Budget (USD)', '99')
  act(() => {
    button('Close').click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="dialog"]')).toBeNull()
  })
  act(() => {
    button('Edit Monthly budget').click()
  })
  await waitForUI(() => {
    expect(field('Budget (USD)').value).toBe('10')
  })
  expect(document.querySelector('#budget-interval')).toBeNull()
  await enter('Budget (USD)', '25')
  await submit()
  const path = `${budgetsPath}/${budgetIds[2] ?? ''}`
  expect(api.requestsTo('PATCH', path).map((request) => request.body)).toEqual([
    { amount_usd: '25' },
  ])
  await waitForUI(() => {
    expect(container.textContent).toContain('of $25.00')
  })
  act(() => {
    button('Edit Monthly budget').click()
  })
  await waitForUI(() => {
    expect(button('Remove budget')).toBeDefined()
  })
  await act(async () => {
    button('Remove budget').click()
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(api.requestsTo('DELETE', path)).toHaveLength(1)
    expect(container.textContent).toContain('No budgets set')
  })
})

const usage: UsageReport = {
  totals: {
    model_calls: 1,
    tokens: {
      input_tokens_total: 10,
      uncached_input_tokens: 10,
      cache_read_input_tokens: 0,
      cache_write_input_tokens: 0,
      output_tokens_total: 1,
      reasoning_output_tokens: 0,
    },
    cost: {
      provider_reported_usd: '1',
      model_calls_with_reported_cost: 1,
      effective_usd: '1',
      model_calls_with_effective_cost: 1,
    },
  },
  by_model: [],
}

async function renderUsagePage(
  role: 'owner' | 'member',
  usageResponse: () => Response = () => jsonResponse(usage),
) {
  const api = budgetApi(
    [budget('month', '10')],
    [{ method: 'GET', path: `/api/v1/orgs/${orgId}/usage`, respond: usageResponse }],
  )
  const client = clientFor(api)
  const org = currentUserOrg({ id: orgId, role })
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  queryClient.setQueryData(getCurrentUserQueryKey({ client }), currentUser([org]))
  const rootRoute = createRootRoute()
  const router = createRouter({
    routeTree: rootRoute.addChildren([
      createRoute({
        getParentRoute: () => rootRoute,
        path: '/',
        component: () => (
          <ActiveOrgProvider>
            <OrganizationUsagePage />
          </ActiveOrgProvider>
        ),
      }),
    ]),
    history: createMemoryHistory({ initialEntries: ['/'] }),
  })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>
          <Suspense fallback={null}>
            <RouterProvider router={router} />
          </Suspense>
        </QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await new Promise((resolve) => setTimeout(resolve, 0))
  })
  return api
}

it('shows budgets to org admins only', async () => {
  const ownerApi = await renderUsagePage('owner')
  await waitForUI(() => {
    expect(container.textContent).toContain('of $10.00')
  })
  expect(ownerApi.requestsTo('GET', budgetsPath)).toHaveLength(1)
  act(() => {
    root.unmount()
  })
  root = createRoot(container)

  const memberApi = await renderUsagePage('member')
  await waitForUI(() => {
    expect(memberApi.requests.length).toBeGreaterThan(0)
  })
  expect(container.textContent).not.toContain('Budgets')
  expect(memberApi.requestsTo('GET', budgetsPath)).toHaveLength(0)
})

it('keeps budgets available when the usage report fails', async () => {
  await renderUsagePage('owner', () =>
    jsonResponse({ code: 'invalid_request', error: 'usage unavailable' }, 400),
  )
  await waitForUI(() => {
    expect(container.querySelector('[role="alert"]')?.textContent).toBeTruthy()
    expect(container.textContent).toContain('of $10.00')
  })
})
