/** @vitest-environment happy-dom */

import { OmnaraClientProvider } from '@omnara/react'
import {
  type AttachAgentChannelRequest,
  createOmnaraClient,
  type CronTrigger,
  type CronTriggerTarget,
} from '@omnara/sdk'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { act, type ReactNode, useState } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, expect, it, vi } from 'vitest'

import { fakeApi, type FakeRoute, jsonResponse } from '@/test/fake-api'
import { fakeId } from '@/test/fixtures'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, submit, waitForUI } from '@/test/secret-editor'

import { CreateCronTriggerDialog, EditCronTriggerDialog } from './CronTriggerDialog'
import { CronTriggersList } from './CronTriggersSection'

const orgId = fakeId('org')
const projectId = fakeId('proj')
const base = `/api/v1/orgs/${orgId}/projects/${projectId}`
const cronPath = base + '/cron-triggers'
const now = '2026-09-15T00:00:00Z'
const profileTarget: CronTriggerTarget = { type: 'profile', agent_profile_id: fakeId('aprf') }
const agentTarget: CronTriggerTarget = {
  type: 'agent',
  agent_id: fakeId('agt'),
  delivery_mode: 'queued',
}
const binding: AttachAgentChannelRequest = {
  channel_id: fakeId('itgt'),
  grants: { read: true, send: true, receive: false },
  reply_channel_grants: { read: true, send: true, receive: true },
}
const trigger: CronTrigger = {
  id: fakeId('cron'),
  org_id: orgId,
  project_id: projectId,
  name: 'daily-update',
  target: profileTarget,
  cron: '0 9 * * *',
  timezone: 'UTC',
  message_template: 'Post the daily update.',
  enabled: true,
  channel_bindings: [binding],
  last_fired_at: null,
  next_fire_at: now,
  failure_report: null,
  created_at: now,
  updated_at: now,
}
const updatePath = `${cronPath}/${trigger.id}`
const connection = {
  id: fakeId('iin'),
  org_id: orgId,
  project_id: projectId,
  integration_app_id: fakeId('iapp'),
  provider: 'discord',
  integration_kind: 'managed',
  connection_mode: 'gateway',
  state: 'active',
  display_name: 'Team server',
  metadata: {},
  created_at: now,
  updated_at: now,
}
const channelsPath = `${base}/integration-installs/${connection.id}/channels`
const channel = {
  channel_id: binding.channel_id,
  definition_id: fakeId('cdef'),
  name: 'announcements',
  provider_ref: '12345',
  provider_ref_kind: 'channel',
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

async function render(ui: ReactNode, routes: FakeRoute[] = []) {
  const api = fakeApi([
    ...routes,
    {
      method: 'GET',
      path: base + '/integration-installs',
      respond: () => jsonResponse({ data: [connection], next_cursor: null }),
    },
    {
      method: 'GET',
      path: channelsPath,
      respond: () => jsonResponse({ channels: [channel], next_cursor: null }),
    },
    {
      method: 'GET',
      path: cronPath,
      respond: () => jsonResponse({ data: [trigger], next_cursor: null }),
    },
    { method: 'POST', path: cronPath, respond: () => jsonResponse(trigger, 201) },
    { method: 'PATCH', path: updatePath, respond: () => jsonResponse(trigger) },
  ])
  const client = createOmnaraClient({ baseUrl: 'https://omnara.test/api/v1' })
  client.setConfig({ fetch: api.fetch })
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false }, mutations: { retry: false } },
  })
  await act(async () => {
    root.render(
      <OmnaraClientProvider client={client}>
        <QueryClientProvider client={queryClient}>{ui}</QueryClientProvider>
      </OmnaraClientProvider>,
    )
    await Promise.resolve()
  })
  return api
}

