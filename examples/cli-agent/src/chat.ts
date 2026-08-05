import readlineCursor from 'node:readline'
import readline from 'node:readline/promises'

import type {
  AgentEvent,
  AgentInteraction,
  InteractionAnswer,
  ModelOutputDelta,
  OmnaraClient,
} from '@omnara/sdk'
import { AgentEventStreamError, ApiError, openAgentEventStream, sdk } from '@omnara/sdk'

import type { AgentProfileSource } from './bootstrap.js'
import { runConfigCommand } from './commands.js'
import type { CliEnv } from './env.js'
import { promptContentBlocks } from './prompt.js'
import { pick } from './select.js'
import { loadCliState, updateCliState } from './state.js'

type DisplayMode = 'simple' | 'default' | 'full'

const ansi = {
  reset: '\x1b[0m',
  bold: '\x1b[1m',
  dim: '\x1b[2m',
  gray: '\x1b[90m',
  cyan: '\x1b[36m',
  green: '\x1b[32m',
  yellow: '\x1b[33m',
  blue: '\x1b[34m',
  magenta: '\x1b[35m',
  red: '\x1b[31m',
}

function label(name: string, color: string): string {
  return `${ansi.bold}${color}${name}${ansi.reset}`
}

export interface ChatTarget {
  client: OmnaraClient
  env: CliEnv
  orgId: string
  projectId: string
  agentId: string
  profile: AgentProfileSource
}

class Terminal {
  private currentRl: readline.Interface | undefined
  private queued: string[] = []
  private locked = false
  private lastBlank = true
  private status: string | undefined
  private preview: string | undefined
  private renderedRows = 0

  private eraseRegion(): void {
    readlineCursor.cursorTo(process.stdout, 0)
    readlineCursor.clearLine(process.stdout, 0)
    for (let row = 0; row < this.renderedRows; row++) {
      readlineCursor.moveCursor(process.stdout, 0, -1)
      readlineCursor.clearLine(process.stdout, 0)
    }
    this.renderedRows = 0
  }

  private renderRegion(): void {
    if (this.preview != null) {
      console.log(this.preview)
      this.renderedRows += 1
    }
    if (this.status != null) {
      console.log(this.status)
      this.renderedRows += 1
    }
    console.log('')
    this.renderedRows += 1
    this.currentRl?.prompt(true)
  }

  print(text: string): void {
    if (this.locked) {
      this.queued.push(text)
      return
    }
    this.eraseRegion()
    console.log(text)
    this.lastBlank = text.trim() === ''
    this.renderRegion()
  }

  setStatus(text: string | undefined): void {
    if (this.locked) {
      this.status = text
      return
    }
    if (text === this.status) return
    this.eraseRegion()
    this.status = text
    this.renderRegion()
  }

  setPreview(text: string | undefined): void {
    if (this.locked) {
      this.preview = text
      return
    }
    if (text === this.preview) return
    this.eraseRegion()
    this.preview = text
    this.renderRegion()
  }

  repaint(): void {
    if (this.locked) {
      this.currentRl?.prompt(true)
      return
    }
    this.eraseRegion()
    this.renderRegion()
  }

  finishRead(): void {
    const staleRows = this.renderedRows
    this.renderedRows = 0
    if (staleRows > 0) {
      readlineCursor.cursorTo(process.stdout, 0)
      readlineCursor.moveCursor(process.stdout, 0, -(staleRows + 1))
      for (let row = 0; row < staleRows; row++) {
        readlineCursor.clearLine(process.stdout, 0)
        readlineCursor.moveCursor(process.stdout, 0, 1)
      }
      readlineCursor.moveCursor(process.stdout, 0, 1)
      readlineCursor.cursorTo(process.stdout, 0)
    }
    console.log('')
    this.lastBlank = true
  }

