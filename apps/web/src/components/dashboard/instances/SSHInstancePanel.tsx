import { SSHLiveTerminal } from './SSHLiveTerminal'

interface SSHInstancePanelProps {
  instanceId: string
}

export function SSHInstancePanel({ instanceId }: SSHInstancePanelProps) {
  return (
    <div className="flex h-full flex-col">
      <SSHLiveTerminal instanceId={instanceId} className="mt-6 h-[82vh] min-h-[520px]" />
    </div>
  )
}
