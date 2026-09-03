import type { AgentInteraction, InteractionAnswer } from '@omnara/sdk'
import { Box, Static, Text, useApp, useInput } from 'ink'
import { useEffect, useState } from 'react'

import { type ChatTarget, useChatSession } from './chat-session.ts'
import {
  type AgentActivity,
  formatDuration,
  type InteractionKindLabel,
  interactionKindLabel,
  type ToolCallInfo,
  type TranscriptEntry,
} from './chat-state.ts'
import { abbreviate } from './output.ts'

const spinnerFrames = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏']
const streamingTailLines = 8
const streamingTailChars = 1500

function Label({ name, color }: { name: string; color: string }) {
  return (
    <Text bold color={color}>
      {name}
    </Text>
  )
}

function outcomeColor(outcome: string): string {
  if (outcome === 'succeeded') return 'green'
  return outcome === 'canceled' ? 'yellow' : 'red'
}

function EntryView({ entry }: { entry: TranscriptEntry }) {
  switch (entry.kind) {
    case 'user':
      return (
        <Text>
          <Label name="you" color="cyan" /> {entry.text}
        </Text>
      )
    case 'agent':
      return (
        <Text>
          <Label name="agent" color="green" /> {entry.text}
        </Text>
      )
    case 'tool':
      return (
        <Box flexDirection="column">
          <Text>
            <Label name="tool" color="magenta" /> {entry.name}{' '}
            <Text color={outcomeColor(entry.outcome)}>({entry.outcome})</Text>
          </Text>
          {entry.summary != null && <Text dimColor>{`  ${entry.summary}`}</Text>}
          {entry.output != null && <Text dimColor>{`  → ${entry.output}`}</Text>}
        </Box>
      )
    case 'checkpoint':
      return (
        <Text>
          <Label name="checkpoint" color="gray" /> context summarized
        </Text>
      )
    case 'error':
      return (
        <Text>
          <Label name={entry.label} color="red" /> {entry.text}
        </Text>
      )
    case 'note':
      return <Text dimColor>{entry.text}</Text>
    case 'answer':
      return (
        <Box flexDirection="column">
          <Text>
            <Label name={entry.kindLabel} color={kindColor(entry.kindLabel)} /> {entry.title}
          </Text>
          {entry.lines.map((line, index) => (
            <Text key={index} dimColor>{`  ${line}`}</Text>
          ))}
        </Box>
      )
  }
}

function kindColor(kind: InteractionKindLabel): string {
  return kind === 'approval' ? 'yellow' : 'cyan'
}

function StreamingText({ text }: { text: string }) {
  const lines = text.slice(-streamingTailChars).split('\n')
  const clipped = text.length > streamingTailChars || lines.length > streamingTailLines
  const tail = lines.slice(-streamingTailLines).join('\n')
  return (
    <Box marginTop={1}>
      <Text>
        <Label name="agent" color="green" /> {clipped ? '…' : ''}
        {tail}
      </Text>
    </Box>
  )
}

function RunningTools({ toolCalls }: { toolCalls: Record<string, ToolCallInfo> }) {
  const tools = Object.values(toolCalls)
  if (tools.length === 0) return null
  const parts = tools.map((tool) =>
    tool.summary != null ? `${tool.name} · ${tool.summary}` : tool.name,
  )
  return (
    <Text wrap="truncate-end">
      <Label name="tool" color="magenta" /> <Text dimColor>{parts.join(' | ')} …</Text>
    </Text>
  )
}

function StatusLine({
  activity,
  workStart,
}: {
  activity: AgentActivity | undefined
  workStart: number | undefined
}) {
  const [tick, setTick] = useState<{ frame: number; now: number | undefined }>({
    frame: 0,
    now: undefined,
  })
  useEffect(() => {
    if (activity == null) return
    const timer = setInterval(() => {
      setTick((previous) => ({
        frame: (previous.frame + 1) % spinnerFrames.length,
        now: Date.now(),
      }))
    }, 120)
    return () => {
      clearInterval(timer)
    }
  }, [activity])
  if (activity == null) return null
  return (
    <Text wrap="truncate-end">
      <Text color="cyan">{spinnerFrames[tick.frame]}</Text> {activity.text}
      {workStart != null && tick.now != null && (
        <Text dimColor> ({formatDuration(tick.now - workStart)})</Text>
      )}
      {activity.detail != null && <Text dimColor> {abbreviate(activity.detail, 60)}</Text>}
    </Text>
  )
}

function TextInput({
  value,
  onChange,
  onSubmit,
  active,
}: {
  value: string
  onChange: (value: string) => void
  onSubmit: (value: string) => void
  active: boolean
}) {
  useInput(
    (input, key) => {
      if (key.return) {
        onSubmit(value)
        return
      }
      if (key.backspace || key.delete) {
        onChange(value.slice(0, -1))
        return
      }
      if (key.ctrl || key.meta || key.escape || key.tab) return
      if (key.upArrow || key.downArrow || key.leftArrow || key.rightArrow) return
      if (key.pageUp || key.pageDown) return
      onChange(value + input)
    },
    { isActive: active },
  )
  return (
    <Text>
      {value}
      <Text inverse> </Text>
    </Text>
  )
}

export interface SelectItem {
  label: string
  hint?: string
}

