import { mkdir, readFile, writeFile } from 'node:fs/promises'
import { existsSync } from 'node:fs'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'

import YAML from 'yaml'

import { type Dataset, fetchTask, listTasks, resolveDataset, skipReason, type Task } from './datasets.ts'
import { ensureTaskImage, ensureVerifierImage, type TaskImage } from './images.ts'
import {
  archiveAgent,
  connect,
  type ExistingAgent,
  createRunProfile,
  finalMessage,
  finalStopReason,
  launchTaskAgent,
  listAllEvents,
  listRunAgents,
  type Omnara,
  sendInstruction,
  summarizeUsage,
  waitForAgent,
  waitForMachineOnline,
} from './omnara.ts'
import { verifySeparate, verifyShared } from './verify.ts'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const list = (value: string | undefined) =>
  (value ?? '')
    .split(',')
    .map((item) => item.trim())
    .filter(Boolean)

const { values } = parseArgs({
  options: {
    // Comma-separated; every dataset × model pair is its own run and profile.
    dataset: { type: 'string', default: 'terminal-bench@2.1' },
    model: { type: 'string', default: 'anthropic/claude-opus-5.5' },
    tasks: { type: 'string' },
    'all-tasks': { type: 'boolean', default: false },
    'exclude-categories': { type: 'string', default: 'security' },
    'exclude-tags': { type: 'string', default: 'security' },
    'provider-config': { type: 'string', default: 'omnara-openrouter' },
    'reasoning-effort': { type: 'string' },
    pool: { type: 'string', default: 'terminal-bench' },
    'blaxel-workspace': { type: 'string', default: process.env.BL_WORKSPACE ?? 'omnara-terminal-bench' },
    'min-memory-mb': { type: 'string', default: '4096' },
    // Agents (and machines) in flight across all runs.
    concurrency: { type: 'string', default: '16' },
    'build-concurrency': { type: 'string', default: '6' },
    'verifier-concurrency': { type: 'string', default: '16' },
    name: { type: 'string' },
    // Comma-separated runs/<run> directories to continue instead of starting new runs.
    resume: { type: 'string' },
    // With --resume: rerun tasks whose agent hit its timeout (e.g. after fixing a slow harness).
    'retry-timeouts': { type: 'boolean', default: false },
    org: { type: 'string' },
    project: { type: 'string' },
    'machine-timeout-sec': { type: 'string', default: '600' },
  },
})

class Semaphore {
  private waiting: (() => void)[] = []
  constructor(private available: number) {}
  async run<T>(fn: () => Promise<T>): Promise<T> {
    if (this.available > 0) this.available--
    else await new Promise<void>((resolve) => this.waiting.push(resolve))
    try {
      return await fn()
    } finally {
      const next = this.waiting.shift()
      if (next !== undefined) next()
      else this.available++
    }
  }
}

/** Caches one in-flight or settled promise per key. */
function memo<T>(fn: (key: string) => Promise<T>): (key: string) => Promise<T> {
  const cache = new Map<string, Promise<T>>()
  return (key) => {
    let value = cache.get(key)
    if (value === undefined) {
      value = fn(key)
      cache.set(key, value)
    }
    return value
  }
}

interface Run {
  name: string
  dataset: Dataset
  model: string
  profileID: string
  base: Record<string, unknown>
  outDir: string
  tasks: Task[]
  skipped: { task: string; reason: string }[]
  /** Agents already under the profile when resuming, by agent name. */
  existing: Map<string, ExistingAgent>
}

interface RunFile {
  name: string
  profile_id: string
  dataset: string
  model: string
  base: Record<string, unknown>
}

interface TaskResult {
  task: string
  status: 'graded' | 'error'
  reward: number | null
  agent_outcome?: string
  stop_reason?: string
  agent_id?: string
  machine_id?: string
  image?: string
  verifier?: unknown
  usage?: unknown
  final_message?: string
  error?: string
  started_at: string
  finished_at?: string
}

