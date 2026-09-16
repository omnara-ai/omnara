import { schemas } from '@omnara/sdk'
import { describe, expect, it, vi } from 'vitest'

import { SlackClient } from './client'
import { prepareSlackFiles } from './files'
import { slackEventFile } from './protocol'
import { attempt, body, credentials, json, slackServer } from './test-support'

function work() {
  return { resize: vi.fn<(bytes: number) => void>(), release: vi.fn<() => void>() }
}

describe('Slack input file preparation', () => {
  it('hydrates shared files and retains UTF-8 text extension priority and native metadata', async () => {
    const requests: string[] = []
    const apiUrl = await slackServer((request, response) => {
      requests.push(request.url ?? '')
      expect(request.headers.authorization).toBe(`Bearer ${credentials.botToken}`)
      if (request.url === '/files.info') {
        void body(request).then((bytes) => {
          expect(JSON.parse(bytes.toString())).toEqual({ file: 'F1' })
          json(response, {
            ok: true,
            file: { id: 'F1', url_private_download: `${apiUrl}/file`, size: 5 },
          })
        })
      } else {
        response.writeHead(200, { 'content-type': 'application/octet-stream' })
        response.end('hello')
      }
    })
    const prepared = await prepareSlackFiles(
      new SlackClient(credentials.botToken, apiUrl),
      [
        slackEventFile.parse({
          id: 'F1',
          name: 'notes.md',
          mimetype: 'application/pdf',
          file_access: 'check_file_info',
        }),
      ],
      attempt(),
      work(),
    )
    expect(requests).toEqual(['/files.info', '/file'])
    expect(prepared.blocks).toEqual([
      {
        type: 'media',
        media_type: 'text/markdown',
        filename: 'notes.md',
        data: Buffer.from('hello').toString('base64'),
      },
    ])
    expect(prepared.metadata).toEqual([
      {
        ordinal: 0,
        id: 'F1',
        name: 'notes.md',
        title: '',
        mimetype: 'application/pdf',
        declared_size_bytes: 5,
        file_access: 'check_file_info',
        status: 'stored',
        content_type: 'text/markdown',
        filename: 'notes.md',
        size_bytes: 5,
      },
    ])
    expect(prepared.summary).toBe('')
    schemas.zInlineMediaContentBlock.parse(prepared.blocks[0])
  })

  it('enforces the streamed 10 MiB limit even when declared size is absent', async () => {
    const apiUrl = await slackServer((_request, response) => {
      response.writeHead(200, { 'content-type': 'text/plain' })
      response.end(Buffer.alloc(10 * 1024 * 1024 + 1, 'x'))
    })
    const prepared = await prepareSlackFiles(
      new SlackClient(credentials.botToken, apiUrl),
      [slackEventFile.parse({ name: 'huge.txt', url_private: `${apiUrl}/file` })],
      attempt(),
      work(),
    )
    expect(prepared.blocks).toEqual([])
    expect(prepared.metadata[0]?.reason).toBe('file_too_large')
    expect(prepared.summary).toBe('Slack files not included:\n- huge.txt skipped: file too large')
  })

  it('enforces the combined 24 MiB limit and grows reservations with actual downloads', async () => {
    const reservation = work()
    let downloads = 0
    const apiUrl = await slackServer((_request, response) => {
      downloads += 1
      expect(reservation.resize.mock.calls.at(-1)?.[0]).toBeGreaterThanOrEqual(
        (downloads - 1) * 8 * 1024 * 1024 * 8,
      )
      response.end(Buffer.alloc(8 * 1024 * 1024, 'a'))
    })
    const file = slackEventFile.parse({
      name: 'data.txt',
      size: 8 * 1024 * 1024,
      url_private: `${apiUrl}/file`,
    })
    const prepared = await prepareSlackFiles(
      new SlackClient(credentials.botToken, apiUrl),
      [file, file, file, file],
      attempt(),
      reservation,
    )
    expect(downloads).toBe(3)
    expect(prepared.blocks).toHaveLength(3)
    expect(prepared.metadata[3]?.reason).toBe('too_large')
    expect(reservation.resize).toHaveBeenLastCalledWith(24 * 1024 * 1024 * 8 + 1024 * 1024)
  })

  it('bounds attachment count and skips empty/unsupported/oversized files with visible reasons', async () => {
    let downloads = 0
    const apiUrl = await slackServer((request, response) => {
      downloads += 1
      if (request.url === '/binary') response.end(Buffer.from([255, 0, 255]))
      else response.end()
    })
    const files = Array.from({ length: 22 }, () => slackEventFile.parse({ name: 'missing' }))
    files[0] = slackEventFile.parse({ name: 'empty.txt', url_private: `${apiUrl}/empty` })
    files[1] = slackEventFile.parse({
      name: 'invalid.txt',
      mimetype: 'text/plain',
      url_private: `${apiUrl}/binary`,
    })
    files[2] = slackEventFile.parse({
      name: 'declared.txt',
      size: 11 * 1024 * 1024,
      url_private: `${apiUrl}/declared`,
    })
    const prepared = await prepareSlackFiles(
      new SlackClient(credentials.botToken, apiUrl),
      files,
      attempt(),
      work(),
    )
    expect(downloads).toBe(2)
    expect(prepared.blocks).toEqual([])
    expect(prepared.metadata.slice(0, 3).map((file) => file.reason)).toEqual([
      'empty',
      'unsupported_media_type',
      'too_large',
    ])
    expect(prepared.metadata.at(-1)).toEqual({
      status: 'skipped',
      reason: 'too_many_attachments',
      count: 2,
    })
    expect(prepared.metadata).toHaveLength(21)
    expect(prepared.summary).toContain('attachment skipped: too many attachments')
  })

  it.each([
    'https://slack.com.evil.test/file',
    'https://evil.test/file',
    'http://files.slack.com/file',
    'https://user@files.slack.com/file',
    'https://files.slack.com:444/file',
  ])('rejects untrusted file URL before fetching or forwarding credentials: %s', async (url) => {
    const fetch = vi.spyOn(globalThis, 'fetch')
    try {
      const prepared = await prepareSlackFiles(
        new SlackClient(credentials.botToken),
        [slackEventFile.parse({ name: 'file.txt', url_private: url })],
        attempt(),
        work(),
      )
      expect(fetch).not.toHaveBeenCalled()
      expect(prepared.metadata[0]?.reason).toBe('invalid_file_url')
    } finally {
      fetch.mockRestore()
    }
  })

  it('never follows a provider redirect with the bot token', async () => {
    const paths: string[] = []
    const apiUrl = await slackServer((request, response) => {
      paths.push(request.url ?? '')
      response.writeHead(302, { location: `${apiUrl}/capture` })
      response.end()
    })
    const prepared = await prepareSlackFiles(
      new SlackClient(credentials.botToken, apiUrl),
      [slackEventFile.parse({ name: 'file.txt', url_private: `${apiUrl}/redirect` })],
      attempt(),
      work(),
    )
    expect(paths).toEqual(['/redirect'])
    expect(prepared.blocks).toEqual([])
  })

  it.each([429, 503])(
    'retries the receipt rather than accepting incomplete content on HTTP %s',
    async (status) => {
      const apiUrl = await slackServer((_request, response) => {
        json(response, {}, status, { 'retry-after': '30' })
      })
      await expect(
        prepareSlackFiles(
          new SlackClient(credentials.botToken, apiUrl),
          [slackEventFile.parse({ id: 'F1', file_access: 'check_file_info' })],
          attempt(),
          work(),
        ),
      ).rejects.toMatchObject({ retryable: true })
    },
  )

  it('uses safe byte-bounded filenames and detects binary media without MIME metadata', async () => {
    const bytes = Buffer.from([137, 80, 78, 71, 13, 10, 26, 10, 255])
    const apiUrl = await slackServer((_request, response) => response.end(bytes))
    const prepared = await prepareSlackFiles(
      new SlackClient(credentials.botToken, apiUrl),
      [slackEventFile.parse({ name: '😀'.repeat(100), url_private: `${apiUrl}/file` })],
      attempt(),
      work(),
    )
    expect(prepared.blocks[0]).toEqual({
      type: 'media',
      media_type: 'image/png',
      filename: 'attachment.png',
      data: bytes.toString('base64'),
    })
  })
})
