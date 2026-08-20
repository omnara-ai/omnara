import { type AgentTemplate, agentTemplates } from '@/components/agents/agentTemplates'
import { ChevronDownIcon } from '@/components/icons'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

export function AgentTemplateMenu({
  disabled,
  onApply,
}: {
  disabled?: boolean
  onApply: (template: AgentTemplate) => void
}) {
  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button type="button" variant="outline" disabled={disabled}>
          Templates
          <ChevronDownIcon />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-80 p-1.5">
        {agentTemplates.map((template) => (
          <DropdownMenuItem
            key={template.id}
            className="cursor-pointer rounded-md p-2.5"
            onSelect={() => {
              onApply(template)
            }}
          >
            <span className="flex min-w-0 flex-col gap-1">
              <span className="text-sm font-medium">{template.name}</span>
              <span className="text-muted-foreground whitespace-normal text-xs leading-snug">
                {template.description}
              </span>
            </span>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
