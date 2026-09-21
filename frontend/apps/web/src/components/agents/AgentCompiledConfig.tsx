import type { AgentConfig } from '@omnara/sdk'

import { ChevronDownIcon } from '@/components/icons'
import { CodeBlock } from '@/components/overview/CodeBlock'
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from '@/components/ui/collapsible'

export function AgentCompiledConfig({
  definition,
  collapsible = true,
}: {
  definition: AgentConfig['compiled_definition']
  collapsible?: boolean
}) {
  const json = JSON.stringify(definition, null, 2)
  const content = (
    <CodeBlock
      content={{ copy: json, segments: [{ text: json }], language: 'json' }}
      label="compiled configuration"
    />
  )

  if (!collapsible) return content

  return (
    <Collapsible>
      <CollapsibleTrigger className="group flex items-center gap-2 text-sm font-medium">
        <ChevronDownIcon className="text-muted-foreground size-4 transition-transform group-data-[state=open]:rotate-180" />
        Compiled config (saved)
      </CollapsibleTrigger>
      <CollapsibleContent className="pt-3">{content}</CollapsibleContent>
    </Collapsible>
  )
}