function create(target = profileTarget, close = vi.fn()) {
  return (
    <CreateCronTriggerDialog
      open
      orgId={orgId}
      projectId={projectId}
      target={target}
      targetLabel="daily-helper"
      onOpenChange={close}
    />
  )
}
function edit(value = trigger, close = vi.fn()) {
  return (
    <EditCronTriggerDialog
      open
      orgId={orgId}
      projectId={projectId}
      trigger={value}
      onOpenChange={close}
    />
  )
}
async function fillSchedule() {
  await enter('Name', trigger.name)
  await enter('Cron expression', trigger.cron)
  await enter('Message', trigger.message_template)
}
async function openChoice(id: string) {
  act(() => {
    document.getElementById(id)?.click()
  })
  await waitForUI(() => {
    expect(document.querySelector('[role="listbox"]')).not.toBeNull()
  })
}
async function selectOption(name: string) {
  await waitForUI(() => {
    expect(
      [...document.querySelectorAll('[role="option"]')].some((item) => item.textContent === name),
    ).toBe(true)
  })
  act(() => {
    ;[...document.querySelectorAll<HTMLElement>('[role="option"]')]
      .find((item) => item.textContent === name)
      ?.click()
  })
}
function checkbox(label: string, index = 0) {
  const element = [...document.querySelectorAll('label')].filter(
    (item) => item.textContent === label,
  )[index]
  const input = element ? document.getElementById(element.htmlFor) : null
  if (!(input instanceof HTMLInputElement)) throw new Error('Missing checkbox ' + label)
  return input
}
function toggle(label: string, index = 0) {
  act(() => {
    checkbox(label, index).click()
  })
}
async function addChannel() {
  await openChoice('cron-connection')
  await selectOption(connection.display_name)
  await openChoice('cron-channel')
  await selectOption('announcements (12345)')
  act(() => {
    button('Add channel access').click()
  })
}

it('creates a profile schedule without optional channels', async () => {
  const close = vi.fn()
  const api = await render(create(profileTarget, close))
  await fillSchedule()
  await submit()
  await waitForUI(() => {
    expect(close).toHaveBeenCalledWith(false)
  })
  expect(api.requestsTo('POST', cronPath)[0]?.body).toEqual({
    name: trigger.name,
    target: profileTarget,
    cron: trigger.cron,
    timezone: new Intl.DateTimeFormat().resolvedOptions().timeZone,
    message_template: trigger.message_template,
  })
  expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(1)
})

it('paginates connections and registered channels, prevents duplicate picks, and submits scheduled-post permissions', async () => {
  const api = await render(create(), [
    {
      method: 'GET',
      path: base + '/integration-installs',
      respond: (request) =>
        jsonResponse(
          request.url.searchParams.has('cursor')
            ? { data: [connection], next_cursor: null }
            : {
                data: [
                  {
                    ...connection,
                    id: fakeId('iin').replaceAll('a', 'b'),
                    display_name: 'Retired server',
                    state: 'disabled',
                  },
                ],
                next_cursor: 'next-connection',
              },
        ),
    },
    {
      method: 'GET',
      path: channelsPath,
      respond: (request) =>
        jsonResponse(
          request.url.searchParams.has('cursor')
            ? { channels: [channel], next_cursor: null }
            : {
                channels: [
                  { ...channel, channel_id: fakeId('itgt').replaceAll('a', 'b'), name: 'general' },
                ],
                next_cursor: 'next-channel',
              },
        ),
    },
  ])
  await fillSchedule()
  await openChoice('cron-connection')
  expect(document.querySelector('[role="option"]')).toBeNull()
  await waitForUI(() => {
    expect(button('Load more results')).toBeDefined()
  })
  act(() => {
    button('Load more results').click()
  })
  await selectOption(connection.display_name)
  await openChoice('cron-channel')
  await waitForUI(() => {
    expect(button('Load more results')).toBeDefined()
  })
  act(() => {
    button('Load more results').click()
  })
  await selectOption('announcements (12345)')
  act(() => {
    button('Add channel access').click()
  })
  expect(checkbox('Receive').checked).toBe(false)
  expect(checkbox('Receive', 1).checked).toBe(true)
  expect(checkbox('Read').checked).toBe(true)
  expect(checkbox('Send').checked).toBe(true)
  await openChoice('cron-channel')
  expect(
    [...document.querySelectorAll('[role="option"]')].some(
      (item) => item.textContent === 'announcements (12345)',
    ),
  ).toBe(false)
  act(() => {
    document.getElementById('cron-channel')?.click()
  })
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('POST', cronPath)).toHaveLength(1)
  })
  expect(api.requestsTo('POST', cronPath)[0]?.body).toMatchObject({ channel_bindings: [binding] })
  expect(
    api.requestsTo('GET', channelsPath).map((request) => request.url.searchParams.get('cursor')),
  ).toEqual([null, 'next-channel'])
  expect(
    api
      .requestsTo('GET', base + '/integration-installs')
      .map((request) => request.url.searchParams.get('cursor')),
  ).toEqual([null, 'next-connection'])
  expect(api.requests.filter((request) => request.method !== 'GET')).toHaveLength(1)
})