  blankLine(): void {
    if (this.locked) {
      const queuedBlank = this.queued.length === 0 ? this.lastBlank : this.queued[this.queued.length - 1]!.trim() === ''
      if (!queuedBlank) this.queued.push('')
      return
    }
    if (!this.lastBlank) this.print('')
  }

  printBlock(text: string): void {
    this.blankLine()
    this.print(text)
  }

  lock(): void {
    this.eraseRegion()
    this.locked = true
  }

  unlock(): void {
    this.locked = false
    const queued = this.queued
    this.queued = []
    for (const text of queued) this.print(text)
  }

  attach(rl: readline.Interface): void {
    this.currentRl = rl
  }

  detach(): void {
    this.currentRl = undefined
  }
}

interface ReadResult {
  line?: string
  interruptedInput?: string
  closed?: boolean
}

type Completer = (line: string) => [string[], string]

function makeCompleter(data: { models: string[]; efforts: string[]; tools: string[] }): Completer {
  const commands = ['/model ', '/effort ', '/permission ', '/display ', '/quit']
  return (line: string) => {
    const wordStart = line.lastIndexOf(' ') + 1
    const word = line.slice(wordStart)
    const candidatesFor = (): string[] => {
      if (!line.startsWith('/')) return []
      if (wordStart === 0) return commands
      const [command] = line.split(/\s+/)
      switch (command) {
        case '/model':
          return data.models
        case '/effort':
          return data.efforts
        case '/permission':
          return [...data.tools, 'ask', 'allow']
        case '/display':
          return ['simple', 'default', 'full']
        default:
          return []
      }
    }
    const hits = candidatesFor().filter((candidate) => candidate.startsWith(word))
    return [hits, word]
  }
}

function readLineOnce(
  terminal: Terminal,
  prompt: string,
  initial: string,
  interrupt: AbortSignal,
  completer?: Completer,
): Promise<ReadResult> {
  return new Promise((resolve) => {
    const rl = readline.createInterface({ input: process.stdin, output: process.stdout, prompt, completer })
    terminal.attach(rl)
    let settled = false
    const settle = (result: ReadResult) => {
      if (settled) return
      settled = true
      interrupt.removeEventListener('abort', onInterrupt)
      if (result.line != null) terminal.finishRead()
      terminal.detach()
      rl.close()
      resolve(result)
    }
    const onInterrupt = () => settle({ interruptedInput: rl.line })
    interrupt.addEventListener('abort', onInterrupt)
    rl.once('line', (line) => settle({ line }))
    rl.once('close', () => settle({ closed: true }))
    rl.once('SIGINT', () => settle({ closed: true }))
    terminal.repaint()
    if (initial !== '') rl.write(initial)
    if (interrupt.aborted) settle({ interruptedInput: rl.line })
  })
}

function abbreviate(text: string, max = 100): string {
  const flat = text.replaceAll(/\s+/g, ' ').trim()
  return flat.length > max ? `${flat.slice(0, max - 1)}…` : flat
}

function toolCallSummary(name: string, input: Record<string, unknown>): string | undefined {
  if (name === 'run_command' && typeof input.command === 'string') return `command: ${abbreviate(input.command)}`
  const entries = Object.entries(input)
  if (entries.length === 0) return undefined
  return abbreviate(entries.map(([key, value]) => `${key}: ${typeof value === 'string' ? value : JSON.stringify(value)}`).join(', '))
}

