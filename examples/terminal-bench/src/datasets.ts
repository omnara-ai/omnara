import { execFile } from 'node:child_process'
import { existsSync } from 'node:fs'
import { mkdir, readFile } from 'node:fs/promises'
import { join } from 'node:path'
import { promisify } from 'node:util'

import { parse as parseToml } from 'smol-toml'

const run = promisify(execFile)

export interface Dataset {
  /** Name as used on the CLI, e.g. terminal-bench@2.1. */
  name: string
  repo: string
  ref: string
}

// Harbor-format Terminal-Bench releases. 2.x tasks ship prebuilt images and run
// the verifier in the agent's environment; 3.0+ tasks build from their
// Dockerfile and grade in a separate verifier environment.
export const DATASETS: Record<string, Dataset> = {
  'terminal-bench@2.0': {
    name: 'terminal-bench@2.0',
    repo: 'harbor-framework/terminal-bench-2',
    ref: 'main',
  },
  'terminal-bench@2.1': {
    name: 'terminal-bench@2.1',
    repo: 'harbor-framework/terminal-bench-2-1',
    ref: 'main',
  },
  'terminal-bench@4.0': {
    name: 'terminal-bench@4.0',
    repo: 'harbor-framework/terminal-bench',
    ref: 'v4.0.0',
  },
}

export interface TaskConfig {
  task?: { keywords?: string[] }
  metadata?: { category?: string; subcategory?: string; tags?: string[] }
  artifacts?: (string | { source: string })[]
  verifier?: {
    timeout_sec?: number
    environment_mode?: 'shared' | 'separate'
    environment?: { cpus?: number; memory_mb?: number; gpus?: number; allow_internet?: boolean }
    env?: Record<string, string>
  }
  agent?: { timeout_sec?: number }
  environment?: {
    docker_image?: string
    cpus?: number
    memory_mb?: number
    gpus?: number
    allow_internet?: boolean
    env?: Record<string, string>
  }
}

export interface Task {
  dataset: Dataset
  name: string
  dir: string
  instruction: string
  config: TaskConfig
  verifierMode: 'shared' | 'separate'
  multiContainer: boolean
}

export function resolveDataset(name: string): Dataset {
  const dataset = DATASETS[name]
  if (dataset === undefined) {
    throw new Error(`unknown dataset ${name}; choose one of ${Object.keys(DATASETS).join(', ')}`)
  }
  return dataset
}

function checkoutDir(cacheDir: string, dataset: Dataset): string {
  return join(cacheDir, 'datasets', `${dataset.repo.replace('/', '__')}@${dataset.ref}`)
}

async function git(cwd: string, ...args: string[]): Promise<string> {
  const { stdout } = await run('git', args, { cwd, maxBuffer: 64 * 1024 * 1024 })
  return stdout
}

// One sparse, blobless checkout per dataset; each task directory is added to
// the sparse set on demand, so runs never download the whole benchmark.
const checkoutLocks = new Map<string, Promise<unknown>>()

async function withCheckoutLock<T>(dir: string, fn: () => Promise<T>): Promise<T> {
  const previous = checkoutLocks.get(dir) ?? Promise.resolve()
  const next = previous.then(fn, fn)
  checkoutLocks.set(
    dir,
    next.catch(() => undefined),
  )
  return next
}

async function ensureCheckout(dir: string, dataset: Dataset): Promise<void> {
  if (existsSync(join(dir, '.git'))) return
  await mkdir(dir, { recursive: true })
  await git(
    dir,
    'clone',
    '--filter=blob:none',
    '--no-checkout',
    `https://github.com/${dataset.repo}.git`,
    '.',
  )
  await git(dir, 'sparse-checkout', 'set', '--no-cone', '/tasks/*/task.toml')
  await git(dir, 'checkout', '-q', dataset.ref)
}

export async function fetchTask(cacheDir: string, dataset: Dataset, name: string): Promise<Task> {
  const dir = checkoutDir(cacheDir, dataset)
  await withCheckoutLock(dir, async () => {
    await ensureCheckout(dir, dataset)
    if (!existsSync(join(dir, 'tasks', name, 'instruction.md'))) {
      await git(dir, 'sparse-checkout', 'add', `/tasks/${name}/`)
    }
  })

  const taskDir = join(dir, 'tasks', name)
  if (!existsSync(join(taskDir, 'task.toml'))) {
    throw new Error(`task ${name} not found in ${dataset.name}`)
  }
  const config = parseToml(await readFile(join(taskDir, 'task.toml'), 'utf8')) as TaskConfig
  const instruction = await readFile(join(taskDir, 'instruction.md'), 'utf8')
  return {
    dataset,
    name,
    dir: taskDir,
    instruction,
    config,
    verifierMode: config.verifier?.environment_mode ?? 'shared',
    multiContainer: existsSync(join(taskDir, 'environment', 'docker-compose.yaml')),
  }
}

export async function listTasks(cacheDir: string, dataset: Dataset): Promise<string[]> {
  const dir = checkoutDir(cacheDir, dataset)
  await withCheckoutLock(dir, () => ensureCheckout(dir, dataset))
  const out = await git(dir, 'ls-tree', '-d', '--name-only', `${dataset.ref}:tasks`)
  return out.split('\n').filter((line) => line.length > 0)
}

export interface TaskFilter {
  excludeCategories: string[]
  excludeTags: string[]
}

/**
 * Why a task can't or shouldn't run here, or undefined when it can. Blaxel
 * machines run one container without GPUs, and excluded categories (security
 * by default, where models refuse) are skipped rather than scored as failures.
 */
export function skipReason(task: Task, filter: TaskFilter): string | undefined {
  const category = task.config.metadata?.category?.toLowerCase()
  if (category !== undefined && filter.excludeCategories.includes(category)) return `category ${category}`
  const tags = [...(task.config.metadata?.tags ?? []), ...(task.config.task?.keywords ?? [])].map((tag) =>
    tag.toLowerCase(),
  )
  const tag = tags.find((value) => filter.excludeTags.includes(value))
  if (tag !== undefined) return `tag ${tag}`
  if (task.multiContainer) return 'multi-container (docker-compose)'
  // Blaxel's sandbox-api listens on 8080 inside every machine.
  if (/(?:port\s+|:)8080\b/i.test(task.instruction)) return 'needs port 8080 (taken by Blaxel sandbox-api)'
  if ((task.config.environment?.gpus ?? 0) > 0) return 'needs a GPU'
  return undefined
}
