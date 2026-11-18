import type { AgentConfigDefinitionCodeMode } from '@omnara/sdk'

import { CheckboxField, FieldGroup } from '@/components/ui/field'

export function AgentConfigCodeModeField({
  value,
  onChange,
}: {
  value: AgentConfigDefinitionCodeMode | undefined
  onChange: (value: AgentConfigDefinitionCodeMode | undefined) => void
}) {
  return (
    <FieldGroup className="bg-card rounded-xl border px-4 py-4 sm:px-5">
      <CheckboxField
        className="items-start"
        inputClassName="mt-0.5"
        label="Code mode"
        description={
          <>
            Let commands this agent runs call its tools with <code>omnarad call</code>. Calls follow
            each tool’s permissions and stay out of the model’s context. A deferred tool that
            commands can call is not added to the model’s tools. Subagents that copy this agent
            inherit it.
          </>
        }
        checked={value?.enabled === true}
        onChange={(event) => {
          onChange(event.target.checked ? { enabled: true } : undefined)
        }}
      />
    </FieldGroup>
  )
}
