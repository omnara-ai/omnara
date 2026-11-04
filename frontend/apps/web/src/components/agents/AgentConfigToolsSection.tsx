import type { ToolCatalog } from '@omnara/sdk'
import { useState } from 'react'

import { AddCustomToolDialog } from '@/components/agents/AddCustomToolDialog'
import { configPath } from '@/components/agents/agentConfigCompatibility'
import { AgentConfigDiagnostics } from '@/components/agents/AgentConfigDiagnostics'
import { AgentConfigToolsField } from '@/components/agents/AgentConfigToolsField'
import type { AgentBuilderForm } from '@/components/agents/useAgentBuilderForm'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Field, FieldLabel } from '@/components/ui/field'
import { Textarea } from '@/components/ui/textarea'

export function AgentConfigToolsSection({
  catalog,
  form,
}: {
  catalog?: ToolCatalog
  form: AgentBuilderForm
}) {
  const [adding, setAdding] = useState(false)
  const custom = form.advancedEntries.filter((entry) => entry.kind === 'custom')
  return (
    <AgentConfigToolsField
      catalog={catalog}
      tools={form.tools}
      reservedNames={form.reservedToolNames}
      resolvedTools={form.resolvedTools}
      onToolsChange={form.setTools}
      onAddCustomTool={
        catalog
          ? () => {
              setAdding(true)
            }
          : undefined
      }
    >
      <div className="divide-y">
        {custom.map((entry) => {
          const path = configPath(entry.path)
          const original = form.originalAdvancedEntries.find(
            (saved) => configPath(saved.path) === path,
          )
          const issues = form.validationIssues.filter((issue) =>
            entry.path.every((part, index) => issue.path[index] === part),
          )
          return (
            <details key={path} className="px-4 py-2.5 sm:px-5">
              <summary className="cursor-pointer text-sm">
                <span className="font-mono">{entry.path[1]}</span>
                <Badge variant="secondary" className="ml-2">
                  Custom
                </Badge>
              </summary>
              <Field className="mt-3">
                <FieldLabel htmlFor={`advanced-${path}`}>
                  {path} definition (YAML or JSON)
                </FieldLabel>
                <p className="text-muted-foreground text-sm">
                  Advanced YAML: the complete definition is preserved unless you edit it here.
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
                <AgentConfigDiagnostics issues={issues} />
                {!original && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      form.setAdvancedEntries(form.advancedEntries.filter((item) => item !== entry))
                    }}
                  >
                    Remove {entry.path[1]}
                  </Button>
                )}
                {original && original.source !== entry.source && (
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => {
                      form.setAdvancedEntries(
                        form.advancedEntries.map((item) => (item === entry ? original : item)),
                      )
                    }}
                  >
                    Discard edits to {entry.path[1]}
                  </Button>
                )}
              </Field>
            </details>
          )
        })}
      </div>
      {adding && (
        <AddCustomToolDialog
          reservedNames={[
            ...(catalog?.built_in_tools.map((tool) => tool.name) ?? []),
            ...form.tools.map((tool) => tool.name),
            ...form.reservedToolNames,
          ]}
          onClose={() => {
            setAdding(false)
          }}
          onAdd={(entry) => {
            form.setAdvancedEntries([...form.advancedEntries, entry])
            setAdding(false)
          }}
        />
      )}
    </AgentConfigToolsField>
  )
}