function SelectList({
  items,
  multiple,
  onSubmit,
}: {
  items: SelectItem[]
  multiple: boolean
  onSubmit: (indices: number[]) => void
}) {
  const [active, setActive] = useState(0)
  const [selected, setSelected] = useState<ReadonlySet<number>>(new Set())
  useInput((input, key) => {
    if (key.upArrow || input === 'k') {
      setActive((current) => (current - 1 + items.length) % items.length)
    } else if (key.downArrow || input === 'j') {
      setActive((current) => (current + 1) % items.length)
    } else if (input === ' ' && multiple) {
      setSelected((current) => {
        const next = new Set(current)
        if (next.has(active)) next.delete(active)
        else next.add(active)
        return next
      })
    } else if (key.return) {
      if (!multiple) {
        onSubmit([active])
        return
      }
      const chosen = selected.size === 0 ? [active] : [...selected].sort((a, b) => a - b)
      onSubmit(chosen)
    }
  })
  return (
    <Box flexDirection="column">
      {items.map((item, index) => {
        const isActive = index === active
        const marker = multiple ? (selected.has(index) ? '[x] ' : '[ ] ') : ''
        return (
          <Text key={index}>
            <Text color="cyan">{isActive ? '❯' : ' '}</Text> {marker}
            <Text color={isActive ? 'cyan' : undefined}>{item.label}</Text>
            {item.hint != null && <Text dimColor> {item.hint}</Text>}
          </Text>
        )
      })}
      <Text dimColor>
        {multiple ? '↑/↓ move · space toggle · enter confirm' : '↑/↓ move · enter confirm'}
      </Text>
    </Box>
  )
}

function InteractionPrompt({
  interaction,
  onAnswer,
}: {
  interaction: AgentInteraction
  onAnswer: (answers: InteractionAnswer[]) => void
}) {
  const kind = interactionKindLabel(interaction)
  const form = interaction.request
  const [answers, setAnswers] = useState<InteractionAnswer[]>([])
  const [pendingText, setPendingText] = useState<number[]>()
  const [text, setText] = useState('')
  const question = form.questions[answers.length]

  const commit = (answer: InteractionAnswer) => {
    const next = [...answers, answer]
    setAnswers(next)
    setPendingText(undefined)
    setText('')
    if (next.length === form.questions.length) onAnswer(next)
  }

  return (
    <Box flexDirection="column" marginTop={1}>
      <Text>
        <Label name={kind} color={kindColor(kind)} /> {form.title}
      </Text>
      {(form.context ?? []).map((item, index) => (
        <Text key={index}>
          {'  '}
          <Text dimColor>{item.label}:</Text> {item.value}
        </Text>
      ))}
      {answers.map((answer, index) => {
        const answered = form.questions[index]
        if (answered == null) return null
        const chosen = answer.option_indices
          .map((optionIndex) => answered.options[optionIndex]?.label)
          .join(', ')
        return (
          <Text key={index}>
            {'  '}
            <Text dimColor>{answered.prompt}</Text> {chosen}
            {answer.text != null ? `: ${answer.text}` : ''}
          </Text>
        )
      })}
      {question != null && pendingText == null && (
        <Box flexDirection="column" marginTop={1}>
          <Text>{question.prompt}</Text>
          <SelectList
            key={answers.length}
            items={question.options.map((option) => ({
              label: option.label,
              hint: option.allows_text === true ? 'accepts text' : undefined,
            }))}
            multiple={question.multiple === true}
            onSubmit={(indices) => {
              const allowsText = indices.some(
                (index) => question.options[index]?.allows_text === true,
              )
              if (allowsText) setPendingText(indices)
              else commit({ option_indices: indices })
            }}
          />
        </Box>
      )}
      {question != null && pendingText != null && (
        <Text>
          <Text dimColor>optional text (enter to skip)</Text> <Text color="cyan">❯</Text>{' '}
          <TextInput
            value={text}
            onChange={setText}
            active
            onSubmit={(line) => {
              const trimmed = line.trim()
              commit(
                trimmed === ''
                  ? { option_indices: pendingText }
                  : { option_indices: pendingText, text: trimmed },
              )
            }}
          />
        </Text>
      )}
    </Box>
  )
}

export function Chat({ target }: { target: ChatTarget }) {
  const { state, send, answer } = useChatSession(target)
  const { exit } = useApp()
  const [draft, setDraft] = useState('')
  useEffect(() => {
    if (state.ended) exit()
  }, [state.ended, exit])
  const interaction = state.interactions[0]

  return (
    <Box flexDirection="column">
      <Static items={state.entries}>
        {(entry) => (
          <Box key={entry.id} marginTop={1}>
            <EntryView entry={entry} />
          </Box>
        )}
      </Static>
      {state.streaming != null && <StreamingText text={state.streaming.text} />}
      <Box flexDirection="column" marginTop={1}>
        <RunningTools toolCalls={state.toolCalls} />
        <StatusLine activity={state.activity} workStart={state.workStart} />
      </Box>
      {interaction != null ? (
        <InteractionPrompt
          key={interaction.id}
          interaction={interaction}
          onAnswer={(answers) => {
            void answer(interaction, answers)
          }}
        />
      ) : (
        state.ready &&
        !state.ended && (
          <Box marginTop={1}>
            <Text bold color="cyan">
              ❯{' '}
            </Text>
            <TextInput
              value={draft}
              onChange={setDraft}
              active
              onSubmit={(line) => {
                const trimmed = line.trim()
                setDraft('')
                if (trimmed === '') return
                if (trimmed === '/quit' || trimmed === '/exit') {
                  exit()
                  return
                }
                void send(trimmed)
              }}
            />
          </Box>
        )
      )}
    </Box>
  )
}