const buildSlots = new Semaphore(Number(values['build-concurrency']))
// Blaxel grading sandboxes for 3.0+ tasks.
const verifierSlots = new Semaphore(Number(values['verifier-concurrency']))

const log = (line: string) => console.log(`${new Date().toISOString().slice(11, 19)} ${line}`)

function runName(dataset: Dataset, model: string, stamp: string): string {
  const short = model.split('/').pop() ?? model
  return `${values.name ?? `tb${dataset.name.split('@')[1]}`} ${short} ${stamp}`.slice(0, 64)
}

function agentName(run: string, task: string): string {
  const name = `${run} · ${task}`
  return name.length <= 64 ? name : `${run.slice(0, 63 - task.length - 3)}… · ${task}`
}

const taskKey = (task: Task) => `${task.dataset.name}/${task.name}`

async function main(): Promise<void> {
  // The Blaxel SDK (verifier sandboxes) reads its workspace from here.
  process.env.BL_WORKSPACE = values['blaxel-workspace']
  const omnara = connect({ orgID: values.org, projectID: values.project })
  const cacheDir = join(root, '.cache')
  const datasets = list(values.dataset).map(resolveDataset)
  const models = list(values.model)
  const filter = { excludeCategories: list(values['exclude-categories']), excludeTags: list(values['exclude-tags']) }
  const stamp = new Date().toISOString().replace(/[-:]/g, '').replace(/\..+$/, '').replace('T', '-')

  const taskByKey = new Map<string, Task>()
  const image = memo<TaskImage>((key) =>
    buildSlots.run(() =>
      ensureTaskImage(taskByKey.get(key)!, {
        workspace: values['blaxel-workspace']!,
        cacheDir,
        log: (line) => log(`[${key}] ${line}`),
      }),
    ),
  )
  const verifierImage = memo<string>((key) =>
    buildSlots.run(() =>
      ensureVerifierImage(taskByKey.get(key)!, {
        workspace: values['blaxel-workspace']!,
        cacheDir,
        log: (line) => log(`[${key}] ${line}`),
      }),
    ),
  )

  // Resolve tasks once per dataset, then filter.
  const tasksFor = memo(async (name: string) => {
    const dataset = resolveDataset(name)
    const names = values['all-tasks'] ? await listTasks(cacheDir, dataset) : list(values.tasks)
    if (names.length === 0) throw new Error('pass --tasks a,b,c or --all-tasks')
    const fetched = await Promise.all(names.map((task) => fetchTask(cacheDir, dataset, task)))
    const tasks: Task[] = []
    const skipped: Run['skipped'] = []
    for (const task of fetched) {
      const reason = skipReason(task, filter)
      if (reason === undefined) {
        tasks.push(task)
        taskByKey.set(taskKey(task), task)
      } else skipped.push({ task: task.name, reason })
    }
    log(`${dataset.name}: ${tasks.length} task(s) to run, ${skipped.length} skipped`)
    return { dataset, tasks, skipped }
  })

  const runs: Run[] = []
  if (values.resume !== undefined) {
    for (const dir of list(values.resume)) {
      const outDir = resolve(dir)
      const file = JSON.parse(await readFile(join(outDir, 'run.json'), 'utf8')) as RunFile
      const { dataset, tasks, skipped } = await tasksFor(file.dataset)
      const existing = await listRunAgents(omnara, file.profile_id)
      runs.push({ name: file.name, dataset, model: file.model, profileID: file.profile_id, base: file.base, outDir, tasks, skipped, existing })
      log(`resuming "${file.name}" (profile ${file.profile_id}, ${existing.size} active agent(s))`)
    }
  } else {
    for (const datasetName of datasets.map((dataset) => dataset.name)) {
      const { dataset, tasks, skipped } = await tasksFor(datasetName)
      for (const model of models) {
        const name = runName(dataset, model, stamp)
        const base = YAML.parse(await readFile(join(root, 'agent.yaml'), 'utf8')) as Record<string, unknown>
        base.model = {
          provider_config: values['provider-config'],
          name: model,
          ...(values['reasoning-effort'] !== undefined ? { reasoning: { effort: values['reasoning-effort'] } } : {}),
        }
        // The profile's own config uses the pool's default image; each task's
        // agent runs a derived config with that task's image instead.
        const profileID = await createRunProfile(
          omnara,
          name,
          YAML.stringify({ ...base, machine_sources: [{ machine_pool_name: values.pool, max_machines: 1 }] }),
        )
        const outDir = join(root, 'runs', name.replace(/[^A-Za-z0-9._-]+/g, '_'))
        await mkdir(outDir, { recursive: true })
        const file: RunFile = { name, profile_id: profileID, dataset: dataset.name, model, base }
        await writeFile(join(outDir, 'run.json'), JSON.stringify(file, null, 2))
        runs.push({ name, dataset, model, profileID, base, outDir, tasks, skipped, existing: new Map() })
        log(`run "${name}" → profile ${profileID}`)
      }
    }
  }

  // Interleave runs so they progress together under one machine budget.
  const queue: { run: Run; task: Task }[] = []
  const longest = Math.max(...runs.map((run) => run.tasks.length))
  for (let i = 0; i < longest; i++) for (const run of runs) if (run.tasks[i] !== undefined) queue.push({ run, task: run.tasks[i]! })
  log(`${queue.length} agent(s) across ${runs.length} run(s), ${values.concurrency} at a time`)

  const results = new Map<Run, TaskResult[]>(runs.map((run) => [run, []]))
  const agentSlots = new Semaphore(Number(values.concurrency))
  await Promise.all(
    queue.map(({ run, task }) =>
      (async () => {
        // Builds start right away; a machine slot is taken only once they're done.
        const ready = Promise.all([
          image(taskKey(task)),
          task.verifierMode === 'separate' ? verifierImage(taskKey(task)) : undefined,
        ])
        ready.catch(() => undefined)
        const result = await agentSlots.run(() => runTask(omnara, run, task, ready))
        results.get(run)!.push(result)
      })(),
    ),
  )

  const overall = []
  for (const run of runs) {
    const runResults = results.get(run)!
    const solved = runResults.filter((result) => (result.reward ?? 0) >= 1).length
    const summary = {
      run: run.name,
      profile_id: run.profileID,
      dataset: run.dataset.name,
      model: run.model,
      tasks: runResults.length,
      graded: runResults.filter((result) => result.status === 'graded').length,
      solved,
      accuracy: runResults.length > 0 ? solved / runResults.length : 0,
      skipped: run.skipped,
      results: runResults
        .sort((a, b) => a.task.localeCompare(b.task))
        .map(({ task, status, reward, agent_outcome, stop_reason, agent_id, error }) => ({
          task,
          status,
          reward,
          agent_outcome,
          stop_reason,
          agent_id,
          error,
        })),
    }
    await writeFile(join(run.outDir, 'summary.json'), JSON.stringify(summary, null, 2))
    overall.push({
      run: run.name,
      solved: `${solved}/${runResults.length}`,
      accuracy: `${(summary.accuracy * 100).toFixed(1)}%`,
      errors: runResults.length - summary.graded,
      skipped: run.skipped.length,
    })
  }
  console.table(overall)
}