function printEvent(
  terminal: Terminal,
  event: AgentEvent,
  streamedTextContexts: Set<string>,
  display: DisplayMode,
  toolNames: Map<string, string>,
): void {
  switch (event.event_kind) {
    case 'agent_input':
      return
    case 'model_output': {
      const textAlreadyStreamed = streamedTextContexts.delete(event.model_call_context_id)
      for (const block of event.content_blocks) {
        switch (block.type) {
          case 'text':
            if (!textAlreadyStreamed && block.text.trim() !== '') {
              terminal.printBlock(`${label('agent', ansi.green)} ${block.text}`)
            }
            break
          case 'tool_call': {
            toolNames.set(block.tool_call_id, block.name)
            if (display === 'default') {
              const summary = toolCallSummary(block.name, block.input)
              terminal.printBlock(
                `${label('tool call', ansi.magenta)} ${block.name}` +
                  (summary != null ? `\n  ${ansi.dim}${summary}${ansi.reset}` : ''),
              )
              break
            }
            const header = `${label('tool call', ansi.magenta)} ${block.name} ${ansi.gray}${block.tool_call_id}${ansi.reset}`
            terminal.printBlock(
              display === 'simple'
                ? header
                : `${header}\n  ${ansi.dim}input${ansi.reset} ${JSON.stringify(block.input)}`,
            )
            break
          }
          case 'reasoning':
            if (display === 'full' && block.text.trim() !== '') {
              terminal.printBlock(`${label('thinking', ansi.gray)} ${ansi.dim}${block.text}${ansi.reset}`)
            }
            break
          case 'error':
            terminal.printBlock(`${label('error', ansi.red)} ${block.text}`)
            break
          default:
            break
        }
      }
      return
    }
    case 'tool_result': {
      if (display === 'default') {
        const name = toolNames.get(event.tool_call_id) ?? event.tool_call_id
        const output = event.content_blocks
          .map((block) => (block.type === 'text' ? block.text : block.type === 'structured_data' ? JSON.stringify(block.value) : ''))
          .find((text) => text.trim() !== '')
        terminal.printBlock(
          `${label('tool result', ansi.blue)} ${name} (${event.outcome})` +
            (output != null ? `\n  ${ansi.dim}${abbreviate(output)}${ansi.reset}` : ''),
        )
        return
      }
      const header = `${label('tool result', ansi.blue)} ${event.tool_call_id} (${event.outcome})`
      if (display === 'simple') {
        terminal.printBlock(header)
        return
      }
      const lines: string[] = []
      for (const block of event.content_blocks) {
        if (block.type === 'text' && block.text.trim() !== '') lines.push(block.text)
        if (block.type === 'structured_data') lines.push(JSON.stringify(block.value))
      }
      terminal.printBlock(lines.length > 0 ? `${header} ${lines.join('\n  ')}` : header)
      return
    }
    case 'context_checkpoint':
      terminal.printBlock(`${label('checkpoint', ansi.gray)} context summarized`)
      return
  }
}

class DeltaRenderer {
  private contextId = ''
  private buffer = ''
  private startedBlock = false

  constructor(
    private readonly terminal: Terminal,
    private readonly streamedTextContexts: Set<string>,
  ) {}

  handle(frame: ModelOutputDelta): void {
    const event = frame.event
    switch (event.kind) {
      case 'text_delta': {
        if (this.contextId !== frame.model_call_context_id) {
          this.flush()
          this.contextId = frame.model_call_context_id
        }
        this.streamedTextContexts.add(frame.model_call_context_id)
        this.buffer += event.delta
        this.commitReadyLines()
        this.updatePreview()
        return
      }
      case 'block_stop':
      case 'message_stop':
        this.flush()
        return
      case 'error':
        this.flush()
        this.terminal.printBlock(`${label('error', ansi.red)} ${event.error.message}`)
        return
      default:
        return
    }
  }

  reset(): void {
    this.flush()
    this.contextId = ''
  }

  private lineWidthLimit(): number {
    const columns = process.stdout.columns ?? 80
    return Math.max(20, columns - (this.startedBlock ? 2 : 6) - 1)
  }

  private commitReadyLines(): void {
    for (;;) {
      const limit = this.lineWidthLimit()
      const newline = this.buffer.indexOf('\n')
      if (newline >= 0 && newline <= limit) {
        this.printLine(this.buffer.slice(0, newline))
        this.buffer = this.buffer.slice(newline + 1)
        continue
      }
      if (this.buffer.length > limit) {
        const head = this.buffer.slice(0, limit)
        const space = head.lastIndexOf(' ')
        const cut = space > limit / 2 ? space : limit
        this.printLine(this.buffer.slice(0, cut))
        this.buffer = this.buffer.slice(cut).replace(/^ /, '')
        continue
      }
      return
    }
  }

