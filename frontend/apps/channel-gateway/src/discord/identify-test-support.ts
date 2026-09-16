import { type ChildProcess, spawn, spawnSync } from 'node:child_process'
import { once } from 'node:events'
import { mkdtemp, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'

import { createClient } from 'redis'
import { afterAll, beforeAll } from 'vitest'

export const redisAvailable =
  spawnSync('redis-server', ['--version'], { stdio: 'ignore' }).status === 0

/** Own one Unix-socket process per suite; never use/flush an existing Redis. */
export function localIdentifyRedis() {
  let directory: string | undefined
  let child: ChildProcess | undefined
  let redis: ReturnType<typeof createClient> | undefined
  beforeAll(async () => {
    directory = await mkdtemp(join(tmpdir(), 'discord-identify-'))
    const socket = join(directory, 'redis.sock')
    const running = spawn(
      'redis-server',
      ['--port', '0', '--unixsocket', socket, '--save', '', '--appendonly', 'no'],
      { stdio: ['ignore', 'pipe', 'pipe'] },
    )
    child = running
    await new Promise<void>((resolve, reject) => {
      const timer = setTimeout(() => {
        reject(new Error('local Redis did not start'))
      }, 5000)
      running.once('error', (error) => {
        clearTimeout(timer)
        reject(error)
      })
      running.stdout.on('data', (chunk: Buffer) => {
        if (chunk.toString().toLowerCase().includes('ready to accept connections')) {
          clearTimeout(timer)
          resolve()
        }
      })
    })
    redis = createClient({
      socket: { path: socket, reconnectStrategy: false, connectTimeout: 1000 },
      disableOfflineQueue: true,
    })
    redis.on('error', () => undefined)
    await redis.connect()
  })
  afterAll(async () => {
    if (redis?.isOpen) redis.destroy()
    if (child?.exitCode === null) {
      const ended = once(child, 'exit')
      child.kill('SIGTERM')
      await ended
    }
    if (directory) await rm(directory, { recursive: true, force: true })
  })
  return () => {
    if (!redis) throw new Error('local Redis is not initialized')
    return redis
  }
}
