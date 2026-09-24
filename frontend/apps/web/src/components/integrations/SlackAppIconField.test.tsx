/** @vitest-environment happy-dom */
import { act } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, expect, it, vi } from 'vitest'

import { fakeApi } from '@/test/fake-api'
import { fakeId, projectIntegration } from '@/test/fixtures'
import { renderProjectIntegration } from '@/test/project-integration-render'
import { enableReactActEnvironment } from '@/test/react-act'
import { button, enter, field, waitForUI } from '@/test/secret-editor'

import { ConnectSlackForm } from './ConnectSlackForm'
import {
  slackAppIconPayload,
  slackConnectionFormValid,
  validateAppIcon,
} from './ConnectSlackFormState'

const file = new File(['image'], 'icon.png', { type: 'image/png' })
afterEach(() => vi.unstubAllGlobals())

it.each([512, 2000])('accepts a square %ipx icon and releases the decoded image', async (size) => {
  const close = vi.fn()
  vi.stubGlobal(
    'createImageBitmap',
    vi.fn().mockResolvedValue({ width: size, height: size, close }),
  )
  expect(await validateAppIcon(file)).toEqual({ kind: 'file', file })
  expect(close).toHaveBeenCalledOnce()
})

it.each([
  [1024, 768, 'square'],
  [511, 511, '512 × 512'],
  [2001, 2001, '2000 × 2000'],
])('rejects a %i × %i icon', async (width, height, message) => {
  const close = vi.fn()
  vi.stubGlobal('createImageBitmap', vi.fn().mockResolvedValue({ width, height, close }))
  const result = await validateAppIcon(file)
  expect(result.kind).toBe('error')
  if (result.kind !== 'error') throw new Error('Expected invalid image')
  expect(result.message).toContain(message)
  expect(await slackAppIconPayload(result)).toBeUndefined()
  expect(
    slackConnectionFormValid({ appName: 'Bot', appConfigurationToken: 'token', appIcon: result }),
  ).toBe(true)
  expect(close).toHaveBeenCalledOnce()
})

it('rejects corrupt images and skips decoding unsupported or oversized files', async () => {
  const decode = vi.fn().mockRejectedValue(new Error('Invalid image data'))
  vi.stubGlobal('createImageBitmap', decode)
  expect(
    await validateAppIcon(new File(['svg'], 'icon.svg', { type: 'image/svg+xml' })),
  ).toMatchObject({ kind: 'error' })
  expect(
    await validateAppIcon(
      new File([new Uint8Array(5 * 1024 * 1024 + 1)], 'icon.png', { type: 'image/png' }),
    ),
  ).toMatchObject({ kind: 'error' })
  expect(decode).not.toHaveBeenCalled()
  const result = await validateAppIcon(file)
  expect(result.kind).toBe('error')
  if (result.kind !== 'error') throw new Error('Expected corrupt image')
  expect(result.message).toContain('Could not read')
})

it.each(['replace', 'switch mode'] as const)(
  'keeps the latest selection when a pending check finishes after %s',
  async (action) => {
    let finish!: (image: { width: number; height: number; close: () => void }) => void
    const pending = new Promise((resolve) => {
      finish = resolve
    })
    const close = vi.fn()
    vi.stubGlobal(
      'createImageBitmap',
      vi.fn().mockReturnValueOnce(pending).mockResolvedValue({ width: 512, height: 512, close }),
    )
    const restore = enableReactActEnvironment()
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    const context = renderProjectIntegration(
      root,
      fakeApi([]),
      <ConnectSlackForm
        orgId={fakeId('org')}
        projectId={fakeId('proj')}
        integration={projectIntegration({ integration_type: 'slack_thread' })}
      />,
    )
    function select(file: File) {
      act(() => {
        const transfer = new DataTransfer()
        transfer.items.add(file)
        const input = field('Slack app icon (optional)')
        if (!(input instanceof HTMLInputElement)) throw new Error('Expected file input')
        input.files = transfer.files
        input.dispatchEvent(new Event('change', { bubbles: true }))
      })
    }
    try {
      await enter('App configuration token', 'token')
      expect(button('Connect integration').disabled).toBe(false)
      select(file)
      expect(button('Connect integration').disabled).toBe(true)
      expect(container.textContent).toContain('Checking icon')
      if (action === 'replace') {
        select(new File(['replacement'], 'replacement.png', { type: 'image/png' }))
        await waitForUI(() => {
          expect(container.textContent).toContain('replacement.png')
        })
      } else {
        act(() => {
          field('Use an existing Slack app').click()
        })
        act(() => {
          field('Use an existing Slack app').click()
        })
      }
      await act(async () => {
        finish({ width: 1024, height: 768, close })
        await pending
      })
      expect(container.querySelector('[role="alert"]')).toBeNull()
      expect(container.textContent).toContain(
        action === 'replace' ? 'replacement.png' : 'No icon selected',
      )
      expect(button('Connect integration').disabled).toBe(false)
    } finally {
      act(() => {
        root.unmount()
      })
      context.cache.clear()
      container.remove()
      restore()
    }
  },
)
