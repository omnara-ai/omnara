// Plain-English descriptions of tool calls, e.g. "Read notes/todo.md" or "Ran 3 commands".
// Covers Omnara's built-in tools; MCP and integration tools fall back to their humanized name.
import * as z from 'zod'

export type ToolStatus = 'running' | 'done' | 'failed'

// The input fields the descriptions read; a field of another shape counts as missing.
const field = z.string().optional().catch(undefined)
export const toolInputSchema = z
  .object({
    command: field,
    pattern: field,
    path: field,
    source: field,
    query: field,
    url: field,
    name: field,
    agent: field,
  })
  .catch({})
export type ToolInput = z.infer<typeof toolInputSchema>

/** [base, present participle, past] forms of the verb. */
type Verbs = [string, string, string]
interface ToolDef {
  verbs: Verbs
  target: (input: ToolInput) => string
  /** [singular, plural] noun for counting repeated calls, e.g. "Ran 3 commands". */
  noun?: [string, string]
}

const list: Verbs = ['list', 'Listing', 'Listed']
const read: Verbs = ['read', 'Reading', 'Read']
const run: Verbs = ['run', 'Running', 'Ran']
const search: Verbs = ['search', 'Searching', 'Searched']
const stop: Verbs = ['stop', 'Stopping', 'Stopped']
const check: Verbs = ['check', 'Checking', 'Checked']

const str = (value: string | undefined) => value?.trim() ?? ''
/** Keeps long values, like a shell command, to one readable line. */
function short(value: string, max = 60) {
  const line = value.split('\n')[0] ?? ''
  return line.length > max || value.includes('\n') ? `${line.slice(0, max).trimEnd()}…` : line
}
const quoted = (value: string | undefined, fallback: string) =>
  str(value) ? `"${short(str(value))}"` : fallback
const pathOr = (value: string | undefined, fallback: string) =>
  str(value) ? short(str(value)) : fallback

const files: [string, string] = ['file', 'files']

const toolDefs = {
  // Machines and processes
  run_command: {
    verbs: run,
    target: (i) => (str(i.command) ? short(str(i.command)) : 'a command'),
    noun: ['command', 'commands'],
  },
  write_process: {
    verbs: ['send', 'Sending', 'Sent'],
    target: () => 'input to a running command',
  },
  read_process: { verbs: read, target: () => 'output from a command', noun: ['output', 'outputs'] },
  stop_process: { verbs: stop, target: () => 'a running command' },
  watch_process: {
    verbs: ['watch', 'Watching', 'Watched'],
    target: (i) => (str(i.pattern) ? `a command for ${quoted(i.pattern, '')}` : 'a command'),
  },
  list_processes: { verbs: list, target: () => 'running commands' },
  create_machine: { verbs: ['create', 'Creating', 'Created'], target: () => 'a machine' },
  delete_machine: { verbs: ['delete', 'Deleting', 'Deleted'], target: () => 'a machine' },
  list_machines: { verbs: list, target: () => 'available machines' },
  inspect_machine: { verbs: check, target: () => 'a machine' },

  // Files stored in Omnara
  storage_list: {
    verbs: list,
    target: (i) => (str(i.pattern) ? `files matching ${short(str(i.pattern))}` : 'files'),
  },
  storage_read: { verbs: read, target: (i) => pathOr(i.path, 'a file'), noun: files },
  storage_search: {
    verbs: search,
    target: (i) => (str(i.path) ? `files in ${short(str(i.path))}` : 'files'),
  },
  storage_write: {
    verbs: ['write', 'Writing', 'Wrote'],
    target: (i) => pathOr(i.path, 'a file'),
    noun: files,
  },
  storage_upload: {
    verbs: ['save', 'Saving', 'Saved'],
    target: (i) => pathOr(i.source, 'a file from a machine'),
    noun: files,
  },
  storage_download: {
    verbs: ['copy', 'Copying', 'Copied'],
    target: (i) => `${pathOr(i.source, 'a file')} to a machine`,
    noun: files,
  },

  // Web
  web_search: {
    verbs: search,
    target: (i) => `the web for ${quoted(i.query, 'something')}`,
    noun: ['web search', 'web searches'],
  },
  web_fetch: {
    verbs: ['open', 'Opening', 'Opened'],
    target: (i) => pathOr(i.url, 'a page'),
    noun: ['page', 'pages'],
  },

  // People, skills, and other agents
  ask_question: { verbs: ['ask', 'Asking', 'Asked'], target: () => 'you a question' },
  skill: {
    verbs: ['load', 'Loading', 'Loaded'],
    target: (i) => (str(i.name) ? `the ${str(i.name)} skill` : 'a skill'),
  },
  spawn_agent: {
    verbs: ['start', 'Starting', 'Started'],
    target: (i) =>
      str(i.name) || str(i.agent) ? `the ${str(i.name) || str(i.agent)} subagent` : 'a subagent',
    noun: ['subagent', 'subagents'],
  },
  read_agent: { verbs: check, target: () => 'on a subagent' },
  send_agent_message: { verbs: ['message', 'Messaging', 'Messaged'], target: () => 'a subagent' },
  stop_agent: { verbs: stop, target: () => 'a subagent' },
  list_agents: { verbs: list, target: () => 'subagents' },
  tool_search: { verbs: search, target: (i) => `tools for ${quoted(i.pattern, 'a pattern')}` },
} satisfies Record<string, ToolDef>
const tools = new Map<string, ToolDef>(Object.entries(toolDefs))

