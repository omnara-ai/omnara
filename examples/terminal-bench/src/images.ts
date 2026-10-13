import { execFile } from 'node:child_process'
import { createHash } from 'node:crypto'
import { cp, mkdir, readdir, readFile, rename, rm, writeFile } from 'node:fs/promises'
import { dirname, join, relative } from 'node:path'
import { promisify } from 'node:util'

import type { Task } from './datasets.ts'

const run = promisify(execFile)

// Bump when the layer below changes so every task image is rebuilt.
const LAYER_VERSION = 2

const DAEMON_RELEASE_URL = 'https://releases.omnara.com/omnarad/latest'

interface DaemonSeed {
  version: string
  path: string
}

let daemonSeed: Promise<DaemonSeed> | undefined

/**
 * Downloads the released omnarad once and checks its digest. Baked into each
 * image at /usr/local/bin/omnarad, the managed bootstrap installs it from
 * there instead of downloading it inside the machine, which is slow from
 * some sandbox networks.
 */
function fetchDaemonSeed(cacheDir: string): Promise<DaemonSeed> {
  daemonSeed ??= (async () => {
    const manifest = await (await fetch(`${DAEMON_RELEASE_URL}/linux-amd64.txt`)).text()
    const field = (key: string) => {
      const value = manifest.match(new RegExp(`^${key}=(.+)$`, 'm'))?.[1]?.trim()
      if (value === undefined) throw new Error(`omnarad release manifest has no ${key}`)
      return value
    }
    const version = field('version')
    const path = join(cacheDir, 'omnarad', version, 'omnarad-linux-amd64')
    try {
      await readFile(path)
    } catch {
      const response = await fetch(field('url'))
      if (!response.ok) throw new Error(`omnarad download failed: ${response.status}`)
      const bytes = Buffer.from(await response.arrayBuffer())
      const digest = createHash('sha256').update(bytes).digest('hex')
      if (digest !== field('sha256')) throw new Error(`omnarad ${version} digest mismatch`)
      await mkdir(dirname(path), { recursive: true })
      // Concurrent runs share the cache; rename so nobody reads a partial file.
      const partial = `${path}.${process.pid}.partial`
      await writeFile(partial, bytes, { mode: 0o755 })
      await rename(partial, path)
    }
    return { version, path }
  })()
  return daemonSeed
}

export interface TaskImage {
  /** Blaxel image reference for the pool's `image` provider option. */
  image: string
  /** Working directory the task's Dockerfile leaves the agent in. */
  workdir: string
  built: boolean
}

// Blaxel sandboxes boot through Blaxel's sandbox-api, and Omnara's bootstrap
// needs curl plus the omnarad seed binary. The task image is otherwise left
// alone so the agent sees the same environment Harbor would give it.
function runtimeLayer(workdir: string, user: string | undefined): string {
  return [
    '',
    '# --- Omnara on Blaxel runtime layer (examples/terminal-bench) ---',
    'USER root',
    'RUN if ! command -v curl >/dev/null 2>&1; then \\',
    '      if command -v apt-get >/dev/null 2>&1; then apt-get update && apt-get install -y --no-install-recommends curl ca-certificates && rm -rf /var/lib/apt/lists/*; \\',
    '      elif command -v apk >/dev/null 2>&1; then apk add --no-cache curl ca-certificates; \\',
    '      elif command -v dnf >/dev/null 2>&1; then dnf install -y curl; \\',
    '      else echo "no package manager to install curl" >&2; exit 1; fi; \\',
    '    fi',
    'COPY --from=ghcr.io/blaxel-ai/sandbox:latest /sandbox-api /usr/local/bin/sandbox-api',
    'COPY --chmod=0755 omnarad-seed /usr/local/bin/omnarad',
    'ENV HOME=${HOME:-/root}',
    ...(user !== undefined && user !== 'root' ? [`USER ${user}`] : []),
    `WORKDIR ${workdir}`,
    'ENTRYPOINT ["/usr/local/bin/sandbox-api"]',
    '',
  ].join('\n')
}

function lastInstruction(dockerfile: string, keyword: string): string | undefined {
  const pattern = new RegExp(`^\\s*${keyword}\\s+(.+?)\\s*$`, 'gim')
  let value: string | undefined
  for (const match of dockerfile.matchAll(pattern)) value = match[1]
  return value
}

async function hashDirectory(dir: string): Promise<string> {
  const hash = createHash('sha256')
  const walk = async (current: string): Promise<void> => {
    const entries = await readdir(current, { withFileTypes: true })
    entries.sort((a, b) => a.name.localeCompare(b.name))
    for (const entry of entries) {
      const path = join(current, entry.name)
      if (entry.isDirectory()) {
        await walk(path)
      } else if (entry.isFile()) {
        hash.update(relative(dir, path))
        hash.update(await readFile(path))
      }
    }
  }
  await walk(dir)
  return hash.digest('hex')
}

export function imageName(task: Task): string {
  const version = task.dataset.name.split('@')[1]?.replace(/\./g, '') ?? 'x'
  return `tb${version}-${task.name}`.toLowerCase().slice(0, 63)
}

async function existingTags(workspace: string, name: string): Promise<string[]> {
  try {
    const { stdout } = await run('bl', ['get', 'image', `sandbox/${name}`, '-w', workspace, '-o', 'json'])
    const images = JSON.parse(stdout) as { spec?: { tags?: { name: string }[] } }[]
    return images.flatMap((image) => image.spec?.tags?.map((tag) => tag.name) ?? [])
  } catch {
    // `bl get image` exits non-zero when the image doesn't exist yet.
    return []
  }
}

