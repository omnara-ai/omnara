import type {
  AgentEvent,
  AgentInteraction,
  InteractionAnswer,
  ListAgentEventsResponse,
  ModelOutputDelta,
  OmnaraClient,
} from '@omnara/sdk'
import { AgentEventStreamError, ApiError, openAgentEventStream, sdk } from '@omnara/sdk'

import { pick } from './select.ts'
import { ansi, label, readLineOnce, Terminal, terminalColumns } from './terminal.ts'

export interface ChatTarget {
  client: OmnaraClient
  orgId: string
  projectId: string
  agentId: string
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

interface ToolCallInfo {
  name: string
  summary?: string
}

function printEvent(
  terminal: Terminal,
  event: AgentEvent,
  streamedTextContexts: Set<string>,
  toolCalls: Map<string, ToolCallInfo>,
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
          case 'tool_call':
            toolCalls.set(block.tool_call_id, {
              name: block.name,
              summary: toolCallSummary(block.name, block.input),
            })
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
      const call = toolCalls.get(event.tool_call_id)
      toolCalls.delete(event.tool_call_id)
      const name = call?.name ?? event.tool_call_id
      const output = event.content_blocks
        .map((block) => (block.type === 'text' ? block.text : block.type === 'structured_data' ? JSON.stringify(block.value) : ''))
        .find((text) => text.trim() !== '')
      const outcomeColor =
        event.outcome === 'succeeded' ? ansi.green : event.outcome === 'canceled' ? ansi.yellow : ansi.red
      terminal.printBlock(
        `${label('tool', ansi.magenta)} ${name} ${outcomeColor}(${event.outcome})${ansi.reset}` +
          (call?.summary != null ? `\n  ${ansi.dim}${call.summary}${ansi.reset}` : '') +
          (output != null ? `\n  ${ansi.dim}→ ${abbreviate(output)}${ansi.reset}` : ''),
      )
      return
    }
    case 'context_checkpoint':
      terminal.printBlock(`${label('checkpoint', ansi.gray)} context summarized`)
      return
  }
}

const historyEventLimit = 500

function printHistoryEvent(terminal: Terminal, event: AgentEvent, toolCalls: Map<string, ToolCallInfo>): void {
  if (event.event_kind === 'agent_input') {
    if (event.input_kind !== 'content') return
    const text = event.content_blocks
      .map((block) => (block.type === 'text' ? block.text : ''))
      .filter((line) => line.trim() !== '')
      .join('\n')
    if (text !== '') terminal.printBlock(`${label('you', ansi.cyan)} ${text}`)
    return
  }
  printEvent(terminal, event, new Set(), toolCalls)
}

async function loadHistory(
  client: OmnaraClient,
  path: { orgID: string; projectID: string; agentID: string },
  terminal: Terminal,
  toolCalls: Map<string, ToolCallInfo>,
): Promise<number> {
  const pages: AgentEvent[][] = []
  let loaded = 0
  let before: number | null = 0
  do {
    const { data }: { data: ListAgentEventsResponse } = await sdk.listEvents({
      client,
      path,
      query: { before_sequence: before, limit: 100 },
    })
    pages.unshift(data.data)
    loaded += data.data.length
    before = data.next_before_sequence ?? null
  } while (before !== null && loaded < historyEventLimit)
  const events = pages.flat().sort((a, b) => a.sequence - b.sequence)
  if (before !== null) terminal.printBlock(`${ansi.dim}(older history omitted)${ansi.reset}`)
  for (const event of events) printHistoryEvent(terminal, event, toolCalls)
  return events.at(-1)?.sequence ?? 0
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
    return Math.max(20, terminalColumns() - (this.startedBlock ? 2 : 6) - 1)
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
  terminal.blankLineNow()
  terminal.printNow(`${label(kind, color)} ${form.title}`)
  for (const item of form.context ?? []) terminal.printNow(`  ${ansi.dim}${item.label}:${ansi.reset} ${item.value}`)
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
    terminal.printNow(`  ${ansi.dim}${question.prompt}${ansi.reset} ${chosen}${answer.text != null ? `: ${answer.text}` : ''}`)
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
  const toolCalls = new Map<string, ToolCallInfo>()
  let toolPreviewShown = false
  const renderRunningTools = (): void => {
    if (toolCalls.size === 0) {
      if (toolPreviewShown) {
        toolPreviewShown = false
        terminal.setPreview(undefined)
      }
      return
    }
    const parts = [...toolCalls.values()].map((tool) =>
      tool.summary != null ? `${tool.name} · ${tool.summary}` : tool.name,
    )
    const width = Math.max(20, terminalColumns() - 8)
    toolPreviewShown = true
    terminal.setPreview(`${label('tool', ansi.magenta)} ${ansi.dim}${abbreviate(parts.join(' | '), width)} …${ansi.reset}`)
  }
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

  const initialSequence = await loadHistory(client, path, terminal, toolCalls)
  renderRunningTools()

  const streamTask = (async () => {
    let afterSequence = initialSequence
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
            printEvent(terminal, frame, streamedTextContexts, toolCalls)
            renderRunningTools()
          } else if ('event' in frame) {
            const delta = frame.event
            if (delta.kind === 'block_start') {
              if (delta.block.kind === 'tool_use') {
                if (!toolCalls.has(delta.block.tool_call_id)) {
                  toolCalls.set(delta.block.tool_call_id, { name: delta.block.tool_name })
                  renderRunningTools()
                }
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
        deltas.reset()
        streamedTextContexts.clear()
        if (error instanceof AgentEventStreamError && error.kind === 'aborted') return
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
      const data = await sdk
        .listAgentInteractions({ client, path, query: { state: 'open' } })
        .then((response) => response.data)
        .catch(() => undefined)
      if (data !== undefined) {
        let arrived = false
        for (const interaction of data.data) {
          if (seenInteractions.has(interaction.id)) continue
          seenInteractions.add(interaction.id)
          pendingInteractions.push(interaction)
          arrived = true
        }
        if (arrived) interruptRead.abort()
      }
      await new Promise((resolve) => setTimeout(resolve, 1000))
    }
  })()

  const handleLine = async (line: string): Promise<'quit' | undefined> => {
    if (line === '') return undefined
    if (line === '/quit' || line === '/exit') return 'quit'
    await sdk.createAgentInput({
      client,
      path,
      body: { content_blocks: [{ type: 'text', text: line }] },
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
      const result = await readLineOnce(terminal, defaultPrompt, pendingInput, interruptRead.signal)
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
