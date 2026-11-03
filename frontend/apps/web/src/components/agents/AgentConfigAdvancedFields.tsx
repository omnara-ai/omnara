import { configPath, validateAdvancedEntry } from '@/components/agents/agentConfigCompatibility'
import { AgentConfigSectionCard } from '@/components/agents/AgentConfigSectionCard'
import type { AgentBuilderForm } from '@/components/agents/useAgentBuilderForm'
import { Field, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'

export function AgentConfigAdvancedFields({ form }: { form: AgentBuilderForm }) {
  const custom = form.advancedEntries.filter((entry) => entry.kind === 'custom')
  if (custom.length === 0) return null
  return (
    <AgentConfigSectionCard title="Custom tools" action={null}>
      <div className="space-y-4 px-4 pb-4 sm:px-5">
        <p className="text-muted-foreground text-sm">
          {custom.length} custom tool definitions are preserved. Edit their descriptions and schemas
          in Advanced YAML.
        </p>
        {custom.map((entry) => {
          const path = configPath(entry.path)
          const issues = validateAdvancedEntry(entry)
          return (
            <details key={path}>
              <summary className="cursor-pointer text-sm">
                <span className="font-mono">{entry.path[1]}</span> — Advanced YAML
              </summary>
              <Field className="mt-3">
                <FieldLabel htmlFor={`advanced-${path}`}>
                  {path} definition (YAML or JSON)
                </FieldLabel>
                <p className="text-muted-foreground text-sm">
                  The complete definition is preserved unless you edit it here.
                </p>
                <Textarea
                  id={`advanced-${path}`}
                  className="min-h-64 font-mono text-xs"
                  value={entry.source}
                  spellCheck={false}
                  aria-invalid={issues.length > 0}
                  onChange={(event) => {
                    form.setAdvancedEntries(
                      form.advancedEntries.map((item) =>
                        item === entry ? { ...item, source: event.target.value } : item,
                      ),
                    )
                  }}
                />
                {issues.map((issue, index) => (
                  <p key={index} role="alert" className="text-destructive text-sm">
                    {configPath(issue.path)}: {issue.message}
                  </p>
                ))}
              </Field>
            </details>
          )
        })}
      </div>
    </AgentConfigSectionCard>
  )
}