/**
 * Builds the task's environment as a Blaxel sandbox image, once per content
 * hash. 2.x tasks start from their published `docker_image`; newer tasks are
 * built from `environment/` on Blaxel's builders.
 */
export async function ensureTaskImage(
  task: Task,
  options: { workspace: string; cacheDir: string; log: (line: string) => void },
): Promise<TaskImage> {
  if (task.multiContainer) {
    throw new Error(`${task.name} uses docker-compose (multiple containers), which this runner does not support yet`)
  }
  if ((task.config.environment?.gpus ?? 0) > 0) {
    throw new Error(`${task.name} needs a GPU, which Blaxel machines do not have`)
  }

  const envDir = join(task.dir, 'environment')
  const dockerfile = await readFile(join(envDir, 'Dockerfile'), 'utf8')
  const workdir = lastInstruction(dockerfile, 'WORKDIR') ?? '/'
  const user = lastInstruction(dockerfile, 'USER')
  const prebuilt = task.config.environment?.docker_image

  const seed = await fetchDaemonSeed(options.cacheDir)
  const hash = createHash('sha256')
  hash.update(`layer-v${LAYER_VERSION}\nomnarad-${seed.version}\n`)
  hash.update(prebuilt !== undefined ? `image:${prebuilt}` : `dir:${await hashDirectory(envDir)}`)
  const tag = hash.digest('hex').slice(0, 12)
  const name = imageName(task)
  const image = `sandbox/${name}:${tag}`

  if ((await existingTags(options.workspace, name)).includes(tag)) {
    return { image, workdir, built: false }
  }

  const buildDir = join(options.cacheDir, 'images', `${name}-${tag}`)
  await rm(buildDir, { recursive: true, force: true })
  await mkdir(buildDir, { recursive: true })
  let base: string
  if (prebuilt !== undefined) {
    base = `FROM ${prebuilt}\n`
  } else {
    await cp(envDir, buildDir, { recursive: true })
    base = dockerfile
  }
  await cp(seed.path, join(buildDir, 'omnarad-seed'))
  await writeFile(join(buildDir, 'Dockerfile'), base + runtimeLayer(workdir, user))
  const built = await pushImage({ name, tag, buildDir, workspace: options.workspace, log: options.log })
  return { image, workdir, built }
}

/** Pushes buildDir as sandbox/<name>:<tag>; false when someone else already did. */
async function pushImage(options: {
  name: string
  tag: string
  buildDir: string
  workspace: string
  log: (line: string) => void
}): Promise<boolean> {
  const { name, tag, buildDir, workspace } = options
  await writeFile(join(buildDir, 'blaxel.toml'), `type = "sandbox"\nname = "${name}"\n\n[build]\nslim = false\n`)
  options.log(`building sandbox/${name}:${tag} on Blaxel`)
  try {
    await run(
      'bl',
      // bl push packages its working directory regardless of -d, so run it there.
      ['push', '-w', workspace, '-y', '--type', 'sandbox', '--name', name, '--tag', tag, '--timeout', '45m'],
      { cwd: buildDir, maxBuffer: 256 * 1024 * 1024 },
    )
    return true
  } catch (error) {
    // Another runner may have pushed the same immutable tag first.
    if ((await existingTags(workspace, name)).includes(tag)) return false
    const output = error as { stdout?: string; stderr?: string }
    const tail = `${output.stdout ?? ''}\n${output.stderr ?? ''}`.trim().split('\n').slice(-30).join('\n')
    throw new Error(`bl push failed for sandbox/${name}:${tag}:\n${tail}`)
  }
}

/**
 * Builds a 3.0+ task's verifier environment (tests/Dockerfile, tests baked
 * in) as a Blaxel sandbox image. It lives in Blaxel, never in the Omnara pool,
 * so the agent has no way to boot it.
 */
export async function ensureVerifierImage(
  task: Task,
  options: { workspace: string; cacheDir: string; log: (line: string) => void },
): Promise<string> {
  const testsDir = join(task.dir, 'tests')
  const dockerfile = await readFile(join(testsDir, 'Dockerfile'), 'utf8')
  const hash = createHash('sha256')
  hash.update(`verifier-layer-v${LAYER_VERSION}\n${await hashDirectory(testsDir)}`)
  const tag = hash.digest('hex').slice(0, 12)
  const version = task.dataset.name.split('@')[1]?.replace(/\./g, '') ?? 'x'
  const name = `tb${version}v-${task.name}`.toLowerCase().slice(0, 63)
  if ((await existingTags(options.workspace, name)).includes(tag)) return `sandbox/${name}:${tag}`

  const buildDir = join(options.cacheDir, 'images', `${name}-${tag}`)
  await rm(buildDir, { recursive: true, force: true })
  await cp(testsDir, buildDir, { recursive: true })
  const layer = [
    '',
    '# --- Blaxel runtime layer (examples/terminal-bench) ---',
    'USER root',
    'COPY --from=ghcr.io/blaxel-ai/sandbox:latest /sandbox-api /usr/local/bin/sandbox-api',
    'RUN mkdir -p /logs/verifier',
    'ENTRYPOINT ["/usr/local/bin/sandbox-api"]',
    '',
  ].join('\n')
  await writeFile(join(buildDir, 'Dockerfile'), dockerfile + layer)
  await pushImage({ name, tag, buildDir, workspace: options.workspace, log: options.log })
  return `sandbox/${name}:${tag}`
}