it('omits unchanged bindings on edit even when discovery fails and the channel label is unknown', async () => {
  const api = await render(edit(), [
    {
      method: 'GET',
      path: base + '/integration-installs',
      respond: () => jsonResponse({ code: 'service_unavailable', error: 'Unavailable' }, 503),
    },
  ])
  expect(document.body.textContent).toContain(binding.channel_id)
  await enter('Message', 'Updated message')
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('PATCH', updatePath)).toHaveLength(1)
  })
  expect(api.requestsTo('PATCH', updatePath)[0]?.body).not.toHaveProperty('channel_bindings')
})

it('sends an explicit empty list when every saved binding is removed', async () => {
  const api = await render(edit())
  act(() => {
    button(`Remove ${binding.channel_id}`).click()
  })
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('PATCH', updatePath)).toHaveLength(1)
  })
  expect(api.requestsTo('PATCH', updatePath)[0]?.body).toHaveProperty('channel_bindings', [])
})

it('edits parent and reply permissions independently, including intentional parent receive', async () => {
  const api = await render(edit())
  toggle('Receive')
  toggle('Send', 1)
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('PATCH', updatePath)).toHaveLength(1)
  })
  expect(api.requestsTo('PATCH', updatePath)[0]?.body).toHaveProperty('channel_bindings', [
    {
      ...binding,
      grants: { read: true, send: true, receive: true },
      reply_channel_grants: { read: true, send: false, receive: true },
    },
  ])
})

it('blocks invalid permissions and requires an explicit choice to remove reply access', async () => {
  const api = await render(edit())
  toggle('Send')
  expect(button('Save changes').disabled).toBe(true)
  expect(document.body.textContent).toContain('Enable Send on this channel')
  await submit()
  expect(api.requestsTo('PATCH', updatePath)).toHaveLength(0)
  expect(checkbox('Allow reply threads').checked).toBe(true)
  toggle('Allow reply threads')
  toggle('Read')
  expect(document.body.textContent).toContain('Choose at least one permission for this channel')
  expect(button('Save changes').disabled).toBe(true)
  toggle('Read')
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('PATCH', updatePath)).toHaveLength(1)
  })
  expect(api.requestsTo('PATCH', updatePath)[0]?.body).toHaveProperty('channel_bindings', [
    { channel_id: binding.channel_id, grants: { read: true, send: false, receive: false } },
  ])
})

it('blocks empty reply permissions', async () => {
  const api = await render(edit())
  toggle('Read', 1)
  toggle('Send', 1)
  toggle('Receive', 1)
  expect(button('Save changes').disabled).toBe(true)
  expect(document.body.textContent).toContain('Choose at least one permission for reply threads')
  await submit()
  expect(api.requestsTo('PATCH', updatePath)).toHaveLength(0)
})

it('locks editing during save and retains changed bindings after a rejected request', async () => {
  let finish: (response: Response) => void = () => {
    throw new Error('Save has not started')
  }
  let attempts = 0
  const close = vi.fn()
  const api = await render(edit(trigger, close), [
    {
      method: 'PATCH',
      path: updatePath,
      respond: () =>
        ++attempts === 1
          ? new Promise<Response>((resolve) => {
              finish = resolve
            })
          : jsonResponse(trigger),
    },
  ])
  toggle('Receive')
  act(() => {
    document
      .querySelector('form')
      ?.dispatchEvent(new Event('submit', { bubbles: true, cancelable: true }))
  })
  await waitForUI(() => {
    expect(api.requestsTo('PATCH', updatePath)).toHaveLength(1)
    expect(button('Save changes').disabled).toBe(true)
    expect(checkbox('Receive').disabled).toBe(true)
  })
  expect(document.body.textContent).not.toContain('Close')
  await act(async () => {
    finish(jsonResponse({ code: 'conflict', error: 'Channel unavailable. Try again.' }, 409))
    await Promise.resolve()
  })
  await waitForUI(() => {
    expect(document.body.textContent).toContain('Channel unavailable. Try again.')
  })
  expect(checkbox('Receive').checked).toBe(true)
  expect(close).not.toHaveBeenCalled()
  await submit()
  await waitForUI(() => {
    expect(close).toHaveBeenCalledWith(false)
  })
  expect(api.requestsTo('PATCH', updatePath).map((request) => request.body)).toEqual([
    expect.objectContaining({
      channel_bindings: [{ ...binding, grants: { ...binding.grants, receive: true } }],
    }),
    expect.objectContaining({
      channel_bindings: [{ ...binding, grants: { ...binding.grants, receive: true } }],
    }),
  ])
})

