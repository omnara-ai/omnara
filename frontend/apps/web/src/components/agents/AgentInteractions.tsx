import type {
  AgentInteraction,
  InteractionAnswer,
  InteractionFormOption,
  ResolveAgentInteractionRequest,
} from '@omnara/sdk'
import { KeyRound, MessageCircleQuestion } from 'lucide-react'
import { useState } from 'react'

import { Button } from '@/components/ui/button'
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

type Resolve = (interactionID: string, body: ResolveAgentInteractionRequest) => Promise<unknown>

function selectedOptions(
  options: InteractionFormOption[],
  optionIndices: number[],
): InteractionFormOption[] {
  return optionIndices.flatMap((index) => options[index] ?? [])
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
  const complete = interaction.request.questions.every(
    (_, questionIndex) => (selections[questionIndex]?.length ?? 0) > 0,
  )

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
    <Card className="border-primary/35 bg-primary/[0.04] shadow-none">
      <CardHeader className="gap-1 pb-3">
        <div className="text-primary flex items-center gap-2">
          {isPermission ? (
            <KeyRound className="size-4" />
          ) : (
            <MessageCircleQuestion className="size-4" />
          )}
          <span className="text-xs font-semibold uppercase tracking-wide">
            {isPermission ? 'Permission required' : 'Agent question'}
          </span>
        </div>
        <CardTitle className="text-sm">{interaction.request.title}</CardTitle>
      </CardHeader>
      <CardContent className="grid gap-5">
        {interaction.request.context != null && interaction.request.context.length > 0 && (
          <dl className="bg-background grid gap-2 rounded-md border p-3 text-xs">
            {interaction.request.context.map((item, index) => (
              <div key={`${item.label}-${index}`} className="grid gap-1">
                <dt className="text-muted-foreground font-medium">{item.label}</dt>
                <dd className="whitespace-pre-wrap font-mono">{item.value}</dd>
              </div>
            ))}
          </dl>
        )}
        {interaction.request.questions.map((question, questionIndex) => {
          const optionIndices = selections[questionIndex] ?? []
          const allowsText = selectedOptions(question.options, optionIndices).some(
            (option) => option.allows_text === true,
          )
          return (
            <fieldset key={questionIndex} className="grid gap-3">
              <legend className="text-sm font-medium">{question.prompt}</legend>
              <div className="flex flex-wrap gap-2">
                {question.options.map((candidate, optionIndex) => (
                  <Button
                    key={optionIndex}
                    type="button"
                    size="sm"
                    variant={optionIndices.includes(optionIndex) ? 'default' : 'outline'}
                    disabled={!canOperate || pending}
                    onClick={() => {
                      const nextOptionIndices =
                        question.multiple === true
                          ? optionIndices.includes(optionIndex)
                            ? optionIndices.filter((index) => index !== optionIndex)
                            : [...optionIndices, optionIndex]
                          : [optionIndex]
                      setSelections((current) => ({
                        ...current,
                        [questionIndex]: nextOptionIndices,
                      }))
                      const nextAllowsText = question.options.some(
                        (option, index) =>
                          option.allows_text === true && nextOptionIndices.includes(index),
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
              </div>
              {allowsText && (
                <div className="grid gap-2">
                  <Label htmlFor={`${interaction.id}-${questionIndex}-response`}>
                    Additional details (optional)
                  </Label>
                  <Textarea
                    id={`${interaction.id}-${questionIndex}-response`}
                    value={responses[questionIndex] ?? ''}
                    placeholder="Add details"
                    disabled={!canOperate || pending}
                    onChange={(event) => {
                      setResponses((current) => ({
                        ...current,
                        [questionIndex]: event.target.value,
                      }))
                    }}
                  />
                </div>
              )}
            </fieldset>
          )
        })}
      </CardContent>
      <CardFooter className="justify-end">
        <Button size="sm" disabled={!canOperate || pending || !complete} onClick={submit}>
          Submit
        </Button>
      </CardFooter>
    </Card>
  )
}

export function AgentInteractions({
  interactions,
  onResolve,
  pending,
  error,
  loadError,
  canOperate,
}: {
  interactions: AgentInteraction[]
  onResolve: Resolve
  pending: boolean
  error?: Error | null
  loadError?: string | null
  canOperate: boolean
}) {
  if (interactions.length === 0 && loadError == null) return null
  return (
    <section className="grid max-h-[40svh] gap-3 overflow-y-auto pr-1" aria-label="Agent requests">
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
