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
            each tool’s permissions and stay out of the model’s context. This does not change which
            tools the model can call. To choose which tools commands can call, set{' '}
            <code>code_mode</code> on a tool or MCP server in the config editor. Subagents that copy
            this agent inherit it. Agents on the same machine can call each other’s tools this way,
            so use it on machines dedicated to one agent or to agents you trust equally.
          </>
        }
        checked={value?.enabled === true}
        onChange={(event) => {
          if (event.target.checked) {
            onChange({ ...value, enabled: true })
            return
          }
          const hasPolicy = Object.keys(value ?? {}).some((key) => key !== 'enabled')
          onChange(hasPolicy ? { ...value, enabled: false } : undefined)
        }}
      />
    </FieldGroup>
  )
}
