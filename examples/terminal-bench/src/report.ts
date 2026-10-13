// Builds a per-task table across runs: solved, failed, refused, not possible
// here (skipped or infrastructure error), or still pending.
//
//   pnpm tb:report runs/<run> [runs/<run> ...] [--out runs/report.md]
import { existsSync } from 'node:fs'
import { readFile, writeFile } from 'node:fs/promises'
import { basename, dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { parseArgs } from 'node:util'

import { fetchTask, listTasks, resolveDataset, skipReason } from './datasets.ts'

const root = join(dirname(fileURLToPath(import.meta.url)), '..')
const { values, positionals } = parseArgs({
  allowPositionals: true,
  options: {
    out: { type: 'string', default: join(root, 'runs', 'report.md') },
    'exclude-categories': { type: 'string', default: 'security' },
    'exclude-tags': { type: 'string', default: 'security' },
  },
})
const list = (value: string | undefined) => (value ?? '').split(',').map((item) => item.trim()).filter(Boolean)

interface RunFile {
  name: string
  dataset: string
  model: string
}

interface Result {
  status: 'graded' | 'error'
  reward: number | null
  stop_reason?: string
  agent_outcome?: string
  final_message?: string
  error?: string
}

type Outcome = 'solved' | 'failed' | 'refused' | 'skipped' | 'infra error' | 'timed out' | 'pending'

const REFUSAL_TEXT = /usage policy|violative|can(?:'|no)t (?:help|assist) with (?:this|that)|i won't help/i

function classify(result: Result | undefined): { outcome: Outcome; note: string } {
  if (result === undefined) return { outcome: 'pending', note: '' }
  if (result.status === 'error') {
    const error = result.error ?? ''
    // Launch-time pool exhaustion is retried by --resume, so it isn't final.
    if (error.includes('capacity exceeded')) return { outcome: 'pending', note: '' }
    const note = error.includes('bl push failed')
      ? `image build failed (${error.match(/sandbox\/(tb\d+v?)-/)?.[1]?.endsWith('v') ? 'verifier' : 'task'})`
      : error.split('\n')[0]!.slice(0, 80)
    return { outcome: 'infra error', note }
  }
  if (
    result.stop_reason === 'refusal' ||
    result.stop_reason === 'content_filter' ||
    REFUSAL_TEXT.test(result.final_message?.slice(0, 600) ?? '')
  ) {
    return { outcome: 'refused', note: result.stop_reason ?? 'refusal text' }
  }
  if ((result.reward ?? 0) >= 1) return { outcome: 'solved', note: '' }
  if (result.agent_outcome === 'timed_out') return { outcome: 'timed out', note: '' }
  return { outcome: 'failed', note: result.reward === null ? 'no reward written' : '' }
}

const EMOJI: Record<Outcome, string> = {
  solved: '✅ solved',
  failed: '❌ failed',
  refused: '🛑 refused',
  skipped: '⏭️ skipped',
  'infra error': '⚠️ infra',
  'timed out': '⏱️ timed out',
  pending: '… pending',
}

const runDirs = positionals.length > 0 ? positionals.map((dir) => resolve(dir)) : []
if (runDirs.length === 0) {
  console.error('usage: pnpm tb:report runs/<run> [runs/<run> ...]')
  process.exit(1)
}

const runs = await Promise.all(
  runDirs.map(async (dir) => ({ dir, file: JSON.parse(await readFile(join(dir, 'run.json'), 'utf8')) as RunFile })),
)
const filter = { excludeCategories: list(values['exclude-categories']), excludeTags: list(values['exclude-tags']) }
const cacheDir = join(root, '.cache')
const sections: string[] = []
const totals: string[] = []

for (const datasetName of [...new Set(runs.map((run) => run.file.dataset))]) {
  const dataset = resolveDataset(datasetName)
  const datasetRuns = runs.filter((run) => run.file.dataset === datasetName)
  const models = datasetRuns.map((run) => run.file.model.split('/').pop()!)
  const names = await listTasks(cacheDir, dataset)
  const tasks = await Promise.all(names.map((name) => fetchTask(cacheDir, dataset, name)))

  const rows: string[] = []
  const counts = datasetRuns.map(() => new Map<Outcome, number>())
  for (const task of tasks.sort((a, b) => a.name.localeCompare(b.name))) {
    const skip = skipReason(task, filter)
    const cells: string[] = []
    const notes = new Set<string>()
    for (const [index, run] of datasetRuns.entries()) {
      let outcome: Outcome
      if (skip !== undefined) {
        outcome = 'skipped'
        notes.add(skip)
      } else {
        const path = join(run.dir, task.name, 'result.json')
        const result = existsSync(path) ? (JSON.parse(await readFile(path, 'utf8')) as Result) : undefined
        const classified = classify(result)
        outcome = classified.outcome
        if (classified.note !== '') notes.add(`${models[index]}: ${classified.note}`)
      }
      counts[index]!.set(outcome, (counts[index]!.get(outcome) ?? 0) + 1)
      cells.push(EMOJI[outcome])
    }
    const category = task.config.metadata?.category ?? ''
    rows.push(`| ${task.name} | ${category} | ${cells.join(' | ')} | ${[...notes].join('; ')} |`)
  }

  sections.push(
    [
      `## ${dataset.name}`,
      '',
      `| Task | Category | ${models.join(' | ')} | Notes |`,
      `| --- | --- | ${models.map(() => '---').join(' | ')} | --- |`,
      ...rows,
    ].join('\n'),
  )
  for (const [index, run] of datasetRuns.entries()) {
    const count = (outcome: Outcome) => counts[index]!.get(outcome) ?? 0
    const attempted = tasks.length - count('skipped')
    totals.push(
      `| ${dataset.name} | ${models[index]} | ${count('solved')}/${attempted} | ${count('failed')} | ${count('refused')} | ${count('timed out')} | ${count('infra error')} | ${count('skipped')} | ${count('pending')} |`,
    )
    void run
  }
}

const report = [
  '# Terminal-Bench on Omnara',
  '',
  `Runs: ${runs.map((run) => `\`${basename(run.dir)}\``).join(', ')}. Generated ${new Date().toISOString()}.`,
  '',
  '**Refused** means the model declined or the provider blocked it (stop reason `refusal`/`content_filter`).',
  '**Skipped** and **infra** are tasks this environment can\'t run: security tasks excluded on purpose,',
  'multi-container or GPU tasks Blaxel machines can\'t host, and image builds that failed.',
  '',
  '| Dataset | Model | Solved | Failed | Refused | Timed out | Infra | Skipped | Pending |',
  '| --- | --- | --- | --- | --- | --- | --- | --- | --- |',
  ...totals,
  '',
  ...sections.flatMap((section) => [section, '']),
].join('\n')

await writeFile(values.out!, report)
console.log(report.split('\n').slice(0, 12 + totals.length).join('\n'))
console.log(`\nfull table: ${values.out}`)
