import { execFile } from 'node:child_process'
import { readFile } from 'node:fs/promises'
import { promisify } from 'node:util'

import { transform } from 'esbuild'
import { expect, it } from 'vitest'

it('exits with a live worker after provider shutdown has exhausted its deadline', async () => {
  const source = await readFile(new URL('./fatal-runtime.ts', import.meta.url), 'utf8')
  const { code } = await transform(source, { loader: 'ts', format: 'esm', target: 'node24' })
  const moduleURL = `data:text/javascript;base64,${Buffer.from(code).toString('base64')}`
  const child = `
    import { Worker } from 'node:worker_threads'
    import { terminateGatewayRuntime } from ${JSON.stringify(moduleURL)}
    const worker = new Worker('setInterval(() => {}, 1000)', { eval: true })
    worker.on('online', () => terminateGatewayRuntime(new Error('private provider diagnostics')))
  `
  await expect(
    promisify(execFile)(process.execPath, ['--input-type=module', '-e', child], { timeout: 3000 }),
  ).rejects.toMatchObject({
    code: 1,
    killed: false,
    signal: null,
    stderr: 'channel gateway: provider runtime could not stop; exiting for replacement\n',
  })
})