  private updatePreview(): void {
    if (this.buffer === '' && !this.startedBlock) {
      this.terminal.setPreview(undefined)
      return
    }
    const prefix = this.startedBlock ? '  ' : `${label('agent', ansi.green)} `
    this.terminal.setPreview(`${prefix}${this.buffer}`)
  }

  private flush(): void {
    if (this.buffer.trim() !== '') this.printLine(this.buffer)
    this.buffer = ''
    this.startedBlock = false
    this.terminal.setPreview(undefined)
  }

  private printLine(line: string): void {
    if (line.trim() === '' && !this.startedBlock) return
    if (!this.startedBlock) {
      this.terminal.blankLine()
      this.terminal.print(`${label('agent', ansi.green)} ${line}`)
      this.startedBlock = true
      return
    }
    this.terminal.print(`  ${line}`)
  }
}

async function answerInteraction(
  terminal: Terminal,
  interaction: AgentInteraction,
): Promise<InteractionAnswer[]> {
  const kind = interaction.interaction_kind === 'permission' ? 'approval' : 'question'
  const color = interaction.interaction_kind === 'permission' ? ansi.yellow : ansi.cyan
  const form = interaction.request
  terminal.blankLine()
  terminal.print(`${label(kind, color)} ${form.title}`)
  for (const item of form.context ?? []) terminal.print(`  ${ansi.dim}${item.label}:${ansi.reset} ${item.value}`)
  const answers: InteractionAnswer[] = []
  for (const question of form.questions) {
    const indices = await pick(
      question.prompt,
      question.options.map((option) => ({
        label: option.label,
        hint: option.allows_text === true ? 'accepts text' : undefined,
      })),
      { multiple: question.multiple === true },
    )
    const answer: InteractionAnswer = { option_indices: indices }
    if (indices.some((index) => question.options[index]?.allows_text === true)) {
      const text = await readLineOnce(
        terminal,
        `${ansi.dim}optional text (enter to skip)${ansi.reset} ${ansi.cyan}❯${ansi.reset} `,
        '',
        new AbortController().signal,
      )
      if (text.line != null && text.line.trim() !== '') answer.text = text.line.trim()
    }
    const chosen = answer.option_indices.map((index) => question.options[index]?.label).join(', ')
    terminal.print(`  ${ansi.dim}${question.prompt}${ansi.reset} ${chosen}${answer.text != null ? `: ${answer.text}` : ''}`)
    answers.push(answer)
  }
  return answers
}

const defaultPrompt = `${ansi.bold}${ansi.cyan}❯${ansi.reset} `