const humanize = (name: string) => name.replaceAll(/[_-]+/g, ' ').trim()

function toolDef(name: string): ToolDef {
  const known = tools.get(name)
  if (known) return known
  // MCP tools arrive as mcp__{server}__{tool}; server keys never contain underscores.
  const mcp = /^mcp__([A-Za-z][A-Za-z0-9-]*)__(.+)$/.exec(name)
  if (mcp?.[1] && mcp[2]) {
    const tool = humanize(mcp[2])
    const server = mcp[1]
    return { verbs: ['use', 'Using', 'Used'], target: () => `${tool} on ${server}` }
  }
  return { verbs: ['use', 'Using', 'Used'], target: () => humanize(name) }
}

export interface ToolDescription {
  /** The muted verb phrase, e.g. "Ran" or "Couldn't read". */
  verb: string
  /** The emphasized object of the verb, e.g. "npm test". */
  target: string
}

export function describeTool(name: string, input: ToolInput, status: ToolStatus): ToolDescription {
  const { verbs, target } = toolDef(name)
  const [base, present, past] = verbs
  const verb = { running: present, done: past, failed: `Couldn't ${base}` }[status]
  return { verb, target: target(input) }
}

export interface ToolCallItem {
  kind: 'tool'
  name: string
  input: ToolInput
  status: ToolStatus
}
export interface ReasoningItem {
  kind: 'reasoning'
  status: ToolStatus
}
/** One step of a run the chat collapses: a tool call or the model's reasoning between calls. */
export type ActivityItem = ToolCallItem | ReasoningItem

export function describeReasoning(status: ToolStatus) {
  return status === 'running' ? 'Reasoning' : 'Reasoned'
}

/**
 * One line for a run of tool calls and reasoning. Repeated calls of a countable tool collapse
 * into a count ("Ran 3 commands") and reasoning reads once ("Reasoned"); the rest read as their
 * own phrase, up to three before "and N more".
 */
export function summarizeActivity(items: ActivityItem[]) {
  const calls = items.filter((item) => item.kind === 'tool')
  const reasoning = items.filter((item) => item.kind === 'reasoning')
  const phrases: string[] = []
  const counted = new Set<string>()
  for (const item of items) {
    if (item.kind === 'reasoning') {
      const thinking = reasoning.some((step) => step.status === 'running')
      const phrase = describeReasoning(thinking ? 'running' : 'done')
      if (!phrases.includes(phrase)) phrases.push(phrase)
      continue
    }
    const { noun } = toolDef(item.name)
    const sameTool = calls.filter(
      (other) => other.name === item.name && other.status === item.status,
    )
    if (noun && sameTool.length > 1) {
      const key = `${item.name}\u0000${item.status}`
      if (counted.has(key)) continue
      counted.add(key)
      const { verb } = describeTool(item.name, item.input, item.status)
      phrases.push(`${verb} ${String(sameTool.length)} ${noun[1]}`)
      continue
    }
    const { verb, target } = describeTool(item.name, item.input, item.status)
    const phrase = `${verb} ${target}`
    if (!phrases.includes(phrase)) phrases.push(phrase)
  }
  const shown = phrases
    .slice(0, 3)
    .map((phrase, index) =>
      index === 0 ? phrase : phrase.charAt(0).toLowerCase() + phrase.slice(1),
    )
  const more = phrases.length - shown.length
  return shown.join(', ') + (more > 0 ? ` and ${String(more)} more` : '')
}
