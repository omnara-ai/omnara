import { TerminalLiveTerminal } from './TerminalLiveTerminal'

interface TerminalInstancePanelProps {
  instanceId: string
}

export function TerminalInstancePanel({ instanceId }: TerminalInstancePanelProps) {
  return (
    <div className="flex h-full flex-col">
      <TerminalLiveTerminal instanceId={instanceId} className="mt-6 h-[82vh] min-h-[520px]" />
    </div>
  )
}