export async function runChat(target: ChatTarget): Promise<void> {
  const { client, orgId, projectId, agentId } = target
  const path = { orgID: orgId, projectID: projectId, agentID: agentId }
  const abort = new AbortController()
  const pendingInteractions: AgentInteraction[] = []
  const seenInteractions = new Set<string>()
  const streamedTextContexts = new Set<string>()
  const terminal = new Terminal()
  const deltas = new DeltaRenderer(terminal, streamedTextContexts)
  let interruptRead = new AbortController()
  let display: DisplayMode = loadCliState(target.env).display ?? 'default'
  const toolNames = new Map<string, string>()
  const spinnerFrames = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']
  let spinnerIndex = 0
  let agentState: { text: string; detail?: string } | undefined
  let thinkingTail = ''
  let workStart: number | undefined
  const formatDuration = (ms: number): string => {
    const seconds = Math.max(1, Math.round(ms / 1000))
    if (seconds < 60) return `${seconds}s`
    return `${Math.floor(seconds / 60)}m ${String(seconds % 60).padStart(2, '0')}s`
  }
  const setAgentState = (text: string | undefined, detail?: string): void => {
    agentState = text == null ? undefined : { text, detail }
    if (text != null && workStart == null) workStart = Date.now()
  }
  const finishTurn = (): void => {
    agentState = undefined
    if (workStart == null) return
    const elapsed = Date.now() - workStart
    workStart = undefined
    if (elapsed >= 1000) {
      terminal.printBlock(`${ansi.dim}✔ Worked for ${formatDuration(elapsed)}${ansi.reset}`)
    }
  }
  const spinner = setInterval(() => {
    if (agentState == null) {
      terminal.setStatus(undefined)
      return
    }
    spinnerIndex = (spinnerIndex + 1) % spinnerFrames.length
    const elapsed = workStart != null ? ` ${ansi.dim}(${formatDuration(Date.now() - workStart)})${ansi.reset}` : ''
    const detail = agentState.detail != null ? ` ${ansi.dim}${abbreviate(agentState.detail, 60)}${ansi.reset}` : ''
    terminal.setStatus(`${ansi.cyan}${spinnerFrames[spinnerIndex]}${ansi.reset} ${agentState.text}${elapsed}${detail}`)
  }, 120)

  const completionData = {
    models: [] as string[],
    efforts: ['minimal', 'low', 'medium', 'high'],
    tools: Object.keys(target.profile.tools ?? {}).sort(),
  }
  const completer = makeCompleter(completionData)
  const refreshModelCompletions = async (): Promise<void> => {
    try {
      const names = new Set<string>()
      let cursor: string | undefined
      do {
        const { data } = await sdk.listProjectModelGrants({
          client,
          path: { orgID: orgId, projectID: projectId },
          query: { cursor },
        })
        for (const item of data.data) names.add(item.model.name)
        cursor = data.next_cursor ?? undefined
      } while (cursor != null)
      completionData.models = [...names].sort()
    } catch {}
  }
  void refreshModelCompletions()

  const streamTask = (async () => {
    let afterSequence = 0
    while (!abort.signal.aborted) {
      try {
        const { stream } = await openAgentEventStream({
          client,
          path,
          query: { after_sequence: afterSequence, stream_deltas: true },
          signal: abort.signal,
        })
        for await (const frame of stream) {
          if ('event_kind' in frame) {
            afterSequence = Math.max(afterSequence, frame.sequence)
            if (
              frame.event_kind === 'agent_input' &&
              (frame.input_kind === 'content' || frame.input_kind === 'interaction_response')
            ) {
              setAgentState('working…')
            }
            if (frame.event_kind === 'tool_result') setAgentState('working…')
            if (frame.event_kind === 'model_output') {
              if (frame.stop_reason === 'tool_use') setAgentState('running tools…')
              else finishTurn()
            }
            printEvent(terminal, frame, streamedTextContexts, display, toolNames)
          } else if ('event' in frame) {
            const delta = frame.event
            if (delta.kind === 'block_start') {
              if (delta.block.kind === 'tool_use') {
                toolNames.set(delta.block.tool_call_id, delta.block.tool_name)
                setAgentState(`calling ${delta.block.tool_name}…`)
              } else if (delta.block.kind === 'thinking') {
                thinkingTail = ''
                setAgentState('thinking…')
              } else {
                setAgentState('writing…')
              }
            } else if (delta.kind === 'thinking_delta') {
              thinkingTail = (thinkingTail + delta.delta).slice(-200)
              setAgentState('thinking…', thinkingTail)
            } else if (delta.kind === 'text_delta') {
              setAgentState('writing…')
            } else if (delta.kind === 'message_stop') {
              thinkingTail = ''
              if (delta.stop.reason === 'tool_use') setAgentState('running tools…')
              else finishTurn()
            } else if (delta.kind === 'error') {
              finishTurn()
            }
            deltas.handle(frame)
          } else if ('code' in frame) {
            terminal.printBlock(`${label('stream error', ansi.red)} ${frame.error}`)
          }
        }
      } catch (error) {
        if (abort.signal.aborted) return
        deltas.reset()
        streamedTextContexts.clear()
        if (error instanceof AgentEventStreamError && error.retryable) {
          await new Promise((resolve) => setTimeout(resolve, 1000))
          continue
        }
        terminal.printBlock(`${label('stream error', ansi.red)} ${error instanceof Error ? error.message : String(error)}`)
        abort.abort()
        interruptRead.abort()
        return
      }
    }
  })()

  const pollTask = (async () => {
    while (!abort.signal.aborted) {
      try {
        const { data } = await sdk.listAgentInteractions({ client, path, query: { state: 'open' } })
        let arrived = false
        for (const interaction of data.data) {
          if (seenInteractions.has(interaction.id)) continue
          seenInteractions.add(interaction.id)
          pendingInteractions.push(interaction)
          arrived = true
        }
        if (arrived) interruptRead.abort()
      } catch {}
      await new Promise((resolve) => setTimeout(resolve, 1000))
    }
  })()

  const handleLine = async (line: string): Promise<'quit' | undefined> => {
    if (line === '') return undefined
    if (line === '/quit' || line === '/exit') return 'quit'
    if (line.startsWith('/display')) {
      const [, arg, ...rest] = line.split(/\s+/)
      if ((arg !== 'simple' && arg !== 'default' && arg !== 'full') || rest.length > 0) {
        throw new Error('usage: /display <simple|default|full>')
      }
      display = arg
      updateCliState(target.env, { display })
      terminal.printBlock(`${label('config', ansi.gray)} display mode set to ${display}`)
      return undefined
    }
    if (line.startsWith('/')) {
      terminal.printBlock(`${label('config', ansi.gray)} ${await runConfigCommand(target, line)}`)
      if (line.startsWith('/model')) void refreshModelCompletions()
      return undefined
    }
    const blocks = await promptContentBlocks(line)
    await sdk.createAgentInput({
      client,
      path,
      headers: { 'Idempotency-Key': `cli-input-${new Date().toISOString()}` },
      body: { content_blocks: blocks },
    })
    setAgentState('working…')
    return undefined
  }

  const printError = (error: unknown): void => {
    if (error instanceof ApiError) {
      terminal.printBlock(`${label('error', ansi.red)} HTTP ${error.status} (${error.code ?? 'no code'}): ${error.message}`)
    } else {
      terminal.printBlock(`${label('error', ansi.red)} ${error instanceof Error ? error.message : String(error)}`)
    }
  }

  try {
    let pendingInput = ''
    while (!abort.signal.aborted) {
      const interaction = pendingInteractions.shift()
      if (interaction != null) {
        setAgentState(undefined)
        terminal.setStatus(undefined)
        terminal.lock()
        try {
          const answers = await answerInteraction(terminal, interaction)
          terminal.unlock()
          await sdk.resolveAgentInteraction({
            client,
            path: { ...path, interactionID: interaction.id },
            body: { answers },
          })
          setAgentState('working…')
        } catch (error) {
          terminal.unlock()
          printError(error)
        }
        continue
      }
      if (interruptRead.signal.aborted) interruptRead = new AbortController()
      const result = await readLineOnce(terminal, defaultPrompt, pendingInput, interruptRead.signal, completer)
      pendingInput = ''
      if (result.closed === true) return
      if (result.interruptedInput != null) {
        pendingInput = result.interruptedInput
        continue
      }
      try {
        if ((await handleLine((result.line ?? '').trim())) === 'quit') return
      } catch (error) {
        printError(error)
      }
    }
  } finally {
    clearInterval(spinner)
    terminal.setStatus(undefined)
    abort.abort()
    interruptRead.abort()
    await Promise.allSettled([streamTask, pollTask])
  }
}
