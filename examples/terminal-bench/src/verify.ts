import { randomBytes } from 'node:crypto'
import { mkdir, readdir, readFile, writeFile } from 'node:fs/promises'
import { join, relative } from 'node:path'

import { SandboxInstance } from '@blaxel/core'

import type { Task } from './datasets.ts'
import { download, exec, execOrThrow, upload } from './machine.ts'
import type { Omnara } from './omnara.ts'

export interface VerifierResult {
  reward: number | null
  rewards?: Record<string, number>
  mode: 'shared' | 'separate'
  exit_code: number | null
  timed_out: boolean
  output_tail: string
}

async function listFiles(dir: string): Promise<string[]> {
  const files: string[] = []
  const walk = async (current: string): Promise<void> => {
    for (const entry of await readdir(current, { withFileTypes: true })) {
      const path = join(current, entry.name)
      if (entry.isDirectory()) await walk(path)
      else if (entry.isFile()) files.push(path)
    }
  }
  await walk(dir)
  return files
}

function parseReward(text: string | undefined, json: string | undefined): Pick<VerifierResult, 'reward' | 'rewards'> {
  if (json !== undefined) {
    const rewards = JSON.parse(json) as Record<string, number>
    return { reward: rewards.reward ?? Object.values(rewards)[0] ?? null, rewards }
  }
  if (text !== undefined && text.trim() !== '') return { reward: Number.parseFloat(text.trim()) }
  return { reward: null }
}

const shellQuote = (value: string) => `'${value.replace(/'/g, `'\\''`)}'`

/**
 * Terminal-Bench 2.x: the verifier runs in the agent's own environment after
 * the agent finishes. Tests are copied in only now, so the agent never saw them.
 */
export async function verifyShared(
  omnara: Omnara,
  task: Task,
  machineID: string,
  workdir: string,
  outDir: string,
): Promise<VerifierResult> {
  const testsDir = join(task.dir, 'tests')
  // Machine commands default to the pool's cwd, which the task image may lack.
  await execOrThrow(omnara, machineID, 'rm -rf /tests && mkdir -p /tests /logs/verifier /logs/agent', { cwd: '/' })
  await Promise.all(
    (await listFiles(testsDir)).map(async (file) =>
      upload(omnara, machineID, `/tests/${relative(testsDir, file)}`, await readFile(file)),
    ),
  )
  const result = await exec(omnara, machineID, 'bash /tests/test.sh', {
    cwd: workdir,
    env: task.config.verifier?.env,
    timeoutSeconds: task.config.verifier?.timeout_sec ?? 600,
  })

  const logs = join(outDir, 'verifier')
  await mkdir(logs, { recursive: true })
  const fetched: Record<string, string> = {}
  for (const name of ['reward.txt', 'reward.json', 'ctrf.json']) {
    try {
      const bytes = await download(omnara, machineID, `/logs/verifier/${name}`)
      fetched[name] = Buffer.from(bytes).toString('utf8')
      await writeFile(join(logs, name), bytes)
    } catch {
      // Verifiers write only some of these.
    }
  }
  await writeFile(join(logs, 'test-output.txt'), result.output)
  return {
    ...parseReward(fetched['reward.txt'], fetched['reward.json']),
    mode: 'shared',
    exit_code: result.exit_code,
    timed_out: result.timed_out,
    output_tail: result.output.slice(-2000),
  }
}

function artifactPaths(task: Task): string[] {
  return (task.config.artifacts ?? []).map((entry) => (typeof entry === 'string' ? entry : entry.source))
}

/**
 * Terminal-Bench 3.0+: copies the task's declared artifacts out of the agent
 * machine and grades them in a fresh Blaxel sandbox booted from the verifier
 * image (tests baked in), like Harbor's separate verifier mode. Grading never
 * runs on Omnara, and the agent never had access to the verifier image.
 */
export async function verifySeparate(
  omnara: Omnara,
  task: Task,
  machineID: string,
  verifierImage: string,
  outDir: string,
): Promise<VerifierResult> {
  const logs = join(outDir, 'verifier')
  await mkdir(logs, { recursive: true })

  let archive: Uint8Array | undefined
  const paths = artifactPaths(task)
  if (paths.length > 0) {
    const relativePaths = paths.map((path) => shellQuote(path.replace(/^\/+/, '').replace(/\/+$/, '')))
    const remote = '/tmp/omnara-tb-artifacts.tgz'
    await execOrThrow(
      omnara,
      machineID,
      `tar -czf ${remote} -C / --ignore-failed-read ${relativePaths.join(' ')} 2>&1 || test -s ${remote}`,
      { cwd: '/' },
    )
    archive = await download(omnara, machineID, remote)
    await writeFile(join(outDir, 'artifacts.tgz'), archive)
  }

  const verifierEnv = task.config.verifier?.environment
  const memory = Math.max(verifierEnv?.memory_mb ?? task.config.environment?.memory_mb ?? 0, 8192)
  const timeoutSec = task.config.verifier?.timeout_sec ?? 600
  const sandbox = await SandboxInstance.create({
    name: `tbv-${task.name.slice(0, 30)}-${randomBytes(4).toString('hex')}`,
    image: verifierImage,
    memory,
    region: 'us-pdx-1',
    // Safety net if this process dies before deleting it.
    ttl: '3h',
    labels: { purpose: 'terminal-bench-verifier', task: task.name.slice(0, 60) },
  })
  try {
    let setup = 'mkdir -p /logs/verifier'
    if (archive !== undefined) {
      await sandbox.fs.writeBinary('/tmp/omnara-tb-artifacts.tgz', archive)
      setup += ' && tar -xzf /tmp/omnara-tb-artifacts.tgz -C /'
    }
    const name = 'verifier'
    await sandbox.process.exec({
      name,
      command: `${setup} && bash /tests/test.sh`,
      env: task.config.verifier?.env,
      timeout: timeoutSec,
      keepAlive: true,
    })
    const finished = await sandbox.process.wait(name, { maxWait: (timeoutSec + 60) * 1000, interval: 5000 })
    const output = await sandbox.process.logs(name, 'all').catch(() => finished.logs ?? '')
    await writeFile(join(logs, 'test-output.txt'), output)

    const fetched: Record<string, string> = {}
    for (const file of ['reward.txt', 'reward.json', 'ctrf.json']) {
      try {
        fetched[file] = await sandbox.fs.read(`/logs/verifier/${file}`)
        await writeFile(join(logs, file), fetched[file])
      } catch {
        // Verifiers write only some of these.
      }
    }
    return {
      ...parseReward(fetched['reward.txt'], fetched['reward.json']),
      mode: 'separate',
      exit_code: finished.status === 'completed' || finished.status === 'failed' ? finished.exitCode : null,
      timed_out: finished.status === 'running' || finished.status === 'killed',
      output_tail: output.slice(-2000),
    }
  } finally {
    await sandbox.delete().catch(() => undefined)
  }
}
