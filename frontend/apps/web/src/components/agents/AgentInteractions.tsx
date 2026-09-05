import { useAgentInteractions, useResolveAgentInteraction } from '@omnara/react'
import type {
  AgentInteraction,
  InteractionAnswer,
  InteractionFormOption,
  ResolveAgentInteractionRequest,
} from '@omnara/sdk'
import { useState } from 'react'

import { KeyRound, MessageCircleQuestion } from '@/components/icons'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { errorMessage } from '@/lib/submit-status'
import { cn } from '@/lib/utils'

type Resolve = (interactionID: string, body: ResolveAgentInteractionRequest) => Promise<void>

function selectedOptions(
  options: InteractionFormOption[],
  optionIndices: number[],
): InteractionFormOption[] {
  return optionIndices.flatMap((index) => options[index] ?? [])
}

function keyedContextItems(interaction: AgentInteraction) {
  const occurrences = new Map<string, number>()
  return (interaction.request.context ?? []).map((item) => {
    const contentKey = JSON.stringify([item.label, item.value])
    const occurrence = (occurrences.get(contentKey) ?? 0) + 1
    occurrences.set(contentKey, occurrence)
    return {
      item,
      key: `${interaction.id}:context:${contentKey}:${String(occurrence)}`,
    }
  })
}

function InteractionFormCard({
  interaction,
  resolve,
  pending,
  canOperate,
}: {
  interaction: AgentInteraction
  resolve: Resolve
  pending: boolean
  canOperate: boolean
}) {
  const [selections, setSelections] = useState<Record<number, number[]>>({})
  const [responses, setResponses] = useState<Record<number, string>>({})
  const isPermission = interaction.interaction_kind === 'permission'
  const questions = interaction.request.questions
  let complete = true
  for (let questionIndex = 0; questionIndex < questions.length; questionIndex += 1) {
    if ((selections[questionIndex]?.length ?? 0) === 0) {
      complete = false
      break
    }
  }
  const contextItems = keyedContextItems(interaction)
  const toolName = interaction.tool_name ?? null

  function submit() {
    const answers: InteractionAnswer[] = []
    for (const [questionIndex, question] of interaction.request.questions.entries()) {
      const optionIndices = selections[questionIndex]
      if (optionIndices == null || optionIndices.length === 0) return
      const options = selectedOptions(question.options, optionIndices)
      const answer: InteractionAnswer = {
        option_indices: optionIndices,
      }
      const text = responses[questionIndex]?.trim()
      if (options.some((option) => option.allows_text === true) && text != null && text !== '') {
        answer.text = text
      }
      answers.push(answer)
    }
    void resolve(interaction.id, {
      answers,
    }).catch(() => undefined)
  }

  return (
    <Card className="border-primary/35 bg-primary/[0.04] min-w-0 gap-3 shadow-none">
      <CardHeader className="min-w-0 gap-1">
        {isPermission ? (
          <CardTitle className="wrap-anywhere flex flex-wrap items-center gap-2 text-sm text-blue-600 dark:text-blue-400">
            <KeyRound className="size-4 shrink-0" />
            {toolName == null ? (
              interaction.request.title
            ) : (
              <>
                Permission requested for
                <span className="bg-muted text-foreground rounded-md px-2 py-1 font-mono text-xs">
                  {toolName}
                </span>
              </>
            )}
          </CardTitle>
        ) : (
          <CardTitle className="wrap-anywhere text-primary flex items-center gap-2 text-sm">
            <MessageCircleQuestion className="size-4 shrink-0" />
            {interaction.request.title}
          </CardTitle>
        )}
      </CardHeader>
      <CardContent className="grid min-w-0 gap-4">
        {contextItems.length > 0 && (
          <dl className="bg-background grid min-w-0 gap-2 rounded-md border p-3 text-xs">
            {contextItems.map(({ item, key }) => (
              <div key={key} className="grid min-w-0 gap-1">
                <dt className="text-muted-foreground wrap-anywhere font-medium">{item.label}</dt>
                <dd className="wrap-anywhere whitespace-pre-wrap font-mono">{item.value}</dd>
              </div>
            ))}
          </dl>
        )}
        {questions.map((question, questionIndex) => {
          const optionIndices = selections[questionIndex] ?? []
          const optionIndexSet = new Set(optionIndices)
          const allowsText = selectedOptions(question.options, optionIndices).some(
            (option) => option.allows_text === true,
          )
          const isLast = questionIndex === questions.length - 1
          const responseId = `${interaction.id}-${questionIndex}-response`
          const responseLabel = isPermission ? 'Reason (optional)' : 'Details (optional)'
          return (
            <fieldset key={questionIndex} className="grid min-w-0 gap-3">
              <legend
                className={cn('wrap-anywhere mb-3 text-sm font-medium', isPermission && 'sr-only')}
              >
                {question.prompt}
              </legend>
              <div className="flex min-w-0 flex-wrap items-center gap-2">
                {question.options.map((candidate, optionIndex) => (
                  <Button
                    key={optionIndex}
                    type="button"
                    size="sm"
                    variant={optionIndexSet.has(optionIndex) ? 'default' : 'outline'}
                    className="wrap-anywhere h-auto min-h-8 max-w-full whitespace-normal text-left"
                    disabled={!canOperate || pending}
                    onClick={() => {
                      const nextOptionIndices =
                        question.multiple === true
                          ? optionIndexSet.has(optionIndex)
                            ? optionIndices.filter((index) => index !== optionIndex)
                            : [...optionIndices, optionIndex]
                          : [optionIndex]
                      const nextOptionIndexSet = new Set(nextOptionIndices)
                      setSelections((current) => ({
                        ...current,
                        [questionIndex]: nextOptionIndices,
                      }))
                      const nextAllowsText = question.options.some(
                        (option, index) =>
                          option.allows_text === true && nextOptionIndexSet.has(index),
                      )
                      if (!nextAllowsText) {
                        setResponses((current) => ({
                          ...current,
                          [questionIndex]: '',
                        }))
                      }
                    }}
                  >
                    {candidate.label}
                  </Button>
                ))}
                {allowsText && (
                  <>
                    <Label htmlFor={responseId} className="sr-only">
                      {responseLabel}
                    </Label>
                    <Input
                      id={responseId}
                      className="h-8 min-w-40 flex-1 sm:h-8"
                      value={responses[questionIndex] ?? ''}
                      placeholder={responseLabel}
                      disabled={!canOperate || pending}
                      onChange={(event) => {
                        setResponses((current) => ({
                          ...current,
                          [questionIndex]: event.target.value,
                        }))
                      }}
                      onKeyDown={(event) => {
                        if (
                          event.key === 'Enter' &&
                          !event.nativeEvent.isComposing &&
                          complete &&
                          !pending
                        ) {
                          event.preventDefault()
                          submit()
                        }
                      }}
                    />
                  </>
                )}
                {isLast && (
                  <Button
                    size="sm"
                    className="ml-auto"
                    disabled={!canOperate || pending || !complete}
                    onClick={submit}
                  >
                    Submit
                  </Button>
                )}
              </div>
            </fieldset>
          )
        })}
      </CardContent>
    </Card>
  )
}

