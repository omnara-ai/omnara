/** @vitest-environment happy-dom */

import { act } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterAll, afterEach, beforeAll, beforeEach, describe, expect, it, vi } from 'vitest'

import { enableReactActEnvironment } from '@/test/react-act'

import { ProviderOptionsOverrideFields } from './MachineOverrideFields'

let container: HTMLDivElement
let root: Root
let restoreActEnvironment: () => void

beforeAll(() => {
  restoreActEnvironment = enableReactActEnvironment()
})

afterAll(() => {
  restoreActEnvironment()
})

beforeEach(() => {
  container = document.createElement('div')
  document.body.append(container)
  root = createRoot(container)
})

afterEach(() => {
  act(() => {
    root.unmount()
  })
  container.remove()
})

function renderCreateOSOverrides(values: {
  resource: string
  location: string
  startupScript: string
}) {
  act(() => {
    root.render(
      <ProviderOptionsOverrideFields
        idPrefix="agent-machine"
        pool={{ provider: 'createos', management_kind: 'tenant' }}
        values={values}
        onChange={vi.fn()}
      />,
    )
  })
}

describe('CreateOS agent machine overrides', () => {
  it('hides shape and provider-assigned region when adding a machine source', () => {
    renderCreateOSOverrides({ resource: '', location: '', startupScript: '' })

    expect(container.querySelector('#agent-machine-resource')).toBeNull()
    expect(container.querySelector('#agent-machine-location')).toBeNull()
    expect(container.querySelector('#agent-machine-startup-script')).not.toBeNull()
  })

  it('keeps unsupported fields hidden while editing an existing agent', () => {
    renderCreateOSOverrides({
      resource: 's-2vcpu-4gb',
      location: 'eu',
      startupScript: 'echo ready',
    })

    expect(container.querySelector('#agent-machine-resource')).toBeNull()
    expect(container.querySelector('#agent-machine-location')).toBeNull()
    expect(
      container.querySelector<HTMLTextAreaElement>('#agent-machine-startup-script')?.value,
    ).toBe('echo ready')
  })
})
