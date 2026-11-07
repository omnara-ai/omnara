import { execFileSync, spawnSync } from 'node:child_process'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import process from 'node:process'
import { fileURLToPath } from 'node:url'

import { expect, it } from 'vitest'

const packageRoot = dirname(dirname(fileURLToPath(import.meta.url)))
const require = createRequire(import.meta.url)

it('lets a packed-package consumer emit declarations for the public custom-tool APIs', () => {
  const consumer = mkdtempSync(join(tmpdir(), 'omnara-sdk-consumer-'))
  try {
    // Source imports bypass the published export map and cannot catch private declaration chunks.
    execFileSync('sh', ['-c', 'pnpm build'], { cwd: packageRoot, stdio: 'pipe', timeout: 120_000 })
    const tarball = join(consumer, 'sdk.tgz')
    execFileSync('sh', ['-c', 'pnpm pack --out "$1"', 'pack', tarball], {
      cwd: packageRoot,
      stdio: 'pipe',
      timeout: 120_000,
    })
    const installed = join(consumer, 'node_modules', '@omnara', 'sdk')
    mkdirSync(installed, { recursive: true })
    execFileSync('tar', ['-xzf', tarball, '-C', installed, '--strip-components=1'], {
      timeout: 15_000,
    })

    // Reuse installed dependencies without a network install or workspace link to the SDK source.
    const manifest = JSON.parse(readFileSync(join(packageRoot, 'package.json'), 'utf8'))
    for (const dependency of Object.keys(manifest.dependencies)) {
      const destination = join(consumer, 'node_modules', dependency)
      mkdirSync(dirname(destination), { recursive: true })
      symlinkSync(join(packageRoot, 'node_modules', dependency), destination, 'dir')
    }
    writeFileSync(join(consumer, 'package.json'), JSON.stringify({ type: 'module' }))
    writeFileSync(
      join(consumer, 'consumer.ts'),
      `import { createOmnaraClient, createCustomToolWorker, verifyEventWebhook } from '@omnara/sdk'

export const client = createOmnaraClient()
export const config = client.getConfig()
export const worker = createCustomToolWorker({
  client,
  path: { orgID: 'org_test', projectID: 'proj_test' },
  tools: { example: async () => ({ outcome: 'deferred' }) },
  onError: (error, call) => console.error(error, call?.toolCallID),
})
export const verify = verifyEventWebhook
export const recover = () => worker.check()
`,
    )
    for (const moduleResolution of ['Bundler', 'NodeNext']) {
      const config = join(consumer, 'tsconfig.json')
      writeFileSync(
        config,
        JSON.stringify({
          compilerOptions: {
            strict: true,
            target: 'ES2022',
            module: moduleResolution === 'Bundler' ? 'ESNext' : 'NodeNext',
            moduleResolution,
            lib: ['ES2022', 'DOM'],
            declaration: true,
            emitDeclarationOnly: true,
            skipLibCheck: false,
            outDir: `dist-${moduleResolution}`,
          },
          include: ['consumer.ts'],
        }),
      )
      const compiled = spawnSync(
        process.execPath,
        [require.resolve('typescript/bin/tsc'), '--project', config],
        { cwd: consumer, encoding: 'utf8', timeout: 120_000 },
      )
      const diagnostics = [
        compiled.stdout,
        compiled.stderr,
        compiled.error?.message,
        compiled.signal,
      ]
        .filter(Boolean)
        .join('\n')
      expect(compiled.status, diagnostics).toBe(0)
    }
  } finally {
    rmSync(consumer, { recursive: true, force: true })
  }
}, 240_000)