async function runTask(
  omnara: Omnara,
  run: Run,
  task: Task,
  ready: Promise<[TaskImage, string | undefined]>,
): Promise<TaskResult> {
  const outDir = join(run.outDir, task.name)
  const prefix = `[${run.name} · ${task.name}]`
  const existing = run.existing.get(agentName(run.name, task.name))
  const priorPath = join(outDir, 'result.json')
  if (existsSync(priorPath)) {
    const prior = JSON.parse(await readFile(priorPath, 'utf8')) as TaskResult
    const retry = values['retry-timeouts'] && prior.agent_outcome === 'timed_out'
    if (prior.status === 'graded' && !retry) {
      if (existing !== undefined) await archiveAgent(omnara, existing.agentID).catch(() => undefined)
      return prior
    }
  }
  const result: TaskResult = { task: task.name, status: 'error', reward: null, started_at: new Date().toISOString() }
  await mkdir(outDir, { recursive: true })
  try {
    const [image, verifierImage] = await ready
    result.image = image.image

    let launched: { agentID: string; machineID: string } | undefined
    let instructedAt: number | undefined
    if (existing?.machineID !== undefined) {
      launched = { agentID: existing.agentID, machineID: existing.machineID }
      instructedAt = existing.instructedAt
      log(`${prefix} adopting agent ${existing.agentID}`)
    } else {
      // An agent without a machine can't continue; start the task over.
      if (existing !== undefined) await archiveAgent(omnara, existing.agentID).catch(() => undefined)
      const memory = Math.max(task.config.environment?.memory_mb ?? 0, Number(values['min-memory-mb']))
      const env = task.config.environment?.env
      const source = YAML.stringify({
        ...run.base,
        machine_sources: [
          {
            machine_pool_name: values.pool,
            max_machines: 1,
            initial_num_machines: 1,
            delete_after_idle_minutes: 30,
            cwd: image.workdir,
            machine_memory_mb: memory,
            machine_provider_options_overlay: { image: image.image },
            ...(env !== undefined && Object.keys(env).length > 0 ? { env_overlay: env } : {}),
          },
        ],
      })
      launched = await launchTaskAgent(omnara, {
        profileID: run.profileID,
        name: agentName(run.name, task.name),
        configSource: source,
      })
    }
    result.agent_id = launched.agentID
    result.machine_id = launched.machineID

    if (instructedAt === undefined) {
      await waitForMachineOnline(omnara, launched.machineID, Number(values['machine-timeout-sec']) * 1000)
      await sendInstruction(omnara, launched.agentID, task.instruction)
      instructedAt = Date.now()
    }
    // The agent timeout counts from the instruction, also for adopted agents.
    const timeoutSec = task.config.agent?.timeout_sec ?? 3600
    log(`${prefix} agent ${launched.agentID} working (timeout ${timeoutSec}s)`)
    result.agent_outcome = await waitForAgent(omnara, launched.agentID, instructedAt + timeoutSec * 1000)

    const verifier =
      task.verifierMode === 'separate'
        ? await verifierSlots.run(() => verifySeparate(omnara, task, launched.machineID, verifierImage!, outDir))
        : await verifyShared(omnara, task, launched.machineID, image.workdir, outDir)
    result.verifier = verifier
    result.reward = verifier.reward
    result.status = 'graded'

    const events = await listAllEvents(omnara, launched.agentID)
    await writeFile(join(outDir, 'events.json'), JSON.stringify(events, null, 2))
    result.usage = summarizeUsage(events)
    result.final_message = finalMessage(events)
    result.stop_reason = finalStopReason(events)
    log(`${prefix} reward ${verifier.reward} (${result.agent_outcome}, ${result.stop_reason})`)
  } catch (error) {
    result.error = error instanceof Error ? error.message : String(error)
    log(`${prefix} error: ${result.error.split('\n')[0]}`)
  }
  // Free the pool machine for the next task; the agent and its events remain.
  if (result.agent_id !== undefined) await archiveAgent(omnara, result.agent_id).catch(() => undefined)
  result.finished_at = new Date().toISOString()
  await writeFile(join(outDir, 'result.json'), JSON.stringify(result, null, 2))
  return result
}

main().catch((error) => {
  console.error(error instanceof Error ? error.message : error)
  process.exit(1)
})
