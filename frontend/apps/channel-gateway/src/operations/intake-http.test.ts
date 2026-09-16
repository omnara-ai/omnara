import { readdir } from 'node:fs/promises'
import { request as httpRequest } from 'node:http'

import { describe, expect, it, vi } from 'vitest'

import { type OperationsOptions, operationsRoute } from './handler'
import { auth, completed, multipart, start } from './test-support'

describe('rejected operation upload HTTP cleanup', () => {
  it('flushes complete rejection JSON and closes an unfinished artifact upload', async () => {
    const execute = vi.fn<OperationsOptions['execute']>(completed)
    const { port, temporaryDirectory, workBudget } = await start(execute, {
      maxTemporaryBytes: 1,
    })
    const received = await new Promise<{
      status: number
      body: string
      complete: boolean
      connection: string | undefined
    }>((resolve, reject) => {
      let result:
        | {
            status: number
            body: string
            complete: boolean
            connection: string | undefined
          }
        | undefined
      const request = httpRequest(
        {
          host: '127.0.0.1',
          port,
          path: operationsRoute,
          method: 'POST',
          headers: {
            ...auth,
            'content-type': 'multipart/form-data; boundary=boundary',
            'content-length': String(12 * 1024 * 1024),
          },
        },
        (response) => {
          let body = ''
          response.setEncoding('utf8')
          response.on('data', (chunk: string) => {
            body += chunk
          })
          response.on('error', reject)
          response.on('end', () => {
            result = {
              body,
              status: response.statusCode ?? 0,
              complete: response.complete,
              connection: response.headers.connection,
            }
          })
        },
      )
      const timeout = setTimeout(() => {
        const error = new Error('rejected upload socket did not close')
        request.socket?.destroy(error)
        request.destroy(error)
        reject(error)
      }, 1_000)
      request.on('error', reject)
      request.once('socket', (socket) => {
        // ClientRequest close only ends the HTTP exchange; the socket can still
        // hold an incomplete upload until the adapter's bounded drain expires.
        socket.once('close', () => {
          clearTimeout(timeout)
          if (result) resolve(result)
          else reject(new Error('upload socket closed without a complete rejection'))
        })
      })
      // Intentionally leave most of the declared body unsent. Intake rejects
      // the first file bytes; cleanup must not wait for EOF or resume execution.
      request.write(multipart(Buffer.alloc(32 * 1024, 'x')))
    })
    expect(received).toMatchObject({ status: 503, complete: true })
    expect(received.connection).not.toBe('close')
    expect(JSON.parse(received.body)).toEqual({ request_id: 'request-1', outcome: 'failed' })
    expect(execute).not.toHaveBeenCalled()
    expect(workBudget.usedBytes).toBe(0)
    expect(await readdir(temporaryDirectory)).toEqual([])
  })
})