export function AgentInteractions({
  orgID,
  projectID,
  agentID,
  agentActive,
  canOperate,
}: {
  orgID: string
  projectID: string
  agentID: string
  agentActive: boolean
  canOperate: boolean
}) {
  const interactionsQuery = useAgentInteractions(orgID, projectID, agentID, agentActive)
  const resolveInteraction = useResolveAgentInteraction(orgID, projectID, agentID)
  const interactions = interactionsQuery.data?.data ?? []
  const loadError =
    interactionsQuery.error != null ? errorMessage(interactionsQuery.error, 'Unknown error') : null
  const error = resolveInteraction.error
  const pending = resolveInteraction.isPending
  const onResolve: Resolve = async (interactionID, body) => {
    await resolveInteraction.mutateAsync({ interactionID, body })
  }
  if (interactions.length === 0 && loadError == null) return null
  return (
    <section
      className="grid max-h-[40svh] min-w-0 gap-3 overflow-y-auto overflow-x-hidden pr-1"
      aria-label="Agent requests"
    >
      {loadError != null && (
        <p className="text-destructive text-xs">Could not load pending requests: {loadError}</p>
      )}
      {error && <p className="text-destructive text-xs">{error.message}</p>}
      {interactions.map((interaction) => (
        <InteractionFormCard
          key={interaction.id}
          interaction={interaction}
          resolve={onResolve}
          pending={pending}
          canOperate={canOperate}
        />
      ))}
    </section>
  )
}
