import { existsSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { createOmnaraClient } from '@omnara/sdk'
import { Command } from 'commander'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

import { registerGroup } from './factory.ts'
import { commandGroups } from './manifest.ts'

const orgID = `org_${'a'.repeat(26)}`
const projectID = `proj_${'a'.repeat(26)}`
const storeID = `mst_${'a'.repeat(26)}`
const digest = `sha256:${'a'.repeat(64)}`
const apiUrl = 'https://omnara.example/api/v1'
const storeUrl = `${apiUrl}/orgs/${orgID}/projects/${projectID}/memory-stores`
const store = {
  id: storeID,
  name: 'engineering',
  description: '',
  read_only: false,
  created_at: '2026-09-18T00:00:00Z',
  updated_at: '2026-09-18T00:00:00Z',
}
const originalExitCode = process.exitCode
let dir: string

beforeEach(() => {
  dir = mkdtempSync(join(tmpdir(), 'omnara-memory-cli-'))
  vi.spyOn(console, 'log').mockImplementation(() => undefined)
  vi.spyOn(console, 'error').mockImplementation(() => undefined)
})

afterEach(() => {
  rmSync(dir, { recursive: true, force: true })
  process.exitCode = originalExitCode
  vi.restoreAllMocks()
})

function cli(reply: (request: Request) => Response | Promise<Response>) {
  const requests: Request[] = []
  const fetch: typeof globalThis.fetch = async (input, init) => {
    const request = new Request(input, init)
    requests.push(request.clone())
    return reply(request)
  }
  const client = createOmnaraClient({ baseUrl: apiUrl, fetch })
  return {
    requests,
    get request() {
      const request = requests[0]
      if (!request) throw new Error('expected an API request')
      return request
    },
    run: async (...args: string[]) => {
      const group = commandGroups.find((group) => group.name === 'memory-stores')
      if (!group) throw new Error('memory-stores command is missing')
      const program = new Command().exitOverride()
      registerGroup(
        program,
        {
          client,
          apiUrl,
          issuerUrl: 'https://omnara.example',
          defaultOrgId: orgID,
          defaultProjectId: projectID,
          store: { path: '', read: () => ({}), update: () => ({}) },
          fetch,
          sleep: () => Promise.resolve(),
          ensureLoggedIn: () => Promise.resolve(),
        },
        group,
      )
      await program.parseAsync(['memory-stores', ...args], { from: 'user' })
    },
  }
}

describe('memory store commands', () => {
  it.each([
    {
      args: ['create', '--name', 'engineering', '--read-only'],
      method: 'POST',
      suffix: '',
      body: { name: 'engineering', read_only: true },
    },
    { args: ['get', storeID], method: 'GET', suffix: `/${storeID}`, body: undefined },
    {
      args: ['update', storeID, '--description', 'Reference', '--no-read-only'],
      method: 'PATCH',
      suffix: `/${storeID}`,
      body: { description: 'Reference', read_only: false },
    },
    { args: ['delete', storeID], method: 'DELETE', suffix: `/${storeID}`, body: undefined },
  ])('sends $method with the configured project', async ({ args, method, suffix, body }) => {
    const command = cli(() =>
      method === 'DELETE' ? new Response(null, { status: 204 }) : Response.json(store),
    )
    await command.run(...args, '--json')
    expect(command.requests).toHaveLength(1)
    const request = command.request
    expect(request.method).toBe(method)
    expect(request.url).toBe(storeUrl + suffix)
    expect(await request.text()).toBe(body === undefined ? '' : JSON.stringify(body))
    expect(console.error).not.toHaveBeenCalled()
  })

  it('lists stores with pagination and explicit scope overrides', async () => {
    const otherOrg = `org_${'b'.repeat(26)}`
    const otherProject = `proj_${'b'.repeat(26)}`
    const command = cli(() => Response.json({ data: [store], has_more: false }))
    await command.run(
      'list',
      '--org',
      otherOrg,
      '--project',
      otherProject,
      '--name',
      'eng*',
      '--limit',
      '2',
      '--cursor',
      'next',
    )
    const url = new URL(command.request.url)
    expect(url.pathname).toBe(`/api/v1/orgs/${otherOrg}/projects/${otherProject}/memory-stores`)
    expect(Object.fromEntries(url.searchParams)).toEqual({
      name: 'eng*',
      limit: '2',
      cursor: 'next',
    })
    expect(console.log).toHaveBeenCalledWith(expect.stringContaining('engineering'))
    expect(console.error).not.toHaveBeenCalled()
  })
})

describe('memory file commands', () => {
  it('lists a directory with its cursor', async () => {
    const command = cli(() => Response.json({ data: [], next_cursor: 'next-page' }))
    await command.run(
      'files',
      'list',
      storeID,
      '--path',
      'notes',
      '--cursor',
      'previous',
      '--limit',
      '3',
    )
    const url = new URL(command.request.url)
    expect(url.pathname).toBe(
      `/api/v1/orgs/${orgID}/projects/${projectID}/memory-stores/${storeID}/files`,
    )
    expect(Object.fromEntries(url.searchParams)).toEqual({
      path: 'notes',
      limit: '3',
      cursor: 'previous',
    })
    expect(console.log).toHaveBeenCalledWith(expect.stringContaining('--cursor next-page'))
  })

  it.each([Buffer.from([0, 255, 128, 10]), Buffer.alloc(0)])(
    'uploads exact bytes',
    async (bytes) => {
      const file = join(dir, 'input.bin')
      writeFileSync(file, bytes)
      const command = cli(() => Response.json({ path: '/memory/engineering/notes/a.bin', digest }))
      await command.run(
        'files',
        'upload',
        storeID,
        '--path',
        'notes/a #?.bin',
        '--file',
        file,
        '--expected-digest',
        digest,
        '--json',
      )
      const request = command.request
      expect(request.method).toBe('PUT')
      expect(request.headers.get('content-type')).toBe('application/octet-stream')
      expect(Object.fromEntries(new URL(request.url).searchParams)).toEqual({
        path: 'notes/a #?.bin',
        expected_digest: digest,
      })
      expect(Buffer.from(await request.arrayBuffer())).toEqual(bytes)
      expect(console.log).toHaveBeenCalledWith(expect.stringContaining(digest))
      expect(console.error).not.toHaveBeenCalled()
    },
  )

  it('omits the digest for creation and reports conflicts without retrying', async () => {
    const file = join(dir, 'input.txt')
    writeFileSync(file, 'replacement')
    const command = cli(() =>
      Response.json(
        {
          error: 'expected_digest is required',
          code: 'file_content_conflict',
          current_digest: digest,
        },
        { status: 409 },
      ),
    )
    await command.run('files', 'upload', storeID, '--path', 'note.txt', '--file', file)
    expect(command.requests).toHaveLength(1)
    expect(new URL(command.request.url).searchParams.has('expected_digest')).toBe(false)
    expect(process.exitCode).toBe(1)
    expect(console.error).toHaveBeenCalledWith(expect.stringContaining('file_content_conflict'))
    expect(console.error).toHaveBeenCalledWith(`current_digest: ${digest}`)
  })

  it('requires the expected digest for deletion', async () => {
    const command = cli(() => new Response(null, { status: 204 }))
    await command.run('files', 'delete', storeID, '--path', 'notes/a.txt')
    expect(command.requests).toHaveLength(0)
    expect(process.exitCode).toBe(1)
    await command.run(
      'files',
      'delete',
      storeID,
      '--path',
      'notes/a.txt',
      '--expected-digest',
      digest,
    )
    expect(command.requests).toHaveLength(1)
    expect(command.request.method).toBe('DELETE')
    expect(new URL(command.request.url).searchParams.get('expected_digest')).toBe(digest)
  })

  it.each([Buffer.from([0, 255, 128, 10]), Buffer.alloc(0)])(
    'downloads exact bytes and reports the digest',
    async (bytes) => {
      const output = join(dir, 'download.bin')
      const command = cli(
        () => new Response(bytes, { headers: { 'X-Omnara-File-Digest': digest } }),
      )
      await command.run(
        'files',
        'download',
        storeID,
        '--path',
        'notes/a.bin',
        '--output',
        output,
        '--json',
      )
      expect(readFileSync(output)).toEqual(bytes)
      expect(console.log).toHaveBeenCalledWith(JSON.stringify({ output, digest }, null, 2))
      expect(console.error).not.toHaveBeenCalled()
    },
  )

  it('preserves an existing local destination', async () => {
    const output = join(dir, 'existing.txt')
    writeFileSync(output, 'keep me')
    const command = cli(
      () => new Response('replacement', { headers: { 'X-Omnara-File-Digest': digest } }),
    )
    await command.run('files', 'download', storeID, '--path', 'notes/a.txt', '--output', output)
    expect(readFileSync(output, 'utf8')).toBe('keep me')
    expect(process.exitCode).toBe(1)
  })

  it.each(['http', 'digest', 'body'])(
    'does not create a destination on a %s failure',
    async (failure) => {
      const output = join(dir, 'download.txt')
      const command = cli(() => {
        if (failure === 'http')
          return Response.json({ error: 'not found', code: 'not_found' }, { status: 404 })
        if (failure === 'digest') return new Response('no digest')
        return new Response(
          new ReadableStream({
            start(controller) {
              controller.error(new Error('interrupted'))
            },
          }),
          { headers: { 'X-Omnara-File-Digest': digest } },
        )
      })
      await command.run('files', 'download', storeID, '--path', 'notes/a.txt', '--output', output)
      expect(existsSync(output)).toBe(false)
      expect(process.exitCode).toBe(1)
      expect(console.log).not.toHaveBeenCalled()
    },
  )
})
