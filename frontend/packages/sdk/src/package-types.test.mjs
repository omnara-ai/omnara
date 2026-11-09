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

it('supports packed-package consumers with current and minimum TanStack versions', () => {
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
    // A separate process importing only this entry must install the shared-client policy too.
    writeFileSync(
      join(consumer, 'tanstack-only.mjs'),
      `import assert from 'node:assert/strict'
import { listAgentsOptions } from '@omnara/sdk/tanstack'
import { QueryClient } from '@tanstack/react-query'

let attempts = 0
Math.random = () => 0
const queryClient = new QueryClient()
const result = await queryClient.fetchQuery({
  ...listAgentsOptions({
    path: { orgID: 'org_test', projectID: 'proj_test' },
    headers: [['X-Test', 'tuple-header']],
    maxRetries: 1,
    timeoutMs: 1000,
    fetch: async (request) => {
      assert.equal(request.headers.get('X-Test'), 'tuple-header')
      attempts++
      return attempts === 1
        ? Response.json({ error: 'try later' }, { status: 503 })
        : Response.json({ data: [], next_cursor: null })
    },
  }),
  retry: false,
})
assert.equal(attempts, 2)
assert.deepEqual(result, { data: [], next_cursor: null })
queryClient.clear()
`,
    )
    writeFileSync(
      join(consumer, 'consumer.ts'),
      `import { createOmnaraClient, createCustomToolWorker, CustomToolDeliveryError, verifyEventWebhook, sdk, type ListAgentsResponse, type ListAgentsError, type RequestObservation, type CustomToolObservation } from '@omnara/sdk'
import { listAgentsOptions, listAgentsInfiniteOptions, createAgentInputMutation } from '@omnara/sdk/tanstack'
import { QueryClient, useSuspenseInfiniteQuery, useSuspenseQuery, type InfiniteData } from '@tanstack/react-query'

export const observations: RequestObservation[] = []
export const client = createOmnaraClient({ maxRetries: 2, timeoutMs: 30_000, onRequestComplete: (observation) => { observations.push(observation) } })
export const config = client.getConfig()
client.setConfig({ maxRetries: 0, timeoutMs: 0 })
export const agents = listAgentsOptions({ client, path: { orgID: 'org_test', projectID: 'proj_test' }, maxRetries: 1, timeoutMs: 5000, onRequestComplete: (observation) => { observations.push(observation) } })
export const infiniteAgents = listAgentsInfiniteOptions({ client, path: { orgID: 'org_test', projectID: 'proj_test' }, maxRetries: 1, timeoutMs: 5000 })
export const useAgents = () => useSuspenseQuery(agents)
export const useInfiniteAgents = () => useSuspenseInfiniteQuery({ ...infiniteAgents, initialPageParam: { path: infiniteAgents.queryKey[0].path }, getNextPageParam: (page) => page.next_cursor ?? undefined })
export const cachedAgents: ListAgentsResponse | undefined = new QueryClient().getQueryData(agents.queryKey)
export const cachedInfiniteAgents: InfiniteData<ListAgentsResponse> | undefined = new QueryClient().getQueryData(infiniteAgents.queryKey)
export const cachedAgentsError: ListAgentsError | null | undefined = new QueryClient().getQueryState(agents.queryKey)?.error
export const cachedInfiniteAgentsError: ListAgentsError | null | undefined = new QueryClient().getQueryState(infiniteAgents.queryKey)?.error
// @ts-expect-error query keys retain their response-data type.
export const incorrectCachedData: number = new QueryClient().getQueryData(agents.queryKey)
// @ts-expect-error infinite query keys retain their paginated response-data type.
export const incorrectInfiniteData: number = new QueryClient().getQueryData(infiniteAgents.queryKey)
export const inputMutation = createAgentInputMutation({ client, maxRetries: 0, timeoutMs: 1000 })
export const input = () => sdk.createAgentInput({ client, path: { orgID: 'org_test', projectID: 'proj_test', agentID: 'agt_test' }, body: { content_blocks: [{ type: 'text', text: 'hello' }] }, maxRetries: 0, timeoutMs: 1000 })
// @ts-expect-error timeoutMs is a numeric duration, not a duration string.
createOmnaraClient({ timeoutMs: '5s' })
// @ts-expect-error per-request retry options are typed too.
listAgentsOptions({ maxRetries: 'always' })
export const worker = createCustomToolWorker({
  client,
  path: { orgID: 'org_test', projectID: 'proj_test' },
  tools: { example: async () => ({ outcome: 'deferred' }) },
  onError: (error, call) => console.error(error, call?.toolCallID),
  onObserve: (observation: CustomToolObservation) => { console.info(observation.stage) },
})
export const verify = verifyEventWebhook
export const recover = () => worker.check()
export const redeliver = (error: CustomToolDeliveryError) => sdk.submitToolCallResult({
  client,
  path: { orgID: 'org_test', projectID: 'proj_test', agentID: error.call.agentID, toolCallID: error.call.toolCallID },
  body: error.result,
})
`,
    )
    // An exact dev-only alias makes the declared peer floor reproducible and
    // keeps the lockfile's newer version from hiding incompatible declarations.
    for (const dependency of ['@tanstack/react-query-minimum', '@tanstack/react-query']) {
      const source = join(packageRoot, 'node_modules', dependency)
      const { version } = JSON.parse(readFileSync(join(source, 'package.json'), 'utf8'))
      if (dependency.endsWith('-minimum')) {
        expect(manifest.peerDependencies['@tanstack/react-query']).toBe(`^${version}`)
      }
      const destination = join(consumer, 'node_modules', '@tanstack', 'react-query')
      mkdirSync(dirname(destination), { recursive: true })
      rmSync(destination, { force: true })
      symlinkSync(source, destination, 'dir')
      execFileSync(process.execPath, ['tanstack-only.mjs'], {
        cwd: consumer,
        stdio: 'pipe',
        timeout: 15_000,
      })
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
              outDir: `dist-${version}-${moduleResolution}`,
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
          `TanStack ${version}, ${moduleResolution}`,
          compiled.stdout,
          compiled.stderr,
          compiled.error?.message,
          compiled.signal,
        ]
          .filter(Boolean)
          .join('\n')
        expect(compiled.status, diagnostics).toBe(0)
      }
    }
  } finally {
    rmSync(consumer, { recursive: true, force: true })
  }
}, 240_000)
