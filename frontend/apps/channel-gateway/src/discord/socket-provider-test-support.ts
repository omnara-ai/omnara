import { createHash } from 'node:crypto'
import { once } from 'node:events'
import { createServer } from 'node:http'
import type { Duplex } from 'node:stream'

import type { JsonBody } from '@omnara/sdk'
import { onTestFinished } from 'vitest'
import { z } from 'zod'

import { config, json } from './test-support'

/** Minimal loopback-only JSON websocket fixture. No provider, SDK strategy mock,
 * test-only dependency resolution or production socket implementation is used.
 */
export async function socketProvider() {
  const connections = new Set<Duplex>()
  const identified: unknown[] = []
  const failures: Error[] = []
  let gatewayURL = ''
  const server = createServer((request, response) => {
    if (request.method === 'GET' && request.headers.authorization === `Bot ${config.botToken}`) {
      if (request.url === '/applications/@me') {
        json(response, { id: config.applicationID, flags: 524288 })
        return
      }
      if (request.url === '/users/@me') {
        json(response, { id: config.botUserID, username: 'Omnara', bot: true })
        return
      }
      if (request.url === '/gateway/bot') {
        json(response, {
          url: gatewayURL,
          shards: 4,
          session_start_limit: {
            total: 100,
            remaining: 50,
            reset_after: 60_000,
            max_concurrency: 2,
          },
        })
        return
      }
    }
    response.writeHead(404).end()
  })
  server.on('upgrade', (request, socket) => {
    const key = z
      .string()
      .regex(/^[A-Za-z0-9+/]{22}==$/)
      .safeParse(request.headers['sec-websocket-key'])
    if (!key.success) {
      socket.destroy()
      return
    }
    connections.add(socket)
    socket.on('close', () => {
      connections.delete(socket)
    })
    socket.on('error', () => undefined)
    const accept = createHash('sha1')
      .update(`${key.data}258EAFA5-E914-47DA-95CA-C5AB0DC85B11`)
      .digest('base64')
    socket.write(
      `HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: ${accept}\r\n\r\n`,
    )
    send(socket, { op: 10, d: { heartbeat_interval: 1000 } })
    let buffered = Buffer.alloc(0)
    socket.on('data', (chunk: Buffer) => {
      buffered = Buffer.concat([buffered, chunk])
      try {
        while (buffered.length >= 2) {
          const opcode = (buffered[0] ?? 0) & 15
          const masked = ((buffered[1] ?? 0) & 128) !== 0
          let length = (buffered[1] ?? 0) & 127
          if (length === 127) throw new Error('fixture frame too large')
          if (length === 126 && buffered.length < 4) return
          const header = length === 126 ? 4 : 2
          if (length === 126) length = buffered.readUInt16BE(2)
          if (!masked || length > 4096) throw new Error('unexpected fixture frame')
          if (buffered.length < header + 4 + length) return
          const mask = buffered.subarray(header, header + 4)
          const payload = Buffer.from(buffered.subarray(header + 4, header + 4 + length))
          for (let i = 0; i < payload.length; i++)
            payload[i] = (payload[i] ?? 0) ^ (mask[i % 4] ?? 0)
          buffered = buffered.subarray(header + 4 + length)
          if (opcode === 8) {
            socket.end(frame(8, payload))
            return
          }
          if (opcode !== 1) throw new Error('unexpected fixture opcode')
          const packet = z
            .object({ op: z.number(), d: z.unknown() })
            .parse(JSON.parse(payload.toString()))
          if (packet.op === 1) send(socket, { op: 11, d: null })
          else if (packet.op === 2) {
            identified.push(packet.d)
            send(socket, {
              op: 0,
              t: 'READY',
              s: 1,
              d: {
                v: 10,
                session_id: 'packaged-worker-session',
                resume_gateway_url: gatewayURL,
                user: {
                  id: config.botUserID,
                  username: 'Omnara',
                  bot: true,
                  avatar: null,
                  discriminator: '0',
                  global_name: null,
                },
                guilds: [],
                application: { id: config.applicationID, flags: 1 << 19, flags_new: '0' },
              },
            })
          } else throw new Error('unexpected fixture packet')
        }
      } catch (cause) {
        failures.push(cause instanceof Error ? cause : new Error('fixture failed'))
        socket.destroy()
      }
    })
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const port = z.object({ port: z.number() }).parse(server.address()).port
  gatewayURL = `ws://127.0.0.1:${port}`
  onTestFinished(async () => {
    for (const socket of connections) socket.destroy()
    server.closeAllConnections()
    await new Promise<void>((resolve, reject) =>
      server.close((error) => {
        if (error) reject(error)
        else resolve()
      }),
    )
  })
  return {
    api: new URL(`http://127.0.0.1:${port}/`),
    gatewayURL,
    identified,
    failures,
    connections,
  }
}

function send(socket: Duplex, value: JsonBody) {
  socket.write(frame(1, Buffer.from(JSON.stringify(value))))
}
function frame(opcode: number, body: Buffer): Buffer {
  const header = Buffer.alloc(body.length < 126 ? 2 : 4)
  header[0] = 128 | opcode
  header[1] = body.length < 126 ? body.length : 126
  if (body.length >= 126) header.writeUInt16BE(body.length, 2)
  return Buffer.concat([header, body])
}
