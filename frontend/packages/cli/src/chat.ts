import type {
  AgentEvent,
  AgentEventStreamData,
  AgentInteraction,
  InteractionAnswer,
  ListAgentEventsResponse,
  OmnaraClient,
} from '@omnara/sdk'
import { ApiError, openAgentEventStream, sdk } from '@omnara/sdk'

import {
  DeltaRenderer,
  printEvent,
  printHistoryEvent,
  type ToolCallInfo,
} from './agent-rendering.ts'
import { abbreviate } from './output.ts'
import { sleepSeconds } from './poll.ts'
import { pick } from './select.ts'
import { ansi, label, Terminal, terminalColumns } from './terminal.ts'

export interface ChatTarget {
  client: OmnaraClient
  orgId: string
  projectId: string
  agentId: string
}

const historyEventLimit = 500

async function loadHistory(
  client: OmnaraClient,
  path: { orgID: string; projectID: string; agentID: string },
  terminal: Terminal,
  toolCalls: Map<string, ToolCallInfo>,
): Promise<number> {
  const events: AgentEvent[] = []
  let before: number | null = 0
  do {
    const { data }: { data: ListAgentEventsResponse } = await sdk.listEvents({
      client,
      path,
      query: { before_sequence: before, limit: 100 },
    })
    events.push(...data.data)
    before = data.next_before_sequence ?? null
  } while (before !== null && events.length < historyEventLimit)
  events.sort((a, b) => a.sequence - b.sequence)
  if (before !== null) terminal.printBlock(`${ansi.dim}(older history omitted)${ansi.reset}`)
  for (const event of events) printHistoryEvent(terminal, event, toolCalls)
  return events.at(-1)?.sequence ?? 0
}

async function answerInteraction(
  terminal: Terminal,
  interaction: AgentInteraction,
): Promise<InteractionAnswer[] | undefined> {
  const kind = interaction.interaction_kind === 'permission' ? 'approval' : 'question'
  const color = interaction.interaction_kind === 'permission' ? ansi.yellow : ansi.cyan
  const form = interaction.request
  terminal.blankLineNow()
  terminal.printNow(`${label(kind, color)} ${form.title}`)
  for (const item of form.context ?? [])
    terminal.printNow(`  ${ansi.dim}${item.label}:${ansi.reset} ${item.value}`)
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
      const text = await terminal.readLine(
        `${ansi.dim}optional text (enter to skip)${ansi.reset} ${ansi.cyan}❯${ansi.reset} `,
        '',
      )
      if (text.kind !== 'line') return undefined
      if (text.line.trim() !== '') answer.text = text.line.trim()
    }
    const chosen = answer.option_indices.map((index) => question.options[index]?.label).join(', ')
    terminal.printNow(
      `  ${ansi.dim}${question.prompt}${ansi.reset} ${chosen}${answer.text != null ? `: ${answer.text}` : ''}`,
    )
    answers.push(answer)
  }
  return answers
}

const defaultPrompt = `${ansi.bold}${ansi.cyan}❯${ansi.reset} `

function startsWork(frame: AgentEventStreamData): boolean {
  if (!('event_kind' in frame)) return false
  if (frame.event_kind === 'tool_result') return true
  return (
    frame.event_kind === 'agent_input' &&
    (frame.input_kind === 'content' || frame.input_kind === 'interaction_response')
  )
}

