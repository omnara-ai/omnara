import type { AgentInteraction, InteractionAnswer } from '@omnara/sdk'
import { Box, Text, useInput } from 'ink'
import { useState } from 'react'

type InteractionKindLabel = 'approval' | 'question'

export function Label({ name, color }: { name: string; color: string }) {
  return (
    <Text bold color={color}>
      {name}
    </Text>
  )
}

function interactionKindLabel(interaction: AgentInteraction): InteractionKindLabel {
  return interaction.interaction_kind === 'permission' ? 'approval' : 'question'
}

function kindColor(kind: InteractionKindLabel): string {
  return kind === 'approval' ? 'yellow' : 'cyan'
}

export function TextInput({
  value,
  onChange,
  onSubmit,
}: {
  value: string
  onChange: (value: string) => void
  onSubmit: (value: string) => void
}) {
  useInput((input, key) => {
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
  })
  return (
    <Text>
      {value}
      <Text inverse> </Text>
    </Text>
  )
}

interface SelectItem {
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
      onSubmit(selected.size === 0 ? [active] : [...selected].sort((a, b) => a - b))
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

export function InteractionPrompt({
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
