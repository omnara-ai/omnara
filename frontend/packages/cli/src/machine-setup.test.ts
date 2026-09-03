import type { ConnectByoMachineResponse } from '@omnara/sdk'
import { describe, expect, it } from 'vitest'

import {
  classifyDaemonSupervision,
  ensureDaemonApiUrl,
  ensureSupportedPlatform,
  formatMachineSetup,
} from './machine-setup.ts'

it('builds install instructions from the API origin', () => {
  const formatted = formatMachineSetup({} as ConnectByoMachineResponse, {
    apiUrl: 'https://api.example.com/v1',
  })
  expect(formatted.value).toMatchObject({
    install_command: "curl -fsSL 'https://api.example.com/install/omnarad.sh' | sh",
  })
})

describe('machine create-local preflight', () => {
  it('accepts the platforms the omnarad installer supports', () => {
    expect(() => {
      ensureSupportedPlatform('darwin', 'arm64')
    }).not.toThrow()
    expect(() => {
      ensureSupportedPlatform('linux', 'x64')
    }).not.toThrow()
    expect(() => {
      ensureSupportedPlatform('win32', 'x64')
    }).toThrow(/macOS and Linux/)
    expect(() => {
      ensureSupportedPlatform('linux', 'ia32')
    }).toThrow(/amd64 and arm64/)
  })

  it('accepts API URLs the daemon canonicalizes', () => {
    for (const url of [
      'https://api.omnara.com/v1',
      'https://app.omnara.com/api',
      'http://localhost:8080',
      'http://127.0.0.1:8080',
      'http://[::1]:8080',
      'http://host.docker.internal:8080',
    ]) {
      expect(() => {
        ensureDaemonApiUrl(url)
      }).not.toThrow()
    }
  })

  it('rejects API URLs the daemon rejects', () => {
    expect(() => {
      ensureDaemonApiUrl('app.omnara.com')
    }).toThrow(/absolute URL/)
    expect(() => {
      ensureDaemonApiUrl('ftp://app.omnara.com')
    }).toThrow(/https or loopback http/)
    expect(() => {
      ensureDaemonApiUrl('http://app.omnara.com')
    }).toThrow(/https unless the host is local/)
    expect(() => {
      ensureDaemonApiUrl('https://user:pw@app.omnara.com')
    }).toThrow(/user info/)
    expect(() => {
      ensureDaemonApiUrl('https://app.omnara.com/?x=1')
    }).toThrow(/query or fragment/)
    expect(() => {
      ensureDaemonApiUrl('https://app.omnara.com/#top')
    }).toThrow(/query or fragment/)
    expect(() => {
      ensureDaemonApiUrl('https://app.omnara.com:70000')
    }).toThrow(/absolute URL/)
  })
})

describe('daemon supervision detection', () => {
  const tree = (parents: Record<number, { pid: number; command: string }>) => (pid: number) =>
    parents[pid]

  it('treats a daemon descended from the installer as foreground', () => {
    const lookup = tree({
      500: { pid: 400, command: 'sh' },
      400: { pid: 300, command: 'sh' },
      300: { pid: 200, command: 'node' },
    })
    expect(classifyDaemonSupervision(500, 300, lookup)).toBe('foreground')
  })

  it('treats a daemon owned by launchd or systemd as a service', () => {
    expect(classifyDaemonSupervision(500, 300, tree({ 500: { pid: 1, command: 'launchd' } }))).toBe(
      'service',
    )
    expect(
      classifyDaemonSupervision(500, 300, tree({ 500: { pid: 4242, command: 'systemd' } })),
    ).toBe('service')
  })

  it('stays undecided when the ancestry cannot be read', () => {
    expect(classifyDaemonSupervision(500, 300, tree({}))).toBeUndefined()
  })
})