export async function runChat(target: ChatTarget): Promise<void> {
  const { client, orgId, projectId, agentId } = target
  const path = { orgID: orgId, projectID: projectId, agentID: agentId }
  const abort = new AbortController()
  const pendingInteractions: AgentInteraction[] = []
  const seenInteractions = new Set<string>()
  const streamedTextByContext = new Map<string, string>()
  const terminal = new Terminal()
  const deltas = new DeltaRenderer(terminal, streamedTextByContext)
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
    terminal.setPreview(
      `${label('tool', ansi.magenta)} ${ansi.dim}${abbreviate(parts.join(' | '), width)} …${ansi.reset}`,
    )
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
  const pauseTurn = (): void => {
    agentState = undefined
    workStart = undefined
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
  const endTurn = (stopReason: string | null | undefined): void => {
    if (stopReason === 'tool_use') setAgentState('running tools…')
    else finishTurn()
  }
  const initialSequence = await loadHistory(client, path, terminal, toolCalls)
  renderRunningTools()

  const spinner = setInterval(() => {
    if (agentState == null) {
      terminal.setStatus(undefined)
      return
    }
    spinnerIndex = (spinnerIndex + 1) % spinnerFrames.length
    const elapsed =
      workStart != null
        ? ` ${ansi.dim}(${formatDuration(Date.now() - workStart)})${ansi.reset}`
        : ''
    const detail =
      agentState.detail != null
        ? ` ${ansi.dim}${abbreviate(agentState.detail, 60)}${ansi.reset}`
        : ''
    terminal.setStatus(
      `${ansi.cyan}${spinnerFrames[spinnerIndex]}${ansi.reset} ${agentState.text}${elapsed}${detail}`,
    )
  }, 120)

  const streamTask = (async () => {
    try {
      const frames = openAgentEventStream({
        client,
        path,
        query: { after_sequence: initialSequence, stream_deltas: true },
        signal: abort.signal,
        onConnectionStateChange: (state) => {
          if (state.state === 'reconnecting') deltas.reset()
        },
      })
      for await (const frame of frames) {
        if (startsWork(frame)) setAgentState('working…')
        if ('event_kind' in frame) {
          if (frame.event_kind === 'model_output') deltas.complete(frame.model_call_context_id)
          printEvent(terminal, frame, streamedTextByContext, toolCalls)
          renderRunningTools()
          if (frame.event_kind === 'model_output') endTurn(frame.stop_reason)
        } else if ('event' in frame) {
          const delta = frame.event
          deltas.handle(frame)
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
            endTurn(delta.stop.reason)
          } else if (delta.kind === 'error') {
            finishTurn()
          }
        }
      }
    } catch (error) {
      if (abort.signal.aborted) return
      deltas.reset()
      terminal.printBlock(
        `${label('stream error', ansi.red)} ${error instanceof Error ? error.message : String(error)}`,
      )
      abort.abort()
      interruptRead.abort()
    }
  })()

  const printError = (error: unknown): void => {
    if (error instanceof ApiError) {
      terminal.printBlock(
        `${label('error', ansi.red)} HTTP ${error.status} (${error.code ?? 'no code'}): ${error.message}`,
      )
    } else {
      terminal.printBlock(
        `${label('error', ansi.red)} ${error instanceof Error ? error.message : String(error)}`,
      )
    }
  }

  const pollTask = (async () => {
    let pollFailing = false
    while (!abort.signal.aborted) {
      try {
        const { data } = await sdk.listAgentInteractions({ client, path, query: { state: 'open' } })
        pollFailing = false
        let arrived = false
        for (const interaction of data.data) {
          if (seenInteractions.has(interaction.id)) continue
          seenInteractions.add(interaction.id)
          pendingInteractions.push(interaction)
          arrived = true
        }
        if (arrived) interruptRead.abort()
      } catch (error) {
        if (!pollFailing) {
          pollFailing = true
          terminal.printBlock(
            `${label('interactions', ansi.red)} could not check for open interactions`,
          )
          printError(error)
        }
      }
      await sleepSeconds(1)
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

  try {
    let pendingInput = ''
    while (!abort.signal.aborted) {
      const interaction = pendingInteractions.shift()
      if (interaction != null) {
        pauseTurn()
        terminal.setStatus(undefined)
        terminal.lock()
        let answers: InteractionAnswer[] | undefined
        try {
          answers = await answerInteraction(terminal, interaction)
        } finally {
          terminal.unlock()
        }
        if (answers === undefined) return
        try {
          await sdk.resolveAgentInteraction({
            client,
            path: { ...path, interactionID: interaction.id },
            body: { answers },
          })
          setAgentState('working…')
        } catch (error) {
          seenInteractions.delete(interaction.id)
          printError(error)
        }
        continue
      }
      if (interruptRead.signal.aborted) interruptRead = new AbortController()
      const result = await terminal.readLine(defaultPrompt, pendingInput, interruptRead.signal)
      pendingInput = ''
      if (result.kind === 'closed') return
      if (result.kind === 'interrupted') {
        pendingInput = result.input
        continue
      }
      try {
        if ((await handleLine(result.line.trim())) === 'quit') return
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