it.each(['create', 'edit'])(
  'keeps agent-target %s schedules isolated from channel discovery and binding writes',
  async (mode) => {
    const api = await render(
      mode === 'create'
        ? create(agentTarget)
        : edit({ ...trigger, target: agentTarget, channel_bindings: [] }),
    )
    expect(document.body.textContent).not.toContain('Channels (optional)')
    if (mode === 'create') await fillSchedule()
    await submit()
    const method = mode === 'create' ? 'POST' : 'PATCH'
    const path = mode === 'create' ? cronPath : updatePath
    await waitForUI(() => {
      expect(api.requestsTo(method, path)).toHaveLength(1)
    })
    expect(api.requestsTo(method, path)[0]?.body).not.toHaveProperty('channel_bindings')
    expect(api.requestsTo(method, path)[0]?.body).toHaveProperty('target', agentTarget)
    expect(api.requestsTo('GET', base + '/integration-installs')).toHaveLength(0)
  },
)

it('does not expose channel editing to a read-only schedule viewer', async () => {
  const api = await render(
    <CronTriggersList
      orgId={orgId}
      projectId={projectId}
      canManage={false}
      filters={{}}
      emptyMessage="No schedules"
    />,
  )
  await waitForUI(() => {
    expect(document.body.textContent).toContain(trigger.name)
  })
  expect(button(`Edit schedule ${trigger.name}`).disabled).toBe(true)
  expect(document.querySelector('form')).toBeNull()
  expect(api.requestsTo('GET', base + '/integration-installs')).toHaveLength(0)
})

it('resets the channel choice when switching connections and preserves already-added channels', async () => {
  const second = {
    ...connection,
    id: fakeId('iin').replaceAll('a', 'b'),
    display_name: 'Second server',
  }
  const secondPath = `${base}/integration-installs/${second.id}/channels`
  const api = await render(create(), [
    {
      method: 'GET',
      path: base + '/integration-installs',
      respond: () => jsonResponse({ data: [connection, second], next_cursor: null }),
    },
    {
      method: 'GET',
      path: secondPath,
      respond: () => jsonResponse({ channels: [], next_cursor: null }),
    },
  ])
  await fillSchedule()
  await addChannel()
  await openChoice('cron-connection')
  await selectOption(second.display_name)
  expect(button('Add channel access').disabled).toBe(true)
  expect(document.body.textContent).toContain(channel.name)
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('POST', cronPath)).toHaveLength(1)
  })
  expect(api.requestsTo('POST', cronPath)[0]?.body).toHaveProperty('channel_bindings', [binding])
})

it('resolves saved channel labels from loaded pages without changing saved permissions', async () => {
  const api = await render(edit())
  expect(button(`Remove ${binding.channel_id}`)).toBeDefined()
  await openChoice('cron-connection')
  await selectOption(connection.display_name)
  await waitForUI(() => {
    expect(button('Remove Team server · announcements (12345)')).toBeDefined()
  })
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('PATCH', updatePath)).toHaveLength(1)
  })
  expect(api.requestsTo('PATCH', updatePath)[0]?.body).not.toHaveProperty('channel_bindings')
})

