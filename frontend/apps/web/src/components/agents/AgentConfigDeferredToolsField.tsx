import type { AgentConfigDefinitionDeferredTools } from '@omnara/sdk'

import { CheckboxField, FieldGroup } from '@/components/ui/field'

export function AgentConfigDeferredToolsField({
  value,
  onChange,
}: {
  value: AgentConfigDefinitionDeferredTools | undefined
  onChange: (value: AgentConfigDefinitionDeferredTools | undefined) => void
}) {
  return (
    <FieldGroup className="bg-card rounded-xl border px-4 py-4 sm:px-5">
      <CheckboxField
        className="items-start"
        inputClassName="mt-0.5"
        label="Code mode for deferred tools"
        description={
          <>
            Let commands this agent runs call its deferred tools with <code>omnarad call</code>,
            instead of adding them to the model’s tools. Calls follow each tool’s permissions and
            stay out of the model’s context. Tools commands can’t call are still added, as are all
            deferred tools while the agent has no online machine.
            Subagents don’t inherit this.
          </>
        }
        checked={value?.mode === 'code_mode'}
        onChange={(event) => {
          onChange(event.target.checked ? { mode: 'code_mode' } : undefined)
        }}
      />
    </FieldGroup>
  )
}
