import type { AgentConfigDefinitionProgrammaticToolCalls } from '@omnara/sdk'
import { useState } from 'react'

import {
  CheckboxField,
  Field,
  FieldDescription,
  FieldGroup,
  FieldLabel,
} from '@/components/ui/field'
import { Input } from '@/components/ui/input'

export function AgentConfigProgrammaticToolCallsField({
  value,
  onChange,
}: {
  value: AgentConfigDefinitionProgrammaticToolCalls | undefined
  onChange: (value: AgentConfigDefinitionProgrammaticToolCalls | undefined) => void
}) {
  const enabled = value?.enabled === true
  const exclude = value?.exclude ?? []
  // The draft keeps what was typed, such as a trailing comma, and resyncs when the list
  // changes from outside, such as a YAML edit.
  const [excludeDraft, setExcludeDraft] = useState(exclude.join(', '))
  if (!sameNames(namesFromDraft(excludeDraft), exclude)) setExcludeDraft(exclude.join(', '))

  return (
    <FieldGroup className="bg-card rounded-xl border px-4 py-4 sm:px-5">
      <CheckboxField
        className="items-start"
        inputClassName="mt-0.5"
        label="Programmatic tool calls"
        description={
          <>
            Let commands this agent runs call its other tools with <code>omnarad call</code>. Calls
            follow each tool’s permissions and stay out of the model’s context. Only machines with
            programmatic tool calls enabled allow them, and subagents don’t inherit this.
          </>
        }
        checked={enabled}
        onChange={(event) => {
          onChange(event.target.checked ? programmaticToolCalls(exclude) : undefined)
        }}
      />
      {enabled && (
        <Field>
          <FieldLabel htmlFor="agent-config-programmatic-exclude">Excluded tools</FieldLabel>
          <Input
            id="agent-config-programmatic-exclude"
            value={excludeDraft}
            placeholder="web_search, mcp__github__create_issue"
            onChange={(event) => {
              setExcludeDraft(event.target.value)
              onChange(programmaticToolCalls(namesFromDraft(event.target.value)))
            }}
          />
          <FieldDescription>Comma-separated tool names commands can’t call.</FieldDescription>
        </Field>
      )}
    </FieldGroup>
  )
}

function programmaticToolCalls(exclude: string[]): AgentConfigDefinitionProgrammaticToolCalls {
  const config: AgentConfigDefinitionProgrammaticToolCalls = { enabled: true }
  if (exclude.length > 0) config.exclude = exclude
  return config
}

function namesFromDraft(draft: string): string[] {
  return draft
    .split(',')
    .map((name) => name.trim())
    .filter((name) => name !== '')
}

function sameNames(a: string[], b: string[]) {
  return a.length === b.length && a.every((name, index) => name === b[index])
}