it('keeps saved channel labels across connection changes and refreshes them from later pages', async () => {
  const second = { ...connection, id: `iin_${'b'.repeat(26)}`, display_name: 'Second server' }
  let channelName = channel.name
  const api = await render(edit(), [
    {
      method: 'GET',
      path: base + '/integration-installs',
      respond: () => jsonResponse({ data: [connection, second], next_cursor: null }),
    },
    {
      method: 'GET',
      path: channelsPath,
      respond: (request) =>
        jsonResponse(
          request.url.searchParams.has('cursor')
            ? { channels: [{ ...channel, name: channelName }], next_cursor: null }
            : { channels: [], next_cursor: 'next-channel' },
        ),
    },
    {
      method: 'GET',
      path: `${base}/integration-installs/${second.id}/channels`,
      respond: () => jsonResponse({ channels: [], next_cursor: null }),
    },
  ])
  expect(api.requests.some((request) => request.url.pathname.endsWith('/channels'))).toBe(false)
  await openChoice('cron-connection')
  await selectOption(connection.display_name)
  await openChoice('cron-channel')
  await waitForUI(() => {
    expect(button('Load more results')).toBeDefined()
  })
  act(() => {
    button('Load more results').click()
  })
  await waitForUI(() => {
    expect(button('Remove Team server · announcements (12345)')).toBeDefined()
  })
  act(() => {
    document.getElementById('cron-channel')?.click()
  })
  await openChoice('cron-connection')
  await selectOption(second.display_name)
  expect(button('Remove Team server · announcements (12345)')).toBeDefined()
  channelName = 'renamed-announcements'
  await openChoice('cron-connection')
  await selectOption(connection.display_name)
  await waitForUI(() => {
    expect(button('Remove Team server · renamed-announcements (12345)')).toBeDefined()
  })
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('PATCH', updatePath)).toHaveLength(1)
  })
  expect(api.requestsTo('PATCH', updatePath)[0]?.body).not.toHaveProperty('channel_bindings')
})

it('preserves customized reply grants when reply threads are toggled off and back on', async () => {
  const saved = {
    ...trigger,
    channel_bindings: [
      { ...binding, reply_channel_grants: { read: true, send: false, receive: false } },
    ],
  }
  const api = await render(edit(saved))
  toggle('Allow reply threads')
  toggle('Allow reply threads')
  expect(checkbox('Receive', 1).checked).toBe(false)
  expect(checkbox('Send', 1).checked).toBe(false)
  expect(checkbox('Read', 1).checked).toBe(true)
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('PATCH', updatePath)).toHaveLength(1)
  })
  expect(api.requestsTo('PATCH', updatePath)[0]?.body).not.toHaveProperty('channel_bindings')
})

it('discards abandoned permission edits even when the owner keeps the dialog component mounted', async () => {
  function KeptMountedDialog() {
    const [open, setOpen] = useState(true)
    return (
      <>
        <button
          type="button"
          onClick={() => {
            setOpen(true)
          }}
        >
          Reopen schedule
        </button>
        <EditCronTriggerDialog
          open={open}
          onOpenChange={setOpen}
          orgId={orgId}
          projectId={projectId}
          trigger={trigger}
        />
      </>
    )
  }
  const api = await render(<KeptMountedDialog />)
  toggle('Receive')
  expect(checkbox('Receive').checked).toBe(true)
  act(() => {
    button('Close').click()
  })
  expect(document.querySelector('form')).toBeNull()
  act(() => {
    button('Reopen schedule').click()
  })
  expect(checkbox('Receive').checked).toBe(false)
  await submit()
  await waitForUI(() => {
    expect(api.requestsTo('PATCH', updatePath)).toHaveLength(1)
  })
  expect(api.requestsTo('PATCH', updatePath)[0]?.body).not.toHaveProperty('channel_bindings')
})

it('explains the channel limit and allows replacing a saved channel after removal', async () => {
  const alphabet = 'abcdefghijklmnopqrstuvwxyz234567'
  const bindings = Array.from({ length: 64 }, (_, index) => ({
    channel_id: `itgt_${'a'.repeat(24)}${alphabet[Math.floor(index / 32)]}${alphabet[index % 32]}`,
    grants: { read: true, send: false, receive: false },
  }))
  await render(edit({ ...trigger, channel_bindings: bindings }))
  expect(document.body.textContent).toContain('Maximum 64 channels')
  expect(button('Search connections…').disabled).toBe(true)
  act(() => {
    button(`Remove ${bindings[0]?.channel_id}`).click()
  })
  expect(button('Search connections…').disabled).toBe(false)
  expect(document.body.textContent).not.toContain('Maximum 64 channels')
})
